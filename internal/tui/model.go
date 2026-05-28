// Package tui is the Bubble Tea interface that drives would-you-kindly.
//
// The model is kept deliberately flat: a single Model struct holds the
// current issues, cursor position, mode (list, detail, filter input),
// and the active preset. Bubble Tea's Update routes key events to
// per-mode handlers that mutate the model and return commands.
package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// titleSource and descSource each expose ONE field of the issue
// list to sahilm/fuzzy. We score the two fields separately and take
// the better of the two per issue, rather than concatenating them
// into a single haystack — concatenation would let a subsequence
// query (e.g. "xy") match across the title→description boundary
// even though those characters live in different fields.
type titleSource []beads.Issue

func (s titleSource) String(i int) string { return s[i].Title }
func (s titleSource) Len() int            { return len(s) }

type descSource []beads.Issue

func (s descSource) String(i int) string { return s[i].Description }
func (s descSource) Len() int            { return len(s) }

// refreshInterval is how often the TUI polls bd for changes. A timer
// keeps things simple and avoids a filesystem-watcher dependency;
// .beads/issues.jsonl rewrites are cheap to re-query.
const refreshInterval = 10 * time.Second

// mode tracks the user's interaction context.
type mode int

const (
	modeList         mode = iota // browsing the issue list
	modeDetail                   // expanded detail view of one issue
	modeFilter                   // / prompt active, typing into textinput
	modeConfirmClose             // y/n confirmation prompt for close
	modeNote                     // text input for a new note
	modeHelp                     // modal listing every keybinding
)

// Source abstracts where issues come from so a test can plug in
// fixtures while the binary uses the real bd CLI. Implementations
// must be safe to call from a Bubble Tea command goroutine and
// respect context cancellation so the program can exit cleanly.
type Source interface {
	Fetch(ctx context.Context, preset filter.Preset) ([]beads.Issue, error)
}

// Mutator is the write side of the bd backend. The TUI checks at
// runtime whether its Source also implements Mutator; if so the
// c / H / n keystrokes dispatch through it. A read-only Source
// remains valid — the write keys show a "read-only" hint instead.
//
// The methods take a full beads.Issue rather than a bare ID so a
// multi-repo Mutator can route on issue.Repo. With bare IDs, two
// workspaces that happen to use the same ID (or any non-prefixed
// scheme) would silently mis-route — see the regression test
// TestMultiBDSource_WriteRoutesByRepoNotID for the case that drove
// this interface shape.
type Mutator interface {
	Close(ctx context.Context, issue beads.Issue) error
	AddLabel(ctx context.Context, issue beads.Issue, label string) error
	RemoveLabel(ctx context.Context, issue beads.Issue, label string) error
	Note(ctx context.Context, issue beads.Issue, text string) error
}

// Model is the Bubble Tea model.
type Model struct {
	src    Source
	keys   keyMap
	mode   mode
	preset filter.Preset
	query  string

	all      []beads.Issue // last full fetch result
	visible  []beads.Issue // after fuzzy filter
	// commonPrefix is the longest shared ID prefix (ending in `-`)
	// across m.all. Recomputed on each fetch; used by displayID to
	// strip noise from the ID column in single-repo mode.
	commonPrefix string
	cursor       int
	width    int
	height   int
	lastErr  error
	lastSync time.Time
	loading  bool // true between a fetch dispatch and its result

	// status is a transient banner shown above the status bar after
	// a write completes ("Closed wyk-42" or an error). It clears on
	// the next user key press, so the next action removes it without
	// needing a timer.
	status string

	// pendingTarget is the full Issue captured at the moment the user
	// entered modeConfirmClose or modeNote. The cursor's position is
	// NOT a safe source of truth at confirm/enter time — an in-flight
	// fetch can re-order or remove issues between the prompt opening
	// and the user's confirmation. We capture the whole Issue (not
	// just the ID) so the Mutator can route on Repo even if the
	// fetched list has moved on. issueExists checks pendingTarget.ID
	// against m.all to detect refetch-removal.
	pendingTarget beads.Issue

	// helpReturnMode is the mode to restore when the user dismisses
	// the ? overlay. ? can be opened from modeList or modeDetail; we
	// drop the user back into whichever they came from.
	helpReturnMode mode

	// tickGen identifies the currently-live tick chain. Each suspend
	// or restart bumps it; stale ticks (e.g. one scheduled before a
	// refresh restart) carry an older gen and are dropped, preventing
	// duplicate tick chains after a terminal-error → recovery cycle.
	tickGen int

	// setupHint is a one-line banner shown above the table — used
	// to nag the user when wyk is running in the empty-registry
	// fallback (single-repo cwd mode) so the multi-repo feature
	// isn't invisible.
	setupHint string

	// input is the textinput shared by modeFilter and modeNote. The
	// modes are mutually exclusive — only one prompt is on screen at
	// a time — so a single field is enough; Prompt/Placeholder are
	// reconfigured on entry.
	input textinput.Model
}

