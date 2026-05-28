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
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

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
type Mutator interface {
	Close(ctx context.Context, id string) error
	AddLabel(ctx context.Context, id, label string) error
	RemoveLabel(ctx context.Context, id, label string) error
	Note(ctx context.Context, id, text string) error
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
	cursor   int
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

	// tickGen identifies the currently-live tick chain. Each suspend
	// or restart bumps it; stale ticks (e.g. one scheduled before a
	// refresh restart) carry an older gen and are dropped, preventing
	// duplicate tick chains after a terminal-error → recovery cycle.
	tickGen int

	// input is the textinput shared by modeFilter and modeNote. The
	// modes are mutually exclusive — only one prompt is on screen at
	// a time — so a single field is enough; Prompt/Placeholder are
	// reconfigured on entry.
	input textinput.Model
}

// New constructs a Model with the given Source and a sensible default
// preset (all).
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
		// Any key press in list/detail/confirm modes clears the last
		// status banner, since the user has acknowledged it by acting.
		// Prompt modes (filter, note) preserve the banner so the user
		// can see context while typing.
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeConfirmClose:
			return m.updateConfirmClose(msg)
		case modeNote:
			return m.updateNote(msg)
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
	}
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
// anything else cancels.
func (m Model) beginClose() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeConfirmClose
	return m, nil
}

func (m Model) updateConfirmClose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if msg.String() == "y" || msg.String() == "Y" {
		i := m.visible[m.cursor]
		mu := m.mutator()
		m.mode = modeList
		return m, runWrite("close", i.ID, func(ctx context.Context) error {
			return mu.Close(ctx, i.ID)
		})
	}
	// any other key cancels
	m.mode = modeList
	m.status = "close cancelled"
	return m, nil
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
			return mu.RemoveLabel(ctx, i.ID, "human")
		})
	}
	return m, runWrite("flag", i.ID, func(ctx context.Context) error {
		return mu.AddLabel(ctx, i.ID, "human")
	})
}

// beginNote opens the textinput prompt for a new note.
func (m Model) beginNote() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeNote
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
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		i := m.visible[m.cursor]
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if text == "" {
			m.status = "note cancelled (empty)"
			return m, nil
		}
		return m, runWrite("note", i.ID, func(ctx context.Context) error {
			return mu.Note(ctx, i.ID, text)
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

// recomputeVisible applies the in-memory fuzzy filter to m.all.
// "Fuzzy" here is a case-insensitive substring over title and
// description — enough for MVP; a true rank-based matcher can drop
// in later without changing the call site.
func (m *Model) recomputeVisible() {
	if m.query == "" {
		m.visible = m.all
	} else {
		q := strings.ToLower(m.query)
		out := make([]beads.Issue, 0, len(m.all))
		for _, i := range m.all {
			if strings.Contains(strings.ToLower(i.Title), q) ||
				strings.Contains(strings.ToLower(i.Description), q) {
				out = append(out, i)
			}
		}
		m.visible = out
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// View dispatches to the per-mode renderer.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	var b strings.Builder

	header := titleStyle.Render("would-you-kindly")
	b.WriteString(header)
	b.WriteString("\n\n")

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
		b.WriteString("\n")
		if len(m.visible) > 0 {
			b.WriteString(confirmStyle.Render(
				fmt.Sprintf("close %s? [y/N]", m.visible[m.cursor].ID)))
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

func (m Model) renderRow(i beads.Issue, selected bool) string {
	cursor := "  "
	if selected {
		cursor = cursorStyle.Render("▶ ")
	}

	st := statusStyleFor(i.Status)
	icon := st.Render(statusIcon(i.Status))

	id := idStyle.Render(fmt.Sprintf("%-22s", trunc(i.ID, 22)))
	pri := fmt.Sprintf("P%d", i.Priority)

	row := fmt.Sprintf("%s%s  %s  %s  %s", cursor, icon, id, pri, i.Title)

	if i.IsHuman() {
		row += "  " + humanBadge.Render("HUMAN")
	}
	return row
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
		meta += "  " + humanBadge.Render("HUMAN")
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
