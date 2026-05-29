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
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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
	modeQuickAdd                 // text input for a new issue title
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
	// Create files a new issue with the given title in the named
	// workspace. The repo arg is the BDSource/sub name; single-repo
	// implementations ignore it. The new issue is labeled src:human
	// (the user filed it) by convention. Returns the new ID.
	Create(ctx context.Context, repo, title string) (string, error)
}

// Detailer is the "fetch the full issue for the detail view"
// interface. bd's list/query endpoints return slim Issues (bd list
// drops Description, bd query drops Notes), so the detail view
// needs a separate Show call to render the full record. Optional —
// when the Source doesn't satisfy this, the detail view falls back
// to whatever the original fetch returned.
type Detailer interface {
	Detail(ctx context.Context, issue beads.Issue) (beads.Issue, error)
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

	// detailIssue is the enriched (full-field, includes notes) issue
	// shown in the detail view. Populated by a Detail Cmd dispatched
	// on enter; before the result arrives the view falls back to the
	// slim Issue from m.visible.
	detailIssue beads.Issue

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

	// fetchErrors holds the per-sub failures from the most recent
	// MultiBDSource.Fetch — populated only when src satisfies
	// MultiSource (multi-repo). Rendered as a banner above the help
	// bar so a sub that errors out doesn't disappear silently.
	fetchErrors []FetchError

	// refreshing is true while a manual-`r` or preset-switch
	// fetch is in flight. Unlike loading (which gates the whole
	// view), refreshing only triggers a subtle indicator in the
	// status bar so the existing rows stay on screen during the
	// round-trip. Cleared on fetchedMsg arrival.
	refreshing bool

	// updateNudge is a one-line "↑ wyk vX.Y.Z available — run
	// `wyk update`" message read from the updater cache at start-
	// up. Rendered above the help bar when non-empty so the user
	// sees the upgrade path inline; the cache refresh happens out
	// of band in main's background goroutine.
	updateNudge string

	// scroll is the row index at the top of the rendered window —
	// used to keep the column header visible when m.visible has
	// more rows than the terminal can fit. Without it, the terminal
	// scrolls overflow off the top and the header disappears.
	// Maintained by ensureCursorVisible whenever the cursor moves
	// or the data set changes shape.
	scroll int

	// detailVP scrolls the detail view's body (description +
	// notes) so long runbooks stay readable without dropping out
	// to a pager. The header lines (title, meta, badge) stay
	// fixed above the viewport so the row's identity never
	// scrolls off. Initialised in New, sized on WindowSizeMsg,
	// content set on entry to modeDetail.
	detailVP viewport.Model

	// spinner animates the first-paint loading state. Replaces
	// the static "loading…" word so the user sees the TUI is
	// actually doing something during the initial bd fetch.
	spinner spinner.Model

	// help renders the one-line footer from the keymap so the
	// status bar's hint can't drift from the actual bindings.
	// Configured to show writes only when the source is a Mutator
	// via the source-of-truth swap in statusBar.
	help help.Model

	// priorityCap caps the visible rows at "<= Pn" so the most
	// common triage move ('show me only the urgent stuff') is a
	// single keystroke. -1 means no cap (the default; all rows
	// pass). 0..3 maps to the digit keys 1..4 (1 → P0 only, 2 →
	// P0..P1, etc.); the "0" key clears the cap.
	priorityCap int

	// statusGen rises on every m.status assignment so a stale
	// auto-clear tick (from a previous status that has since been
	// overwritten) can't wipe the current one.
	statusGen int

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

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = setupHintStyle

	h := help.New()
	// The status-bar palette already supplies its own bg/fg; the
	// help component shouldn't inject a separate background colour
	// or the bindings render in a different shade than the line
	// they're embedded in.
	h.Styles.ShortKey = helpStyle
	h.Styles.ShortDesc = helpStyle
	h.Styles.ShortSeparator = helpStyle.Copy()

	return Model{
		src:         src,
		keys:        defaultKeyMap(),
		mode:        modeList,
		preset:      filter.PresetAll,
		input:       ti,
		loading:     true, // first paint shows "loading…" until Init's fetch returns
		detailVP:    viewport.New(80, 20),
		spinner:     sp,
		help:        h,
		priorityCap: -1,
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

// WithUpdateNudge returns a copy of the model with the update nudge
// string set. main reads the updater cache at startup and feeds
// the result here when there's a newer release available; the
// model renders it as a one-line banner above the help bar.
func (m Model) WithUpdateNudge(nudge string) Model {
	m.updateNudge = nudge
	return m
}

// Init triggers the first fetch and starts the refresh tick. Also
// kicks the spinner so the loading indicator animates from frame 0.
func (m Model) Init() tea.Cmd {
	// gen 0 is implicit on the zero-valued Model; the matching tick
	// message carries gen 0 too, so the chain starts coherently.
	return tea.Batch(m.fetchCmd(), tickCmd(m.tickGen), m.spinner.Tick)
}

// fetchCmd asks the Source for issues matching the current preset.
// It uses a fresh background context per call; the bd Client applies
// its own per-call timeout. The originating preset is echoed back in
// the result so stale fetches (a tick that arrived while the user was
// switching presets) can be dropped instead of overwriting newer data.
//
// When the Source is a MultiSource, per-sub errors are pulled
// atomically with the issues (via FetchWithSubErrors) so a
// concurrent next-tick fetch cannot interleave its errors with this
// fetch's rows.
func (m Model) fetchCmd() tea.Cmd {
	src, preset := m.src, m.preset
	return func() tea.Msg {
		ctx := context.Background()
		if ms, ok := src.(MultiSource); ok {
			issues, subErrs, err := ms.FetchWithSubErrors(ctx, preset)
			return fetchedMsg{preset: preset, issues: issues, subErrs: subErrs, err: err}
		}
		issues, err := src.Fetch(ctx, preset)
		return fetchedMsg{preset: preset, issues: issues, err: err}
	}
}

func tickCmd(gen int) tea.Cmd {
	return tea.Tick(refreshInterval, func(_ time.Time) tea.Msg { return tickMsg{gen: gen} })
}

type fetchedMsg struct {
	preset  filter.Preset
	issues  []beads.Issue
	subErrs []FetchError
	err     error
}

type tickMsg struct{ gen int }

// flashClearMsg auto-clears m.status after a short delay so a
// "closed wyk-42" banner doesn't linger forever when the user
// goes idle. Tagged with statusGen so a stale clear (status was
// overwritten before the timer fired) can't wipe the current one.
type flashClearMsg struct{ gen int }

// flashClearDelay is how long a SUCCESS status banner sticks
// before auto-clearing. Short enough not to feel stale on the
// next glance; long enough to read. var (not const) so tests can
// lower it to keep `go test` snappy — tea.Tick blocks the
// invoking goroutine for the full delay, and the test suite drains
// these commands synchronously.
//
// Failure banners are NOT auto-cleared (see handleWriteResult's
// error branch): a user who glances away during a bd write
// shouldn't lose the error text before they can read it.
var flashClearDelay = 4 * time.Second

func flashClearCmd(gen int) tea.Cmd {
	return tea.Tick(flashClearDelay, func(_ time.Time) tea.Msg {
		return flashClearMsg{gen: gen}
	})
}

// detailMsg carries the enriched Issue back from a Detail dispatch.
// See Update's modeDetail entry branch.
type detailMsg struct {
	issue beads.Issue
	err   error
}

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
		// Resize can shrink the body below the cursor — re-clamp
		// the scroll so the cursor stays in the viewport.
		m.ensureCursorVisible()
		// Resize the detail viewport too. detailChromeHeight
		// reserves space for the fixed header (title + badge +
		// meta + labels + section title) and the footer help
		// line; the rest goes to the scrollable body.
		bodyH := msg.Height - detailChromeHeight
		if bodyH < 1 {
			bodyH = 1
		}
		m.detailVP.Width = msg.Width
		m.detailVP.Height = bodyH
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
		m.refreshing = false
		m.lastSync = time.Now()
		m.lastErr = msg.err
		if msg.err == nil {
			m.all = msg.issues
			m.commonPrefix = commonIDPrefix(m.all)
			m.recomputeVisible()
			// New row count → cursor may now sit outside the
			// viewport, or m.scroll may exceed maxScroll.
			m.ensureCursorVisible()
		}
		// Per-sub fetch errors travel on the msg itself so they
		// always reflect THIS fetch — not a concurrent one that
		// happened to win the race for shared state. Always
		// assigned so a partial-failure → total-failure transition
		// clears the per-sub banner cleanly.
		m.fetchErrors = msg.subErrs
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

	case spinner.TickMsg:
		// Animate the loading indicator. Only re-tick while we're
		// actually showing it (the first paint, before data lands)
		// to avoid burning CPU on every other view.
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case flashClearMsg:
		// Stale clears (status was overwritten before the timer
		// fired) carry an older gen and are dropped — only the
		// active gen actually clears.
		if msg.gen == m.statusGen {
			m.status = ""
		}
		return m, nil

	case detailMsg:
		// Late-arriving Detail result. Only adopt it if the user
		// is still looking at the same issue — otherwise the
		// notes would attach to the wrong row.
		if m.mode == modeDetail && msg.err == nil && msg.issue.ID == m.detailIssue.ID {
			m.detailIssue = msg.issue
			// Re-seed the viewport now that notes have arrived.
			// Preserve scroll offset: a user who'd already paged
			// to line 40 shouldn't be yanked back to the top.
			prev := m.detailVP.YOffset
			m.detailVP.SetContent(detailBody(m.detailIssue))
			m.detailVP.SetYOffset(prev)
		}
		return m, nil

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
		case modeQuickAdd:
			return m.updateQuickAdd(msg)
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
		m.ensureCursorVisible()
	case keyHit(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureCursorVisible()
	case keyHit(msg, m.keys.Top):
		m.cursor = 0
		m.ensureCursorVisible()
	case keyHit(msg, m.keys.Bottom):
		m.cursor = max(0, len(m.visible)-1)
		m.ensureCursorVisible()
	case keyHit(msg, m.keys.Open):
		if len(m.visible) > 0 {
			m.mode = modeDetail
			// Stage the slim row immediately so the view renders
			// with title/description from the list, then dispatch
			// a Detail call to enrich with notes asynchronously.
			m.detailIssue = m.visible[m.cursor]
			// Seed the viewport with the body we have now (notes
			// may be empty until the Detail Cmd resolves); reset
			// scroll to the top so a previous detail view's scroll
			// position doesn't bleed in.
			m.detailVP.SetContent(detailBody(m.detailIssue))
			m.detailVP.GotoTop()
			if d, ok := m.src.(Detailer); ok {
				target := m.detailIssue
				return m, func() tea.Msg {
					full, err := d.Detail(context.Background(), target)
					return detailMsg{issue: full, err: err}
				}
			}
		}
	case keyHit(msg, m.keys.Filter):
		m.mode = modeFilter
		m.input.SetValue(m.query)
		m.input.Focus()
		m.ensureCursorVisible()
		return m, textinput.Blink
	case keyHit(msg, m.keys.Human):
		return m.switchPreset(filter.PresetHuman)
	case keyHit(msg, m.keys.Cycle):
		return m.switchPreset(filter.NextPreset(m.preset))
	case keyHit(msg, m.keys.FilterP0):
		return m.setPriorityCap(0)
	case keyHit(msg, m.keys.FilterP1):
		return m.setPriorityCap(1)
	case keyHit(msg, m.keys.FilterP2):
		return m.setPriorityCap(2)
	case keyHit(msg, m.keys.FilterP3):
		return m.setPriorityCap(3)
	case keyHit(msg, m.keys.FilterPAll):
		return m.setPriorityCap(-1)
	case keyHit(msg, m.keys.Refresh):
		// Manual refresh also restarts the auto-tick if it was
		// suspended after a terminal error. Bumping tickGen retires
		// any older tick that's still in-flight, so the new chain is
		// the only one alive.
		//
		// We do NOT set m.loading here: the existing rows stay on
		// screen while the refresh runs in the background, and a
		// small ↻ glyph appears in the status bar (see statusBar).
		// Replacing the table with "loading…" on every keypress
		// produced a jarring full-canvas blank.
		m.refreshing = true
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
	case keyHit(msg, m.keys.QuickAdd):
		return m.beginQuickAdd()
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
			m.ensureCursorVisible()
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
		m.ensureCursorVisible()
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeConfirmClose
	m.pendingTarget = m.visible[m.cursor]
	// Modal entry adds 2 lines of chrome — re-clamp scroll so the
	// cursor stays in the now-smaller viewport.
	m.ensureCursorVisible()
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

// beginQuickAdd opens a title prompt and on enter files a new issue
// in the repo of the cursor's current row (or the first registered
// workspace if no row is selected). The issue is labeled src:human.
func (m Model) beginQuickAdd() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		m.ensureCursorVisible()
		return m, nil
	}
	m.mode = modeQuickAdd
	// Capture the cursor's repo so the new issue lands in the same
	// workspace the user is currently looking at. Empty means
	// "first registered repo" in multi-repo mode, or "the one and
	// only client" in single-repo.
	if len(m.visible) > 0 && m.cursor < len(m.visible) {
		m.pendingTarget = beads.Issue{Repo: m.visible[m.cursor].Repo}
	}
	m.input.SetValue("")
	m.input.Prompt = "new ▸ "
	m.input.Placeholder = "title for the new issue"
	m.input.Focus()
	m.ensureCursorVisible()
	return m, textinput.Blink
}

func (m Model) updateQuickAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		title := strings.TrimSpace(m.input.Value())
		repo := m.pendingTarget.Repo
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if title == "" {
			m.status = "quick-add cancelled (empty title)"
			return m, nil
		}
		return m, runQuickAdd(func(ctx context.Context) (string, error) {
			return mu.Create(ctx, repo, title)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// runQuickAdd wraps Mutator.Create in a tea.Cmd that emits a writeMsg
// with the new ID populated as id. handleWriteResult then displays
// the "created <id>" banner and refetches.
func runQuickAdd(fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		id, err := fn(context.Background())
		return writeMsg{action: "create", id: id, err: err}
	}
}

// beginNote opens the textinput prompt for a new note. The full
// target issue is captured here for the same reasons as beginClose —
// see Model.pendingTarget.
func (m Model) beginNote() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		m.ensureCursorVisible()
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
	m.ensureCursorVisible()
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
		// Create failure has no ID yet — render without the empty
		// "id" slot to keep the message clean (no double-space).
		if msg.id == "" {
			m.setStatus(fmt.Sprintf("%s failed: %s", msg.action, msg.err.Error()))
		} else {
			m.setStatus(fmt.Sprintf("%s %s failed: %s", msg.action, msg.id, msg.err.Error()))
		}
		// Errors stay until the next user action (any keystroke in
		// updateList clears m.status). A 4s auto-wipe is too short
		// for a user who glances away to read the full bd
		// complaint.
		return m, nil
	}
	switch msg.action {
	case "close":
		m.setStatus("closed " + msg.id)
	case "flag":
		m.setStatus("flagged " + msg.id + " for human")
	case "unflag":
		m.setStatus("unflagged " + msg.id)
	case "note":
		m.setStatus("noted " + msg.id)
	case "create":
		m.setStatus("created " + msg.id)
	default:
		m.setStatus(msg.action + " " + msg.id)
	}
	// Refetch so the list reflects the write. Loading flag isn't set
	// here because the existing data is still valid until the new
	// fetch arrives — flashing "loading…" would just be noise.
	return m, tea.Batch(m.fetchCmd(), flashClearCmd(m.statusGen))
}