// New constructs a Model with the given Source and a sensible default
// preset (all). For a startup hint banner (e.g. "no repos registered;
// run wyk init -scan ~/Projects to discover them"), use NewWithHint.
func New(src Source) Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "fuzzy filter…"
	ti.CharLimit = 200

	return Model{
		src:     src,
		keys:    defaultKeyMap(),
		mode:    modeList,
		preset:  filter.PresetAll,
		input:   ti,
		loading: true, // first paint shows "loading…" until Init's fetch returns
	}
}

// NewWithHint is New plus a setupHint banner shown above the issue
// list. Used when the caller wants to surface an onboarding nag
// (e.g. empty registry) without forcing all callers to pass a hint.
func NewWithHint(src Source, hint string) Model {
	m := New(src)
	m.setupHint = hint
	return m
}

// Init triggers the first fetch and starts the refresh tick.
func (m Model) Init() tea.Cmd {
	// gen 0 is implicit on the zero-valued Model; the matching tick
	// message carries gen 0 too, so the chain starts coherently.
	return tea.Batch(m.fetchCmd(), tickCmd(m.tickGen))
}

// fetchCmd asks the Source for issues matching the current preset.
// It uses a fresh background context per call; the bd Client applies
// its own per-call timeout. The originating preset is echoed back in
// the result so stale fetches (a tick that arrived while the user was
// switching presets) can be dropped instead of overwriting newer data.
func (m Model) fetchCmd() tea.Cmd {
	src, preset := m.src, m.preset
	return func() tea.Msg {
		issues, err := src.Fetch(context.Background(), preset)
		return fetchedMsg{preset: preset, issues: issues, err: err}
	}
}

func tickCmd(gen int) tea.Cmd {
	return tea.Tick(refreshInterval, func(_ time.Time) tea.Msg { return tickMsg{gen: gen} })
}

type fetchedMsg struct {
	preset filter.Preset
	issues []beads.Issue
	err    error
}

type tickMsg struct{ gen int }

// isTerminalErr reports whether an error is one the auto-refresh tick
// should give up on. These don't self-heal mid-session; the user must
// install bd or move into a workspace and hit `r` to recover.
func isTerminalErr(err error) bool {
	return errors.Is(err, beads.ErrBDNotFound) || errors.Is(err, beads.ErrNoWorkspace)
}