// setStatus is the single seam for setting the transient status
// banner. Bumps statusGen so any in-flight flashClearCmd from a
// previous status can't wipe the new one, and re-clamps the
// viewport since the new line shrinks bodyHeight by one. Pointer
// receiver because callers use it on a value Model — Go promotes
// it via &m at the call site.
func (m *Model) setStatus(s string) {
	m.status = s
	m.statusGen++
	m.ensureCursorVisible()
}

// switchPreset clears the visible rows before dispatching the new
// fetch so the UI doesn't flash the old preset's data under the new
// header. The previous preset's rows stay visible until the new
// fetch returns — clearing them would blank the screen for the
// duration of the bd round-trip. The refreshing indicator in the
// status bar signals that the on-screen data is stale-for-this-
// preset; the cursor resets to 0 so the user lands at the top of
// the new view as soon as data arrives. Any pending fuzzy filter
// stays — it re-applies once the new data arrives.
func (m Model) switchPreset(p filter.Preset) (tea.Model, tea.Cmd) {
	m.preset = p
	m.cursor = 0
	m.scroll = 0
	m.refreshing = true
	return m, m.fetchCmd()
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Back), keyHit(msg, m.keys.Open):
		m.mode = modeList
		return m, nil
	case keyHit(msg, m.keys.Quit):
		return m, tea.Quit
	case keyHit(msg, m.keys.Help):
		return m.openHelp()
	}
	// Forward any other key (j/k/PgUp/PgDn/g/G inside the
	// detail view, mouse wheel events, etc.) to the viewport so
	// the body scrolls.
	var cmd tea.Cmd
	m.detailVP, cmd = m.detailVP.Update(msg)
	return m, cmd
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
		m.ensureCursorVisible()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.query = m.input.Value()
	m.recomputeVisible()
	m.ensureCursorVisible()
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
	// Apply the priority cap first so the fuzzy ranking only ever
	// runs over rows the user actually wants to see. -1 means "no
	// cap"; the test below short-circuits.
	pool := m.all
	if m.priorityCap >= 0 {
		filtered := make([]beads.Issue, 0, len(m.all))
		for _, i := range m.all {
			if i.Priority <= m.priorityCap {
				filtered = append(filtered, i)
			}
		}
		pool = filtered
	}

	if m.query == "" {
		m.visible = pool
		if m.cursor >= len(m.visible) {
			m.cursor = max(0, len(m.visible)-1)
		}
		return
	}

	best := make(map[int]int, len(pool))
	for _, mt := range fuzzy.FindFrom(m.query, titleSource(pool)) {
		best[mt.Index] = mt.Score
	}
	for _, mt := range fuzzy.FindFrom(m.query, descSource(pool)) {
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
		out = append(out, pool[s.idx])
	}
	m.visible = out

	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// setPriorityCap updates the priority filter and re-runs the
// visible-row pipeline. Cursor resets to 0 since the previous
// position is meaningless against a different filter; scroll
// re-clamps so the (now smaller or larger) list doesn't leave the
// cursor offscreen.
func (m Model) setPriorityCap(cap int) (tea.Model, tea.Cmd) {
	m.priorityCap = cap
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
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
		{"Writes", []key.Binding{m.keys.Close, m.keys.ToggleHuman, m.keys.AddNote, m.keys.QuickAdd}},
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
	// Filter chip strip — preset always shown; priority chip only
	// when the user has set a cap. Renders blank for the default
	// state (preset=all, no priority) so a fresh view stays
	// chrome-free.
	if chips := renderFilterChips(m.preset, m.priorityCap); chips != "" {
		b.WriteString(chips)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Render the table whenever we have data. Transient states
	// (a flaky fetch error, an in-flight refresh) become banners
	// at the bottom instead of taking over the whole view — the
	// user always sees the most recent rows. Only the very first
	// paint, before any data has arrived, shows the full-screen
	// "loading…" / error stand-in.
	switch {
	case len(m.all) > 0:
		b.WriteString(m.renderHeader())
		b.WriteByte('\n')
		if len(m.visible) == 0 {
			// Preset-aware empty copy. The default "no rows for
			// this filter" line is honest but uninspiring; the
			// human preset specifically gets a celebratory line
			// since "nothing for me to do" is a great state.
			b.WriteString(emptyStyle.Render(emptyMatchCopy(m.preset, m.query)))
		} else {
			// Sticky-header viewport: pick a window around the
			// cursor instead of dumping every row and letting the
			// terminal scroll the header off the top. bodyHeight
			// for rendering uses the same computation as
			// ensureCursorVisible — when they agree, the cursor
			// can never be outside the rendered window.
			h := m.bodyHeight()
			start := m.scroll
			end := start + h
			if end > len(m.visible) {
				end = len(m.visible)
			}
			if start > end {
				start = end
			}
			for i := start; i < end; i++ {
				b.WriteString(m.renderRow(m.visible[i], i == m.cursor))
				b.WriteByte('\n')
			}
			// "+N more above/below" hints when the window doesn't
			// show everything. Subtle, single line each; only the
			// non-zero side renders so a fully-visible list stays
			// chrome-free.
			if start > 0 {
				b.WriteString(emptyStyle.Render(fmt.Sprintf("  ↑ %d more above", start)))
				b.WriteByte('\n')
			}
			if end < len(m.visible) {
				b.WriteString(emptyStyle.Render(fmt.Sprintf("  ↓ %d more below", len(m.visible)-end)))
				b.WriteByte('\n')
			}
		}
	case m.lastErr != nil:
		b.WriteString(errorStyle.Render(friendlyError(m.lastErr)))
		b.WriteString("\n\n")
		b.WriteString(emptyStyle.Render("press r to retry, q to quit"))
	case m.loading:
		b.WriteString(m.spinner.View())
		b.WriteString(emptyStyle.Render(" loading…"))
	case m.preset != filter.PresetAll:
		// Non-default preset with zero rows AND zero matches —
		// celebrate / explain per preset rather than rendering
		// the first-run copy that assumes bd is fresh.
		b.WriteString(emptyStyle.Render(emptyMatchCopy(m.preset, m.query)))
	default:
		b.WriteString(emptyStyle.Render(firstRunEmptyCopy()))
	}

	// modal prompts live just above the status bar
	switch m.mode {
	case modeFilter, modeNote, modeQuickAdd:
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

	// transient-fetch-error banner: when we have stale data on
	// screen but the most recent refresh errored, surface it as a
	// one-line banner instead of replacing the table. Without
	// this, a flaky bd query during an auto-refresh tick would
	// wipe the visible rows until the next tick recovered — the
	// "screen blanks on refresh" symptom.
	//
	// Terminal errors (bd missing, no workspace) also suspend the
	// auto-refresh tick, so the user needs an explicit cue to
	// press r and re-arm — append the retry hint in that case so
	// the recovery path stays discoverable. Transient errors
	// don't need it: the next 10s tick will retry on its own.
	if m.lastErr != nil && len(m.all) > 0 {
		msg := "refresh failed: " + friendlyError(m.lastErr)
		if isTerminalErr(m.lastErr) {
			msg += " — press r to retry"
		}
		b.WriteString("\n")
		b.WriteString(fetchErrorStyle.Render(msg))
	}

	// fetch-error banner: per-sub Fetch failures from a multi-repo
	// source. Surfaces above the transient status banner so it isn't
	// overwritten by write feedback. Re-rendered every paint from
	// m.fetchErrors so it tracks the latest fetch. Bounded by
	// m.width so several repos with long names can't wrap.
	if len(m.fetchErrors) > 0 {
		b.WriteString("\n")
		b.WriteString(fetchErrorStyle.Render(renderFetchErrorBanner(m.fetchErrors, m.width)))
	}

	// status banner (transient write feedback) above the status bar
	if m.status != "" {
		b.WriteString("\n")
		b.WriteString(statusBannerStyle.Render(m.status))
	}

	// update nudge: read from the updater cache at startup, shown
	// just above the status bar so the upgrade path is in view
	// without competing with the more dynamic status/fetch-error
	// lines above. Same amber-italic styling as setupHint — these
	// are both "thing you should do" prompts.
	if m.updateNudge != "" {
		b.WriteString("\n")
		b.WriteString(setupHintStyle.Render(m.updateNudge))
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
	colResp    = 13 // responsibility column: " ← HUMAN ", " · HUMAN ", " AGENT ", " HUMAN-BLOCK ", or blank. 13 = " HUMAN-BLOCK " visual width (Padding(0,1) + 11-char content), the widest variant. Shorter badges get trailing whitespace. Placed second-from-left to put the most important "whose move is it" signal where the eye lands first.
	colWyk     = 3 // wyk-hook indicator: ✓ if installed, blank if not. Header reads "wyk" so the column is self-explanatory.
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
	// human column is always present so the badge has a stable
	// home regardless of single/multi-repo mode. Header lower-
	// cased to match `wyk` — both are indicator columns rather
	// than data ones.
	respCol := fmt.Sprintf("%-*s  ", colResp, "owner")
	var prefix string
	if m.isMultiRepo() {
		prefix = fmt.Sprintf("%-*s  %-*s  %-*s  ",
			colWyk, "wyk",
			colRepo, "Repo",
			colBranch, "Branch",
		)
	}
	h := fmt.Sprintf("%s%s%s%-*s  %-*s  %-*s  %-*s  %-*s  %s",
		cursor, respCol, prefix,
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

	respCol := paddedResponsibilityBadge(i) + "  "

	var prefix string
	if m.isMultiRepo() {
		// Center the ✓ under the "wyk" header (3 chars wide).
		// blank for not-hooked stays the same width so the next
		// column aligns regardless of state.
		w := strings.Repeat(" ", colWyk)
		if i.WykHooked {
			w = " " + wykIndicatorStyle.Render("✓") + " "
		}
		repo := typeStyle.Render(fmt.Sprintf("%-*s", colRepo, trunc(i.Repo, colRepo)))
		br := typeStyle.Render(fmt.Sprintf("%-*s", colBranch, trunc(i.Branch, colBranch)))
		prefix = w + "  " + repo + "  " + br + "  "
	}

	id := idStyle.Render(fmt.Sprintf("%-*s", colID, trunc(m.displayID(i), colID)))
	tp := typeStyle.Render(fmt.Sprintf("%-*s", colType, abbrevType(i.IssueType)))
	st := statusStyleFor(i.Status).Render(fmt.Sprintf("%-*s", colStatus, abbrevStatus(i.Status)))
	pri := fmt.Sprintf("P%d", i.Priority)
	upd := updatedStyle.Render(fmt.Sprintf("%-*s", colUpdated, relTime(i.UpdatedAt)))

	// Truncate the title to whatever space remains after every
	// preceding column. Without this, long titles wrap or overflow
	// the right edge — most existing rows in real use spill past
	// the terminal. Detail view (enter) still shows the full text.
	title := i.Title
	if avail := m.titleBudget(); avail > 0 {
		title = trunc(title, avail)
	}
	return fmt.Sprintf("%s%s%s%s  %s  %s  %s  %s  %s", cursor, respCol, prefix, id, tp, st, pri, upd, title)
}

// titleBudget returns how many runes are available for the title
// column given the current terminal width and the fixed widths of
// every preceding column. Returns 0 when m.width is unknown (before
// the first WindowSizeMsg) so we just print the full title — the
// next paint will redraw with the right budget. A 20-rune floor
// keeps the column from collapsing to nothing on absurdly narrow
// panes; the user can widen and re-render.
func (m Model) titleBudget() int {
	if m.width <= 0 {
		return 0
	}
	// Each "  " separator is 2 spaces; we count one after every
	// non-final column to mirror what renderRow prints.
	const sep = 2
	used := 2 // cursor (▶ or 2 spaces is 2 visual cols either way)
	used += colResp + sep
	if m.isMultiRepo() {
		used += colWyk + sep
		used += colRepo + sep
		used += colBranch + sep
	}
	used += colID + sep
	used += colType + sep
	used += colStatus + sep
	used += colPrio + sep // "Pn" is 2 chars
	used += colUpdated + sep
	avail := m.width - used
	if avail < 20 {
		avail = 20 // floor so we don't render an empty title cell
	}
	return avail
}

// paddedResponsibilityBadge renders the per-row responsibility cell
// so the column stays aligned regardless of badge presence/variant.
// Rows with no responsibility signal (no human label and no
// src:agent label) emit colResp spaces; flagged rows emit the
// styled badge padded out to the same visual width with trailing
// blanks. We can't just %-*s the badge string because lipgloss
// escape codes would be counted as visual width by fmt.
func paddedResponsibilityBadge(i beads.Issue) string {
	badge := responsibilityBadgeFor(i)
	if badge == "" {
		return strings.Repeat(" ", colResp)
	}
	pad := colResp - lipgloss.Width(badge)
	if pad > 0 {
		badge += strings.Repeat(" ", pad)
	}
	return badge
}

// responsibilityBadgeFor returns the badge for the "owner" column,
// telling the reader whose move it is. Three branches in
// precedence order:
//   - has `human` label → one of the HUMAN variants (the
//     human-needs-to-act signal trumps everything else)
//   - has `src:agent` and no `human` → AGENT (the inbox case; the
//     agent has the next move and the convention says they should
//     be acting on it)
//   - otherwise → empty (no responsibility signal applies)
//
// The HUMAN variants distinguish source: "← HUMAN" for agent-filed
// (hot pink, the "needs your attention" hand-back), "· HUMAN" for
// human-filed (muted blue), bare "HUMAN" for legacy issues with no
// src label.
func responsibilityBadgeFor(i beads.Issue) string {
	if i.IsHuman() {
		switch {
		case i.HasLabel("src:agent"):
			return humanBadgeAgent.Render("← HUMAN")
		case i.HasLabel("src:human"):
			return humanBadgeSelf.Render("· HUMAN")
		default:
			return humanBadge.Render("HUMAN")
		}
	}
	if i.HasLabel("src:agent") {
		// HUMAN-BLOCK takes precedence over plain AGENT so a row
		// the agent cannot unblock reads visually different from
		// rows the inbox imperative says to act on. Set by
		// markBlockedByHuman post-Fetch when a dep carries the
		// human label.
		if i.BlockedByHuman {
			return humanBlockBadge.Render("HUMAN-BLOCK")
		}
		return agentBadge.Render("AGENT")
	}
	return ""
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

// detailChromeHeight is the number of lines viewDetail emits
// outside the scrolling body — used by WindowSizeMsg to size the
// viewport correctly. Counts: badge line, blank, title, meta,
// blank, labels (assume present), blank, footer help, plus a one-
// line breathing buffer. Slightly over-counts when labels are
// missing (the viewport just gets one extra line, never less).
const detailChromeHeight = 9

// detailBody composes the scrolling-eligible portion of the
// detail view: the section headings, description, and notes. The
// fixed-chrome portion (badge, title, meta, labels, footer) lives
// in viewDetail directly so the row's identity never scrolls off
// the top.
func detailBody(i beads.Issue) string {
	var b strings.Builder
	b.WriteString(detailLabelStyle.Render("instructions"))
	b.WriteString("\n")
	if strings.TrimSpace(i.Description) == "" {
		b.WriteString(emptyStyle.Render("(no description)"))
	} else {
		b.WriteString(i.Description)
	}
	if strings.TrimSpace(i.Notes) != "" {
		b.WriteString("\n\n")
		b.WriteString(detailLabelStyle.Render("notes"))
		b.WriteString("\n")
		b.WriteString(i.Notes)
	}
	return b.String()
}

func (m Model) viewDetail() string {
	// Prefer the enriched (Detail-fetched) issue if available;
	// otherwise fall back to the slim row from the list. m.detailIssue
	// is set on entry to modeDetail; the Detail Cmd updates it
	// asynchronously with the full record (including notes).
	i := m.detailIssue
	if i.ID == "" {
		if len(m.visible) == 0 {
			return ""
		}
		i = m.visible[m.cursor]
	}

	var b strings.Builder

	// Responsibility badge on its own line above the title — for
	// a `← HUMAN` runbook the badge IS the headline. Empty when
	// the row has no responsibility signal.
	if badge := responsibilityBadgeFor(i); badge != "" {
		b.WriteString(badge)
		b.WriteString("\n\n")
	}

	b.WriteString(detailHeaderStyle.Render(i.Title))
	b.WriteString("\n")

	meta := fmt.Sprintf("%s  %s  %s  P%d",
		idStyle.Render(i.ID),
		statusStyleFor(i.Status).Render(i.Status),
		i.IssueType,
		i.Priority,
	)
	b.WriteString(meta)
	b.WriteString("\n\n")

	if len(i.Labels) > 0 {
		b.WriteString(detailLabelStyle.Render("labels: "))
		b.WriteString(strings.Join(i.Labels, ", "))
		b.WriteString("\n\n")
	}

	// Scrollable body — viewport handles overflow. Re-seed
	// content on every paint so a direct mutation of
	// m.detailIssue (tests, future code paths) stays reflected.
	// viewport.SetContent preserves YOffset, so the user's scroll
	// position survives the refresh.
	m.detailVP.SetContent(detailBody(i))
	b.WriteString(m.detailVP.View())

	// Footer: scroll percent (only when there's actually
	// something to scroll) + key hint.
	b.WriteString("\n")
	footer := "esc / enter: back   j/k ↑↓ scroll   q: quit"
	if m.detailVP.TotalLineCount() > m.detailVP.Height {
		pct := int(m.detailVP.ScrollPercent() * 100)
		footer = fmt.Sprintf("%d%%   %s", pct, footer)
	}
	b.WriteString(helpStyle.Render(footer))
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
	// In-flight refresh indicator. Subtle on purpose — the table
	// stays visible underneath; this just tells the user that
	// hitting r (or switching presets) actually did something
	// while bd's round-trip is in flight.
	if m.refreshing {
		left += "  ↻ refreshing"
	}
	// Render the right-side help inline from the keymap so the
	// status bar's bindings can't drift from the actual handlers.
	// Read-only mode swaps in shortHelpReadOnly so the footer
	// doesn't advertise write keys that just produce a banner.
	bindings := m.keys.ShortHelp()
	suffix := ""
	if m.mutator() == nil {
		bindings = m.keys.shortHelpReadOnly()
		suffix = "  (read-only)"
	}
	helpLine := m.help.ShortHelpView(bindings) + suffix
	gap := " "
	if m.width > 0 {
		need := lipgloss.Width(left) + lipgloss.Width(helpLine) + 2
		if need < m.width {
			gap = strings.Repeat(" ", m.width-need)
		}
	}
	return statusBarStyle.Render(left + gap + helpLine)
}

func keyHit(msg tea.KeyMsg, b key.Binding) bool {
	return key.Matches(msg, b)
}

// renderFetchErrorBanner formats the per-sub Fetch failures into a
// single line. Names are joined with commas; a long list collapses
// to "N repos failed: a, b, c, +M more" so a registry full of
// failing repos doesn't blow out the line. The actionable hint
// ("press r to retry; wyk doctor for details") rides on every
// variant — the truncated case is exactly when retrying is most
// likely the right move. If width > 0 and the formatted message
// still exceeds it (e.g. several repos with long names), trunc
// caps it with an ellipsis so the banner can't wrap. width<=0
// disables the cap (used by tests).
func renderFetchErrorBanner(errs []FetchError, width int) string {
	const showFirst = 3
	const tail = " (press r to retry; wyk doctor for details)"
	n := len(errs)
	names := make([]string, 0, n)
	for _, e := range errs {
		names = append(names, e.Repo)
	}
	var s string
	switch {
	case n == 1:
		s = "1 repo failed to load: " + names[0] + tail
	case n <= showFirst:
		s = fmt.Sprintf("%d repos failed to load: %s%s", n, strings.Join(names, ", "), tail)
	default:
		s = fmt.Sprintf("%d repos failed to load: %s, +%d more%s",
			n, strings.Join(names[:showFirst], ", "), n-showFirst, tail)
	}
	if width > 0 && len(s) > width {
		s = trunc(s, width)
	}
	return s
}

// chromeMinOverhead is the number of non-row lines viewList always
// emits when the table is shown: title, blank, header, blank, status
// bar, plus a one-line breathing-room buffer so the bottom row never
// kisses the status bar. Banners (setupHint, fetch error, status,
// modal prompts) are NOT in this base because they're conditional;
// bodyHeight subtracts them via the m.chromeExtra() count below.
const chromeMinOverhead = 5

// chromeExtra counts the conditional chrome lines that compete with
// rows for vertical real estate. Each banner is one line; modal
// prompts vary. Kept close to viewList so the budget arithmetic
// matches what's actually rendered.
func (m Model) chromeExtra() int {
	n := 0
	if m.setupHint != "" {
		// setupHint can wrap; count newlines + 1.
		n += 1 + strings.Count(m.setupHint, "\n")
	}
	if m.preset != filter.PresetAll || m.priorityCap >= 0 {
		n++ // filter chip strip
	}
	if m.lastErr != nil && len(m.all) > 0 {
		n++ // refresh-failed banner
	}
	if len(m.fetchErrors) > 0 {
		n++ // per-sub fetch-error banner
	}
	if m.status != "" {
		n++ // transient write-feedback banner
	}
	if m.updateNudge != "" {
		n++ // update-available nudge
	}
	switch m.mode {
	case modeFilter, modeNote, modeQuickAdd:
		n += 2 // blank + textinput
	case modeConfirmClose:
		if m.pendingTarget.ID != "" {
			n += 2 // blank + confirm prompt
		}
	}
	return n
}

// bodyHeight is the number of issue rows the viewport will render
// given the current terminal height and chrome state. Floors at 1
// so we always show at least one row (and at least one ↑/↓ hint)
// regardless of how cramped the terminal is. If m.height is zero
// (before the first WindowSizeMsg arrives), fall back to a generous
// default so the initial paint isn't a one-line stub.
func (m Model) bodyHeight() int {
	if m.height <= 0 {
		return 20
	}
	h := m.height - chromeMinOverhead - m.chromeExtra()
	// Reserve a line for each "+N more" hint that may render.
	// Always subtract one — we'd rather under-fill by a row than
	// over-fill and clip the cursor row off the bottom.
	h -= 2
	if h < 1 {
		h = 1
	}
	return h
}

// ensureCursorVisible adjusts m.scroll so m.cursor falls inside the
// rendered window. Called after every cursor mutation (j, k, g, G,
// jump-to-human) and whenever m.visible shrinks or grows. The same
// math runs at render time via bodyHeight, so the two agree.
func (m *Model) ensureCursorVisible() {
	h := m.bodyHeight()
	if h < 1 {
		h = 1
	}
	maxScroll := len(m.visible) - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	} else if m.cursor >= m.scroll+h {
		m.scroll = m.cursor - h + 1
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// renderFilterChips builds the filter-strip line shown above the
// table. Returns the empty string when nothing is filtered (preset
// is the default `all` AND no priority cap) so a fresh view stays
// chrome-free. The non-default preset chip + priority chip both
// use chipStyle for visual coherence.
func renderFilterChips(p filter.Preset, priorityCap int) string {
	var parts []string
	if p != filter.PresetAll {
		parts = append(parts, chipActiveStyle.Render(" "+string(p)+" "))
	}
	if priorityCap >= 0 {
		label := fmt.Sprintf(" ≤P%d ", priorityCap)
		if priorityCap == 0 {
			label = " P0 only "
		}
		parts = append(parts, chipActiveStyle.Render(label))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// emptyMatchCopy returns the preset-aware "no rows match this
// filter" copy. The human preset gets a small celebration since
// "nothing flagged for you" is the goal state; other presets
// describe the absence factually.
func emptyMatchCopy(p filter.Preset, query string) string {
	if query != "" {
		return fmt.Sprintf("no matches for %q", query)
	}
	switch p {
	case filter.PresetHuman:
		return "✓ no human-flagged issues — nothing waiting on you right now"
	case filter.PresetReady:
		return "no ready work — everything left is blocked or in progress"
	case filter.PresetMine:
		return "nothing assigned to you in this workspace"
	case filter.PresetBlocked:
		return "no blocked issues — work is flowing"
	default:
		return "no issues match this view"
	}
}

// firstRunEmptyCopy is shown when bd has no issues at all (fresh
// workspace, no rows ever fetched). Points the user at the most
// likely next action.
func firstRunEmptyCopy() string {
	return "no issues yet — try `wyk handoff -create \"<title>\"` to file your first one, or `bd create \"<title>\"` directly"
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

// trunc caps s at n runes, replacing the trailing rune with `…`
// when truncation actually happens. Rune-aware (not byte-aware) so
// non-ASCII content — issue titles, repo names with diacritics —
// can't be split mid-codepoint. Width semantics throughout the TUI
// (column widths, banner caps) are visual, not byte, so this is
// the right unit. n<=0 returns ""; n==1 returns the first rune
// (no ellipsis, since … would consume the slot).
func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if n == 1 {
		return string(runes[:1])
	}
	return string(runes[:n-1]) + "…"
}