// Update is the main event router.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case fetchedMsg:
		// Drop results from a fetch dispatched for a preset we've
		// since moved off of — otherwise an in-flight tick can clobber
		// the user's newly-selected view.
		if msg.preset != m.preset {
			return m, nil
		}
		// If we're recovering from a terminal-error state (no bd / no
		// workspace) into a successful fetch, the tick chain may have
		// self-suspended in the meantime — there's an interleaving
		// where a tick fires after refresh-restart but before the
		// fetch returns, sees the still-terminal m.lastErr, and
		// retires the chain. Re-arm here so auto-refresh is guaranteed
		// alive whenever we leave the error state.
		recovered := isTerminalErr(m.lastErr) && !isTerminalErr(msg.err)
		m.loading = false
		m.lastSync = time.Now()
		m.lastErr = msg.err
		if msg.err == nil {
			m.all = msg.issues
			m.commonPrefix = commonIDPrefix(m.all)
			m.recomputeVisible()
		}
		if recovered {
			m.tickGen++
			return m, tickCmd(m.tickGen)
		}
		return m, nil

	case tickMsg:
		// Drop ticks from a chain we've already replaced — this
		// happens when a manual refresh restarts the tick before
		// an earlier one has had a chance to fire and self-suspend.
		if msg.gen != m.tickGen {
			return m, nil
		}
		// Suspend the auto-refresh while we're in a terminal error
		// state (no bd / no workspace). Bump the generation so any
		// later refresh starts a fresh chain that supersedes this one.
		if isTerminalErr(m.lastErr) {
			m.tickGen++
			return m, nil
		}
		return m, tea.Batch(m.fetchCmd(), tickCmd(m.tickGen))

	case writeMsg:
		return m.handleWriteResult(msg)

	case tea.KeyMsg:
		// Any keystroke processed in modeList — including the ones
		// that open the filter or note prompts — clears the previous
		// status banner. Once inside a prompt mode, the prompt
		// handlers don't clear m.status on every keystroke; they only
		// set or clear it when the prompt resolves (cancel, submit,
		// vanished-target). So a banner set just before opening a
		// prompt is wiped here on entry, but typing inside the prompt
		// preserves a banner set by the resolution itself.
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeConfirmClose:
			return m.updateConfirmClose(msg)
		case modeNote:
			return m.updateNote(msg)
		case modeHelp:
			return m.updateHelp(msg)
		default:
			m.status = ""
			return m.updateList(msg)
		}
	}

	// Forward any other message (e.g. textinput's cursor-blink ticks)
	// to the focused textinput while the filter prompt is open. Without
	// this the cursor stops blinking after the initial Blink command.
	if m.mode == modeFilter {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Quit):
		return m, tea.Quit
	case keyHit(msg, m.keys.Down):
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case keyHit(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	case keyHit(msg, m.keys.Top):
		m.cursor = 0
	case keyHit(msg, m.keys.Bottom):
		m.cursor = max(0, len(m.visible)-1)
	case keyHit(msg, m.keys.Open):
		if len(m.visible) > 0 {
			m.mode = modeDetail
		}
	case keyHit(msg, m.keys.Filter):
		m.mode = modeFilter
		m.input.SetValue(m.query)
		m.input.Focus()
		return m, textinput.Blink
	case keyHit(msg, m.keys.Human):
		return m.switchPreset(filter.PresetHuman)
	case keyHit(msg, m.keys.Cycle):
		return m.switchPreset(filter.NextPreset(m.preset))
	case keyHit(msg, m.keys.Refresh):
		// Manual refresh also restarts the auto-tick if it was
		// suspended after a terminal error. Bumping tickGen retires
		// any older tick that's still in-flight, so the new chain is
		// the only one alive.
		m.loading = true
		cmds := []tea.Cmd{m.fetchCmd()}
		if isTerminalErr(m.lastErr) {
			m.tickGen++
			cmds = append(cmds, tickCmd(m.tickGen))
		}
		return m, tea.Batch(cmds...)

	case keyHit(msg, m.keys.Close):
		return m.beginClose()
	case keyHit(msg, m.keys.ToggleHuman):
		return m.toggleHuman()
	case keyHit(msg, m.keys.AddNote):
		return m.beginNote()
	case keyHit(msg, m.keys.JumpNextHuman):
		return m.jumpToHuman(+1)
	case keyHit(msg, m.keys.JumpPrevHuman):
		return m.jumpToHuman(-1)
	case keyHit(msg, m.keys.Help):
		return m.openHelp()
	}
	return m, nil
}

// jumpToHuman moves the cursor to the next (dir=+1) or previous
// (dir=-1) issue in m.visible that carries the human label. Wraps.
// If no human-flagged issues are visible, sets a status banner and
// leaves the cursor put.
func (m Model) jumpToHuman(dir int) (tea.Model, tea.Cmd) {
	n := len(m.visible)
	if n == 0 {
		return m, nil
	}
	for offset := 1; offset <= n; offset++ {
		idx := ((m.cursor + dir*offset) % n + n) % n
		if m.visible[idx].IsHuman() {
			m.cursor = idx
			return m, nil
		}
	}
	m.status = "no human-flagged issues in this view"
	return m, nil
}

// --- write actions (Phase 2.B) ------------------------------------

// writeMsg carries the result of a Mutator call back to the model.
// `action` describes what was attempted (used to compose the status
// banner); `id` identifies the affected issue.
type writeMsg struct {
	action string
	id     string
	err    error
}

// mutator returns the Mutator interface if the configured Source
// also implements it. nil means we're in read-only mode and write
// keys should show a "read-only" hint instead of acting.
func (m Model) mutator() Mutator {
	mu, _ := m.src.(Mutator)
	return mu
}

// beginClose enters the confirm-close mode so a stray `c` doesn't
// destroy work. Confirmation is just the next keystroke: y proceeds,
// anything else cancels. The full issue (not just its ID) is captured
// so a concurrent refetch can't shift the cursor onto a different
// issue between the prompt opening and the user's confirmation, AND
// so a multi-repo Mutator can route on Repo even if the fetched list
// has moved on.
func (m Model) beginClose() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeConfirmClose
	m.pendingTarget = m.visible[m.cursor]
	return m, nil
}

func (m Model) updateConfirmClose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	target := m.pendingTarget
	m.pendingTarget = beads.Issue{}
	if msg.String() == "y" || msg.String() == "Y" {
		if !m.issueExists(target.ID) {
			m.mode = modeList
			m.status = "close cancelled: " + target.ID + " was removed from the workspace by a refresh"
			return m, nil
		}
		mu := m.mutator()
		m.mode = modeList
		return m, runWrite("close", target.ID, func(ctx context.Context) error {
			return mu.Close(ctx, target)
		})
	}
	// any other key cancels
	m.mode = modeList
	m.status = "close cancelled"
	return m, nil
}

// issueExists reports whether the given ID is still present in the
// model's last fetched set (m.all, not the post-filter m.visible).
// A fuzzy filter that hides an issue does NOT count as "gone" — the
// user already confirmed the action against a known ID. Used by the
// prompt handlers to detect a refetch that genuinely removed the
// originally-targeted issue.
func (m Model) issueExists(id string) bool {
	for _, i := range m.all {
		if i.ID == id {
			return true
		}
	}
	return false
}

// toggleHuman flips the `human` label on the cursor issue. No
// confirmation — the operation is reversible by toggling again.
func (m Model) toggleHuman() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	i := m.visible[m.cursor]
	mu := m.mutator()
	if i.IsHuman() {
		return m, runWrite("unflag", i.ID, func(ctx context.Context) error {
			return mu.RemoveLabel(ctx, i, "human")
		})
	}
	return m, runWrite("flag", i.ID, func(ctx context.Context) error {
		return mu.AddLabel(ctx, i, "human")
	})
}

// beginNote opens the textinput prompt for a new note. The full
// target issue is captured here for the same reasons as beginClose —
// see Model.pendingTarget.
func (m Model) beginNote() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeNote
	m.pendingTarget = m.visible[m.cursor]
	m.input.SetValue("")
	m.input.Prompt = "note ▸ "
	m.input.Placeholder = "append a note to this issue"
	m.input.Focus()
	return m, textinput.Blink
}

func (m Model) updateNote(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		m.pendingTarget = beads.Issue{}
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if text == "" {
			m.status = "note cancelled (empty)"
			return m, nil
		}
		if !m.issueExists(target.ID) {
			m.status = "note cancelled: " + target.ID + " was removed from the workspace by a refresh"
			return m, nil
		}
		return m, runWrite("note", target.ID, func(ctx context.Context) error {
			return mu.Note(ctx, target, text)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// restoreFilterPrompt resets the shared textinput so the next `/`
// shows the filter UI instead of the note UI.
func (m *Model) restoreFilterPrompt() {
	m.input.Prompt = "/ "
	m.input.Placeholder = "fuzzy filter…"
}

// runWrite wraps a Mutator call in a tea.Cmd that emits a writeMsg.
// All mutators in the Client carry their own per-call timeout, so a
// fresh background context is fine here.
func runWrite(action, id string, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		err := fn(context.Background())
		return writeMsg{action: action, id: id, err: err}
	}
}

// handleWriteResult sets the status banner and triggers a refetch so
// the list reflects the new state. On error, the banner shows the
// failure message; the existing data stays so the user can retry.
func (m Model) handleWriteResult(msg writeMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = fmt.Sprintf("%s %s failed: %s", msg.action, msg.id, msg.err.Error())
		return m, nil
	}
	switch msg.action {
	case "close":
		m.status = "closed " + msg.id
	case "flag":
		m.status = "flagged " + msg.id + " for human"
	case "unflag":
		m.status = "unflagged " + msg.id
	case "note":
		m.status = "noted " + msg.id
	default:
		m.status = msg.action + " " + msg.id
	}
	// Refetch so the list reflects the write. Loading flag isn't set
	// here because the existing data is still valid until the new
	// fetch arrives — flashing "loading…" would just be noise.
	return m, m.fetchCmd()
}

// switchPreset clears the visible rows before dispatching the new
// fetch so the UI doesn't flash the old preset's data under the new
// header. The loading flag distinguishes the in-flight state from a
// genuinely empty result. Any pending fuzzy filter stays — it re-
// applies once the new data arrives.
func (m Model) switchPreset(p filter.Preset) (tea.Model, tea.Cmd) {
	m.preset = p
	m.cursor = 0
	m.all = nil
	m.visible = nil
	m.loading = true
	return m, m.fetchCmd()
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Back), keyHit(msg, m.keys.Open):
		m.mode = modeList
	case keyHit(msg, m.keys.Quit):
		return m, tea.Quit
	case keyHit(msg, m.keys.Help):
		return m.openHelp()
	}
	return m, nil
}

// openHelp captures the current mode and switches to modeHelp; the
// help overlay's dismiss handler restores the captured mode.
func (m Model) openHelp() (tea.Model, tea.Cmd) {
	m.helpReturnMode = m.mode
	m.mode = modeHelp
	return m, nil
}

func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc", "?", "q":
		m.mode = m.helpReturnMode
	}
	return m, nil
}

func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c quits unconditionally; the status bar advertises it and
	// the textinput wouldn't otherwise intercept it.
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		m.query = m.input.Value()
		m.mode = modeList
		m.input.Blur()
		m.recomputeVisible()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.query = m.input.Value()
	m.recomputeVisible()
	return m, cmd
}

// recomputeVisible applies the fuzzy filter to m.all. The matcher
// is rank-based (sahilm/fuzzy): subsequence matches score lower
// than exact substrings, results are sorted best-first, and ties
// fall back to the issue's position in m.all so the cursor doesn't
// jump as the user types.
//
// Title and description are scored independently and merged on the
// max score, which avoids letting a query span the title→description
// boundary (a query "xy" must hit "x" and "y" in the same field).
func (m *Model) recomputeVisible() {
	if m.query == "" {
		m.visible = m.all
		if m.cursor >= len(m.visible) {
			m.cursor = max(0, len(m.visible)-1)
		}
		return
	}

	best := make(map[int]int, len(m.all))
	for _, mt := range fuzzy.FindFrom(m.query, titleSource(m.all)) {
		best[mt.Index] = mt.Score
	}
	for _, mt := range fuzzy.FindFrom(m.query, descSource(m.all)) {
		if s, ok := best[mt.Index]; !ok || mt.Score > s {
			best[mt.Index] = mt.Score
		}
	}

	type scored struct{ idx, score int }
	list := make([]scored, 0, len(best))
	for idx, score := range best {
		list = append(list, scored{idx, score})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].idx < list[j].idx
	})
	out := make([]beads.Issue, 0, len(list))
	for _, s := range list {
		out = append(out, m.all[s.idx])
	}
	m.visible = out

	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// View dispatches to the per-mode renderer.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeHelp:
		return m.viewHelp()
	default:
		return m.viewList()
	}
}

// viewHelp renders the keybinding overlay, grouped so the writes and
// navigation sections don't blur into each other. Source of truth is
// the keymap itself — no copy/paste of help strings.
func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("Keys"))
	b.WriteString("\n")

	groups := []struct {
		title    string
		bindings []key.Binding
	}{
		{"Navigation", []key.Binding{
			m.keys.Up, m.keys.Down, m.keys.Top, m.keys.Bottom,
			m.keys.Open, m.keys.Back,
			m.keys.JumpPrevHuman, m.keys.JumpNextHuman,
		}},
		{"Filters", []key.Binding{m.keys.Filter, m.keys.Human, m.keys.Cycle}},
		{"Writes", []key.Binding{m.keys.Close, m.keys.ToggleHuman, m.keys.AddNote}},
		{"Meta", []key.Binding{m.keys.Refresh, m.keys.Help, m.keys.Quit}},
	}
	for _, g := range groups {
		b.WriteString("\n")
		b.WriteString(detailLabelStyle.Render(g.title))
		b.WriteString("\n")
		for _, kb := range g.bindings {
			h := kb.Help()
			b.WriteString(fmt.Sprintf("  %-6s  %s\n", h.Key, h.Desc))
		}
	}
	b.WriteString("\n")
	b.WriteString(detailLabelStyle.Render("Notes"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  IDs in the table are shown without the repeated workspace prefix\n"))
	b.WriteString(helpStyle.Render("  (e.g. \"ma5.2.1\" stands for \"" + exampleFullID(m) + "ma5.2.1\").\n"))
	b.WriteString(helpStyle.Render("  Press ⏎ to expand a row and see the full ID in the detail view.\n"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("esc / ? / q to close"))
	return b.String()
}

// exampleFullID returns a workspace-prefix string suitable for the
// help text — uses the model's commonPrefix (single-repo) or the
// first multi-repo row's Repo prefix if available. Falls back to a
// generic placeholder if nothing's loaded yet.
func exampleFullID(m Model) string {
	if m.commonPrefix != "" {
		return m.commonPrefix
	}
	for _, i := range m.all {
		if i.Repo != "" {
			return i.Repo + "-"
		}
	}
	return "<workspace>-"
}

func (m Model) viewList() string {
	var b strings.Builder

	header := titleStyle.Render("would-you-kindly")
	b.WriteString(header)
	b.WriteString("\n")
	if m.setupHint != "" {
		b.WriteString(setupHintStyle.Render(m.setupHint))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	switch {
	case m.lastErr != nil:
		b.WriteString(errorStyle.Render(friendlyError(m.lastErr)))
		b.WriteString("\n\n")
		b.WriteString(emptyStyle.Render("press r to retry, q to quit"))
	case m.loading:
		b.WriteString(emptyStyle.Render("loading…"))
	case len(m.all) == 0:
		b.WriteString(emptyStyle.Render("no issues — bd returned an empty list"))
	case len(m.visible) == 0:
		b.WriteString(emptyStyle.Render(fmt.Sprintf("no matches for %q", m.query)))
	default:
		b.WriteString(m.renderHeader())
		b.WriteByte('\n')
		for i, issue := range m.visible {
			b.WriteString(m.renderRow(issue, i == m.cursor))
			b.WriteByte('\n')
		}
	}

	// modal prompts live just above the status bar
	switch m.mode {
	case modeFilter, modeNote:
		b.WriteString("\n")
		b.WriteString(m.input.View())
	case modeConfirmClose:
		// Render the captured ID, not the cursor's current target —
		// a refetch may have shifted things since the prompt opened.
		if m.pendingTarget.ID != "" {
			b.WriteString("\n")
			b.WriteString(confirmStyle.Render(
				fmt.Sprintf("close %s? [y/N]", m.pendingTarget.ID)))
		}
	}

	// status banner (transient write feedback) above the status bar
	if m.status != "" {
		b.WriteString("\n")
		b.WriteString(statusBannerStyle.Render(m.status))
	}

	b.WriteString("\n")
	b.WriteString(m.statusBar())
	return b.String()
}

// Column widths for the list view. Kept as constants so the header
// row and the data rows stay aligned without duplicating numbers.
// The Repo and Branch columns are only rendered in multi-repo mode
// (when at least one fetched Issue carries a populated Repo field).
//
// colID shrank from 22 → 12 once the common prefix trimming landed
// (commonIDPrefix): with the repeated `<prefix>-` stripped, the
// remaining suffix is usually ≤ 8 chars (e.g. `ma5.2.1`), so the
// extra width was just whitespace in every row.
const (
	colRepo    = 18
	colBranch  = 10
	colID      = 12
	colType    = 4
	colStatus  = 8
	colPrio    = 2
	colUpdated = 7
)

// isMultiRepo reports whether the current list has any issue with
// a populated Repo field. The Repo/Branch columns are gated on this
// — they render whenever the source decorates issues. In practice
// every BDSource path now sets a Name (which Fetch uses to populate
// Repo), so this is effectively always true and the columns are
// always on. The gate stays as a safety net: a Source that
// intentionally returns undecorated issues (a stub in tests, or a
// future read-only adapter) still gets the compact layout.
func (m Model) isMultiRepo() bool {
	for _, i := range m.all {
		if i.Repo != "" {
			return true
		}
	}
	return false
}

// displayID returns the ID with its repeated workspace prefix
// stripped — the part that's identical for every row in the same
// view. For multi-repo mode the prefix is `<issue.Repo>-`; for
// single-repo it's the longest common prefix of m.all ending in
// `-`. If no trim applies the original ID is returned.
func (m Model) displayID(i beads.Issue) string {
	if i.Repo != "" {
		// Use the issue's own Repo to pick the prefix so cross-repo
		// rows in the same view each get the right strip.
		if rest, ok := strings.CutPrefix(i.ID, i.Repo+"-"); ok {
			return rest
		}
		return i.ID
	}
	if m.commonPrefix != "" {
		if rest, ok := strings.CutPrefix(i.ID, m.commonPrefix); ok {
			return rest
		}
	}
	return i.ID
}

// commonIDPrefix returns the longest common prefix of every issue's
// ID that ends in `-` so the trimmed suffix is still readable.
// Returns "" if there's no consistent prefix (or fewer than 2 rows).
func commonIDPrefix(issues []beads.Issue) string {
	if len(issues) < 2 {
		return ""
	}
	pref := issues[0].ID
	for _, i := range issues[1:] {
		pref = lcp(pref, i.ID)
		if pref == "" {
			return ""
		}
	}
	if idx := strings.LastIndex(pref, "-"); idx >= 0 {
		return pref[:idx+1]
	}
	return ""
}

// lcp is the longest-common-prefix of two strings.
func lcp(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// renderHeader prints the column-titles row above the issue list,
// followed by a thin divider so the header doesn't visually merge
// with the first data row. The leading two spaces line up with the
// cursor column on data rows so the title and ID columns share a
// left edge. Repo and Branch only appear when the current list
// spans multiple workspaces.
func (m Model) renderHeader() string {
	const cursor = "  "
	var prefix string
	if m.isMultiRepo() {
		prefix = fmt.Sprintf("%-*s  %-*s  ", colRepo, "Repo", colBranch, "Branch")
	}
	h := fmt.Sprintf("%s%s%-*s  %-*s  %-*s  %-*s  %-*s  %s",
		cursor, prefix,
		colID, "ID",
		colType, "T",
		colStatus, "Status",
		colPrio, "P",
		colUpdated, "Updated",
		"Title",
	)
	return tableHeaderStyle.Render(h)
}

func (m Model) renderRow(i beads.Issue, selected bool) string {
	cursor := "  "
	if selected {
		cursor = cursorStyle.Render("▶ ")
	}

	var prefix string
	if m.isMultiRepo() {
		repo := typeStyle.Render(fmt.Sprintf("%-*s", colRepo, trunc(i.Repo, colRepo)))
		br := typeStyle.Render(fmt.Sprintf("%-*s", colBranch, trunc(i.Branch, colBranch)))
		prefix = repo + "  " + br + "  "
	}

	id := idStyle.Render(fmt.Sprintf("%-*s", colID, trunc(m.displayID(i), colID)))
	tp := typeStyle.Render(fmt.Sprintf("%-*s", colType, abbrevType(i.IssueType)))
	st := statusStyleFor(i.Status).Render(fmt.Sprintf("%-*s", colStatus, abbrevStatus(i.Status)))
	pri := fmt.Sprintf("P%d", i.Priority)
	upd := updatedStyle.Render(fmt.Sprintf("%-*s", colUpdated, relTime(i.UpdatedAt)))

	row := fmt.Sprintf("%s%s%s  %s  %s  %s  %s  %s", cursor, prefix, id, tp, st, pri, upd, i.Title)
	if i.IsHuman() {
		row += "  " + humanBadgeFor(i)
	}
	return row
}

// humanBadgeFor distinguishes the two states the contract supports:
// src:agent means an agent handed this back ("← HUMAN"), src:human
// means the person filed it themselves ("· HUMAN"). The leading
// glyph is the cheap, high-readability signal; a hover or expanded
// view could carry more.
func humanBadgeFor(i beads.Issue) string {
	switch {
	case i.HasLabel("src:agent"):
		return humanBadgeAgent.Render("← HUMAN")
	case i.HasLabel("src:human"):
		return humanBadgeSelf.Render("· HUMAN")
	default:
		return humanBadge.Render("HUMAN")
	}
}

// abbrevType returns a fixed-width type slug. Most bd types fit in
// 4 chars natively (task, bug, epic); the longer ones are truncated
// to the same width so column alignment holds.
func abbrevType(t string) string {
	if len(t) <= colType {
		return t
	}
	return t[:colType]
}

// abbrevStatus normalises bd's status names for the table column.
// "in_progress" gets the conventional "wip" because the full string
// would dominate the row width and 'wip' is unambiguous in context.
func abbrevStatus(s string) string {
	if s == "in_progress" {
		return "wip"
	}
	return s
}

// relTime renders a coarse "how long ago" stamp for the Updated
// column. Bins (now / <1h / <1d / <30d / older) keep the column
// narrow without losing the rough age signal a triage reader wants.
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

func (m Model) viewDetail() string {
	if len(m.visible) == 0 {
		return ""
	}
	i := m.visible[m.cursor]

	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render(i.Title))
	b.WriteString("\n")

	meta := fmt.Sprintf("%s  %s  %s  P%d",
		idStyle.Render(i.ID),
		statusStyleFor(i.Status).Render(i.Status),
		i.IssueType,
		i.Priority,
	)
	if i.IsHuman() {
		meta += "  " + humanBadgeFor(i)
	}
	b.WriteString(meta)
	b.WriteString("\n\n")

	if len(i.Labels) > 0 {
		b.WriteString(detailLabelStyle.Render("labels: "))
		b.WriteString(strings.Join(i.Labels, ", "))
		b.WriteString("\n\n")
	}

	b.WriteString(detailLabelStyle.Render("instructions"))
	b.WriteString("\n")
	if strings.TrimSpace(i.Description) == "" {
		b.WriteString(emptyStyle.Render("(no description)"))
	} else {
		b.WriteString(i.Description)
	}

	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("esc / enter: back   q: quit"))
	return b.String()
}

func (m Model) statusBar() string {
	left := fmt.Sprintf("[%s]  %d/%d", m.preset, len(m.visible), len(m.all))
	if m.query != "" {
		left += fmt.Sprintf("  filter:%q", m.query)
	}
	if !m.lastSync.IsZero() {
		left += "  synced " + m.lastSync.Format("15:04:05")
	}
	help := "j/k  ⏎ open  / filter  h human  tab  r refresh  c close  H ±human  n note  q quit"
	if m.mutator() == nil {
		help = "j/k  ⏎ open  / filter  h human  tab  r refresh  q quit  (read-only)"
	}
	gap := " "
	if m.width > 0 {
		need := lipgloss.Width(left) + lipgloss.Width(help) + 2
		if need < m.width {
			gap = strings.Repeat(" ", m.width-need)
		}
	}
	return statusBarStyle.Render(left + gap + help)
}

func keyHit(msg tea.KeyMsg, b key.Binding) bool {
	return key.Matches(msg, b)
}

func friendlyError(err error) string {
	switch {
	case errors.Is(err, beads.ErrBDNotFound):
		return "bd is not installed (or not on PATH). Install from https://github.com/gastownhall/beads"
	case errors.Is(err, beads.ErrNoWorkspace):
		return "no beads workspace here. Run `bd init` in your repo root."
	default:
		return "error: " + err.Error()
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
