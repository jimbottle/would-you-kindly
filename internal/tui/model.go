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
	"os"
	"os/exec"
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
	"github.com/jimbottle/would-you-kindly/internal/clipboard"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/filters"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
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
	modeColumns                  // column-visibility overlay (o)
	modeDefer                    // text input for `bd update --defer` value
	modeCommand                  // vim-style `:` command palette
	modeOutput                   // read-only overlay showing captured bd output
	modeAssign                   // text input for `bd update --assignee` value
	modeLabel                    // text input for an arbitrary label to toggle
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
	// implementations ignore it. assignee is the issue's owner —
	// required to be non-empty by the caller (wyk's QuickAdd
	// refuses to dispatch when m.me is empty) so every TUI-filed
	// issue lands with an owner. The new issue is labeled
	// src:human by convention. Returns the new ID.
	Create(ctx context.Context, repo, title, assignee string) (string, error)
	// Reopen sets a closed issue back to status=open. Backs the `u`
	// undo-last-close key — paired with the Model's lastClosed*
	// fields so the user gets a single-deep undo without the TUI
	// having to fetch the closed list to find the row again.
	Reopen(ctx context.Context, issue beads.Issue) error
	// SetDefer hides an issue from `bd ready` until `when`. The
	// when string is passed through verbatim to bd, which owns
	// parsing (+1d / tomorrow / 2026-06-15 / etc.). Empty `when`
	// clears any existing defer.
	SetDefer(ctx context.Context, issue beads.Issue, when string) error
	// SetPriority writes a new priority (0–4, 0 = highest). The
	// caller MUST clamp into range; bd rejects out-of-range
	// values with an error.
	SetPriority(ctx context.Context, issue beads.Issue, priority int) error
	// SetAssignee changes the issue's owner. Empty assignee
	// clears the owner — wyk's create-time owner-required rule
	// is intentionally narrower (only QuickAdd enforces it; a
	// pre-existing issue can be hand-edited back to un-owned via
	// `bd update` and we respect that).
	SetAssignee(ctx context.Context, issue beads.Issue, assignee string) error
	// SetDescription rewrites the issue's description. Multi-line
	// content survives because the underlying bd call uses
	// --description-file rather than a shell-escaped flag. Empty
	// body is honored as a deliberate clear.
	SetDescription(ctx context.Context, issue beads.Issue, body string) error
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

	all     []beads.Issue // last full fetch result
	visible []beads.Issue // after fuzzy filter
	// commonPrefix is the longest shared ID prefix (ending in `-`)
	// across m.all. Recomputed on each fetch; used by displayID to
	// strip noise from the ID column in single-repo mode.
	commonPrefix string
	cursor       int
	width        int
	height       int
	lastErr      error
	lastSync     time.Time
	loading      bool // true between a fetch dispatch and its result

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

	// sortBy is the active sort key for the visible rows. Cycled
	// with s. sortNone preserves bd's native order (the default).
	sortBy sortKey

	// sortDesc reverses the active sort's NATURAL direction
	// (priority's natural is asc, updated's is desc, etc.).
	// Toggled by Shift-S so the user can flip without re-cycling
	// the axis. Always false when sortBy == sortNone (no axis to
	// reverse).
	sortDesc bool

	// showClosed mirrors the BDSource/MultiBDSource IncludeClosed
	// flag so the chip strip + status bar can render it without
	// reaching back through the Source. Toggled by C.
	showClosed bool

	// colsHidden is the per-column visibility map, keyed by the
	// constants in columns.go. Populated from uiconfig at startup
	// (via WithHiddenColumns) and mutated by the `o` overlay; on
	// overlay close the new state is persisted back to disk.
	// nil and empty maps both mean "everything visible" so a
	// first-run user sees the default layout without ceremony.
	colsHidden map[string]bool

	// uiConfigPath is the resolved on-disk path to ui.json so the
	// overlay can persist column-visibility changes without
	// re-resolving XDG every time. Empty disables persistence
	// (used by tests and read-only embeddings).
	uiConfigPath string

	// lastClosed snapshots the most recent close so the `u` undo
	// can reopen it without re-fetching the closed list. Captured
	// from writeMsg{action: "close"} on success and cleared once
	// a reopen lands. Single-deep — vim-style "u" undoes the last
	// move, not a stack.
	lastClosed beads.Issue

	// outputText is the body of the modeOutput overlay (captured
	// stdout/stderr from a `:bd <args>` invocation). Cleared on
	// dismiss so a future open doesn't show stale text.
	outputText string

	// outputVP is the scrollable viewport for the :bd output
	// overlay. Long bd dumps (e.g. `bd list --all` on a busy
	// repo) used to lose the header and footer to terminal
	// scroll; the viewport handles overflow internally with
	// j/k/PgUp/PgDn forwarded from updateOutput.
	outputVP viewport.Model

	// me is the current-user identity, used to count the "mine"
	// slot in the status-bar stats line. Mirrors the Me field on
	// every BDSource (which the filter package uses for PresetMine
	// query construction) so the count and the preset stay
	// consistent. Empty disables the mine slot — no identity, no
	// meaningful count.
	me string

	// fsEvents is the optional channel a watch.Watcher feeds for
	// instant refresh on external bd writes (an external `bd`
	// invocation, another wyk instance, a git pull). Nil keeps the
	// model on the 10s polling fallback only — used by tests and
	// by sources that don't expose a filesystem path. The Watcher
	// itself lives outside the model; main owns its lifecycle.
	fsEvents <-chan struct{}

	// filterAliases is the loaded ~/.config/wyk/filters.json
	// snapshot. When the user types `@name` in the / prompt the
	// model substitutes the saved query before applying the
	// fuzzy filter. Empty map means "no aliases" — the @-syntax
	// stays available but always misses.
	filterAliases filters.Aliases

	// titleMatches stores per-issue rune positions of fuzzy-filter
	// matches inside the Title cell, keyed by Issue.ID. Populated
	// by recomputeVisible when m.query != ""; consulted by
	// renderRow to style the matched runes. Empty/nil disables
	// highlighting (no filter active, or no match landed in the
	// title field).
	titleMatches map[string][]int

	// marked is the multi-select set, keyed by Issue.ID. Toggled
	// by `v`. When non-empty, bulk-capable write keys (c/H/d)
	// operate on every marked row instead of the cursor row. The
	// row prefix in renderRow surfaces a ✓ for marked entries so
	// the selection is visible. esc in modeList clears the set.
	marked map[string]bool

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
	// lipgloss.Style is a value type; plain assignment is the copy.
	h.Styles.ShortSeparator = helpStyle

	return Model{
		src:         src,
		keys:        defaultKeyMap(),
		mode:        modeList,
		preset:      filter.PresetAll,
		input:       ti,
		loading:     true, // first paint shows "loading…" until Init's fetch returns
		detailVP:    viewport.New(80, 20),
		outputVP:    viewport.New(80, 20),
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

// WithHiddenColumns returns a copy of the model with the column-
// visibility map and persistence path set. main wires this from
// uiconfig.Load on startup; tests can pass an empty path to keep
// toggles in-memory only.
func (m Model) WithHiddenColumns(hidden map[string]bool, persistPath string) Model {
	if hidden == nil {
		hidden = map[string]bool{}
	}
	m.colsHidden = hidden
	m.uiConfigPath = persistPath
	return m
}

// WithMe returns a copy of the model with the current-user
// identity set so the status-bar stats line can compute the
// "mine" count. main wires this from the same value passed to
// every BDSource.Me, keeping the count and PresetMine query in
// sync. Empty leaves the stats line without a mine slot.
func (m Model) WithMe(me string) Model {
	m.me = me
	return m
}

// WithFSEvents returns a copy of the model wired to a watcher's
// event channel. Each receive on `events` triggers an immediate
// refresh — the 10s polling timer stays in place as a fallback
// for platforms where fsnotify isn't supported. Nil disables the
// fast path (used by tests and probe runs).
func (m Model) WithFSEvents(events <-chan struct{}) Model {
	m.fsEvents = events
	return m
}

// WithFilterAliases returns a copy of the model with the loaded
// filter aliases. main wires this from filters.Load at startup so
// `@name` in the / prompt expands to the saved query.
func (m Model) WithFilterAliases(a filters.Aliases) Model {
	m.filterAliases = a
	return m
}

// Init triggers the first fetch and starts the refresh tick. Also
// kicks the spinner so the loading indicator animates from frame 0.
// When fsEvents is wired, also primes the fs-watch loop so external
// bd writes refresh the list instantly.
func (m Model) Init() tea.Cmd {
	// gen 0 is implicit on the zero-valued Model; the matching tick
	// message carries gen 0 too, so the chain starts coherently.
	cmds := []tea.Cmd{m.fetchCmd(), tickCmd(m.tickGen), m.spinner.Tick}
	if m.fsEvents != nil {
		cmds = append(cmds, waitFSEvent(m.fsEvents))
	}
	return tea.Batch(cmds...)
}

// fsEventMsg lands when the watcher reports a debounced bd-write.
// The Update handler refetches and re-arms the wait. Carries no
// payload — "something changed in .beads" is the only signal we
// act on.
type fsEventMsg struct{}

// waitFSEvent is the requeue pattern: block on the channel, emit
// fsEventMsg when it fires, let Update re-arm the wait. Closes
// the loop cleanly when the channel closes (watcher shut down).
func waitFSEvent(events <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-events
		if !ok {
			return nil
		}
		return fsEventMsg{}
	}
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

// sortKey identifies the active client-side sort for the visible
// rows. sortNone preserves bd's native order (the default).
type sortKey int

const (
	sortNone sortKey = iota
	sortPriority
	sortUpdated
	sortRepo
	sortID
)

// next returns the next sort key in the cycle so `s` rotates
// through {none, priority, updated, repo, id, none, ...}.
func (k sortKey) next() sortKey {
	if k == sortID {
		return sortNone
	}
	return k + 1
}

// label returns the human-readable name used as the chip strip
// text (the header arrow is applied separately by sortDecorate
// against the column's own caption).
func (k sortKey) label() string {
	switch k {
	case sortPriority:
		return "priority"
	case sortUpdated:
		return "updated"
	case sortRepo:
		return "repo"
	case sortID:
		return "id"
	default:
		return ""
	}
}

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
		// The :bd output overlay uses the same chrome budget as
		// the detail view (header + footer line); a separate
		// chrome const would be over-engineering for a single
		// extra row.
		m.outputVP.Width = msg.Width
		m.outputVP.Height = bodyH
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

	case fsEventMsg:
		// External bd write — refetch immediately AND re-arm the
		// watcher wait so the next event still arrives. We don't
		// bump tickGen; the 10s timer keeps running as fallback.
		// Terminal-error suspension still applies: no point
		// refetching when there's no source to query.
		if isTerminalErr(m.lastErr) {
			return m, waitFSEvent(m.fsEvents)
		}
		return m, tea.Batch(m.fetchCmd(), waitFSEvent(m.fsEvents))

	case writeMsg:
		return m.handleWriteResult(msg)

	case editFinishedMsg:
		return m.handleEditFinished(msg)

	case bulkWriteMsg:
		return m.handleBulkWriteResult(msg)

	case rawBDMsg:
		// Compose the overlay body: a header naming the command,
		// then stdout, then the error string if bd exited non-
		// zero. We don't try to separate stdout/stderr — bd's own
		// stderr is folded into the returned error.
		var b strings.Builder
		b.WriteString("$ bd ")
		b.WriteString(msg.args)
		b.WriteString("\n\n")
		if len(msg.out) > 0 {
			b.Write(msg.out)
			if msg.out[len(msg.out)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
		if msg.err != nil {
			b.WriteString("\n[error] ")
			b.WriteString(msg.err.Error())
			b.WriteByte('\n')
		}
		m.outputText = b.String()
		// Only open the overlay if the user is still on the list
		// (or in the palette finishing the command). A slow `:bd`
		// completing while the user has navigated into the
		// detail view, help, or another modal would otherwise
		// yank them back to the bd output unexpectedly. There's
		// no UI today for re-surfacing a stashed output from
		// another mode, so be honest in the banner: the result is
		// discarded and the user needs to re-run.
		if m.mode == modeList || m.mode == modeCommand {
			m.outputVP.SetContent(m.outputText)
			m.outputVP.GotoTop()
			m.mode = modeOutput
			return m, nil
		}
		m.outputText = "" // clear so a stale body doesn't leak through future paths
		m.setStatus("bd output discarded — you navigated away mid-run; re-run :bd from the list to view")
		return m, flashClearCmd(m.statusGen)

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

	case tea.MouseMsg:
		// Mouse routes by mode:
		// - modeList: wheel scrolls the cursor; left-click sets it
		// - modeDetail: wheel scrolls the viewport body so a long
		//   description doesn't force the user to hunt for j/k
		// - modeOutput: wheel scrolls the :bd output viewport so
		//   the overlay's footer hint matches behaviour
		// - other modes (help, modals, prompts): keyboard-focused,
		//   mouse is dropped
		switch m.mode {
		case modeList:
			return m.handleMouse(msg)
		case modeDetail:
			var cmd tea.Cmd
			m.detailVP, cmd = m.detailVP.Update(msg)
			return m, cmd
		case modeOutput:
			var cmd tea.Cmd
			m.outputVP, cmd = m.outputVP.Update(msg)
			return m, cmd
		default:
			return m, nil
		}

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
		case modeColumns:
			return m.updateColumns(msg)
		case modeDefer:
			return m.updateDefer(msg)
		case modeCommand:
			return m.updateCommand(msg)
		case modeOutput:
			return m.updateOutput(msg)
		case modeAssign:
			return m.updateAssign(msg)
		case modeLabel:
			return m.updateLabel(msg)
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
	case keyHit(msg, m.keys.Back):
		// esc in modeList clears the multi-select. Without a
		// dedicated escape, the only way to drop a botched mark
		// set would be `v` on each row — too punishing. Other esc
		// uses (cancel prompt, return from detail) live in their
		// own mode handlers and don't reach here.
		if len(m.marked) > 0 {
			m.marked = nil
			m.setStatus("cleared marks")
			return m, flashClearCmd(m.statusGen)
		}
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
	case keyHit(msg, m.keys.SortCycle):
		return m.setSortKey(m.sortBy.next())
	case keyHit(msg, m.keys.SortReverse):
		return m.reverseSort()
	case keyHit(msg, m.keys.Command):
		return m.beginCommand()
	case keyHit(msg, m.keys.PriorityUp):
		return m.bumpPriority(-1)
	case keyHit(msg, m.keys.PriorityDown):
		return m.bumpPriority(+1)
	case keyHit(msg, m.keys.AssignOwner):
		return m.beginAssign()
	case keyHit(msg, m.keys.Label):
		return m.beginLabel()
	case keyHit(msg, m.keys.Editor):
		return m.beginEdit()
	case keyHit(msg, m.keys.ShowClosed):
		return m.toggleShowClosed()
	case keyHit(msg, m.keys.Columns):
		m.mode = modeColumns
		return m, nil
	case keyHit(msg, m.keys.Yank):
		return m.handleYank()
	case keyHit(msg, m.keys.Undo):
		return m.handleUndo()
	case keyHit(msg, m.keys.Defer):
		return m.beginDefer()
	case keyHit(msg, m.keys.Mark):
		return m.toggleMark()
	case keyHit(msg, m.keys.Refresh):
		return m.manualRefresh()

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

// handleMouse interprets a tea.MouseMsg against the list view:
// wheel up/down moves the cursor (like k/j); left-click lands the
// cursor on the targeted row. Out-of-bounds clicks (header, chip
// strip, banners) are silently ignored.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// MouseAction is only meaningful for press/release on a button.
	// We only act on the press; releasing a button or a wheel-tick
	// is enough motion to know what the user wanted.
	if msg.Action == tea.MouseActionRelease {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.cursor > 0 {
			m.cursor--
			m.ensureCursorVisible()
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			m.ensureCursorVisible()
		}
		return m, nil
	case tea.MouseButtonLeft:
		// Compute the cell-row offset from the top of the table
		// body, then translate to a m.visible index via the
		// current scroll offset. Clicks above the body
		// (title/setupHint/chip/header) or past the last rendered
		// row produce target out-of-range → no-op. We also clamp
		// to the actual rendered window height so a click on the
		// "↑ N more above"/"↓ N more below" hint lines (which sit
		// just past the row window) doesn't get mapped to the
		// next row out-of-window — that produced a surprising
		// downward jump.
		rowY := msg.Y - m.rowsStartY()
		if rowY < 0 {
			return m, nil
		}
		visibleRows := len(m.visible) - m.scroll
		if h := m.bodyHeight(); h > 0 && visibleRows > h {
			visibleRows = h
		}
		if rowY >= visibleRows {
			return m, nil
		}
		target := m.scroll + rowY
		if target < 0 || target >= len(m.visible) {
			return m, nil
		}
		m.cursor = target
		m.ensureCursorVisible()
		return m, nil
	}
	return m, nil
}

// rowsStartY returns the Y-coordinate (zero-indexed from the top
// of the rendered output) at which the first table row lands.
// Mirrors the viewList chrome ordering — bumping any conditional
// chrome there means bumping it here too. We use this to map a
// click's msg.Y back to a row index.
func (m Model) rowsStartY() int {
	y := 1 // title line
	if m.setupHint != "" {
		// setupHint can wrap; count newlines + 1 to match the
		// vertical real estate it actually consumes.
		y += 1 + strings.Count(m.setupHint, "\n")
	}
	if m.preset != filter.PresetAll || m.priorityCap >= 0 || m.sortBy != sortNone || m.showClosed {
		y++ // chip strip
	}
	y++ // blank line between header chrome and table header
	y++ // table header
	return y
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
		idx := ((m.cursor+dir*offset)%n + n) % n
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
// banner); `id` identifies the affected issue. `issue` snapshots
// the full row (filled by close so the undo-handler has the Repo
// to route reopen back through MultiBDSource without re-fetching
// the closed list).
type writeMsg struct {
	action string
	id     string
	issue  beads.Issue
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
	// Bulk path: marks are the targets and the confirm prompt
	// counts them. Single path: snapshot the cursor row into
	// pendingTarget as before.
	if len(m.marked) == 0 {
		m.pendingTarget = m.visible[m.cursor]
	}
	// Modal entry adds 2 lines of chrome — re-clamp scroll so the
	// cursor stays in the now-smaller viewport.
	m.ensureCursorVisible()
	return m, nil
}

func (m Model) updateConfirmClose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	bulk := len(m.marked) > 0
	target := m.pendingTarget
	m.pendingTarget = beads.Issue{}
	if msg.String() == "y" || msg.String() == "Y" {
		mu := m.mutator()
		m.mode = modeList
		if bulk {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("close", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.Close(ctx, i)
			})
		}
		if !m.issueExists(target.ID) {
			m.status = "close cancelled: " + target.ID + " was removed from the workspace by a refresh"
			return m, nil
		}
		return m, runWriteWithIssue("close", target, func(ctx context.Context) error {
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
// Bulk path: when marks are present, ADDS the human label to every
// marked row that doesn't already have it (the most common triage
// flow — "flag these five for review"). Toggle-per-row would be
// inconsistent across mixed-state selections.
func (m Model) toggleHuman() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.status = "read-only mode (no Mutator wired up)"
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	mu := m.mutator()
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("flag", targets, func(ctx context.Context, i beads.Issue) error {
			if i.IsHuman() {
				return nil // already flagged; bulk is add-only
			}
			return mu.AddLabel(ctx, i, "human")
		})
	}
	i := m.visible[m.cursor]
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
		// Refuse to file an orphan task. wyk's working assumption is
		// every issue has an owner; the way to enforce that without
		// putting up another prompt is to require the launcher pass
		// -me (or have a defaultMe() result). The status banner
		// names the fix so a user surprised by the refusal knows
		// what to do.
		if m.me == "" {
			m.status = "quick-add cancelled: no owner. Re-launch with -me=you@example.com (or set git user.email / $USER)"
			return m, nil
		}
		assignee := m.me
		return m, runQuickAdd(func(ctx context.Context) (string, error) {
			return mu.Create(ctx, repo, title, assignee)
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

// handleBulkWriteResult formats a status banner from a bulk
// dispatch. Total success → "closed/flagged/deferred N rows";
// partial failure → "<verb> K of N (M failed: <first failure>)";
// total failure → "<action> failed for all N rows (<first
// failure>)". On any failure, marks are restored for the failed
// rows so the user can retry without re-marking (dispatch sites
// optimistically clear m.marked; this is the rollback). Refetches
// after a non-total-failure outcome so the new state is visible.
func (m Model) handleBulkWriteResult(msg bulkWriteMsg) (tea.Model, tea.Cmd) {
	succeeded := msg.total - len(msg.failed)
	verb := bulkVerbs[msg.action]
	if verb == "" {
		verb = msg.action
	}
	if len(msg.failed) > 0 {
		if m.marked == nil {
			m.marked = make(map[string]bool, len(msg.failed))
		}
		for _, t := range msg.failed {
			m.marked[issueKey(t)] = true
		}
	}
	switch {
	case len(msg.failed) == 0:
		m.setStatus(fmt.Sprintf("%s %d rows", verb, succeeded))
	case succeeded == 0:
		m.setStatus(fmt.Sprintf("%s failed for all %d rows (%s)", msg.action, msg.total, msg.errs[0]))
		return m, nil // sticky banner on total failure
	default:
		m.setStatus(fmt.Sprintf("%s %d of %d (%d failed: %s)", verb, succeeded, msg.total, len(msg.failed), msg.errs[0]))
	}
	return m, tea.Batch(m.fetchCmd(), flashClearCmd(m.statusGen))
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

// runWriteWithIssue is runWrite that also threads the full Issue
// through to the result. Used by close so handleWriteResult can
// snapshot the row into m.lastClosed for `u` undo without forcing
// every other write path to plumb the issue through.
func runWriteWithIssue(action string, issue beads.Issue, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		err := fn(context.Background())
		return writeMsg{action: action, id: issue.ID, issue: issue, err: err}
	}
}

// runBulkWrite fires fn against each target sequentially and
// reports a single bulkWriteMsg with success/failure detail. We
// run sequentially (not in parallel) so the bd subprocess load
// stays the same as the single-target path — the multi-repo
// HUMAN-BLOCK semaphore already caps fanout, but parallel bulk
// closes would still spike subprocess count and risk reordering
// audit events. The dispatch is O(N) requests but N is the size
// of a user's triage selection (rarely >20), so the latency is
// acceptable.
func runBulkWrite(action string, targets []beads.Issue, fn func(ctx context.Context, i beads.Issue) error) tea.Cmd {
	return func() tea.Msg {
		var failed []beads.Issue
		var errs []string
		for _, t := range targets {
			if err := fn(context.Background(), t); err != nil {
				failed = append(failed, t)
				errs = append(errs, fmt.Sprintf("%s: %v", t.ID, err))
			}
		}
		return bulkWriteMsg{action: action, total: len(targets), failed: failed, errs: errs}
	}
}

// bulkWriteMsg carries the result of a runBulkWrite back to the
// model. action is what was attempted (close/flag/defer); total is
// the batch size; failed lists the issues that errored (parallel
// to errs which holds per-target error strings). Carrying the full
// issues — not just IDs — lets handleBulkWriteResult restore marks
// for failed rows so the user can retry without re-marking.
type bulkWriteMsg struct {
	action string
	total  int
	failed []beads.Issue
	errs   []string
}

// bulkVerbs maps each bulk-capable action to its past-tense form
// for the status banner. A naive `action + "ed"` produced
// "closeed" and "defered"; this explicit map matches what
// handleWriteResult uses for the single-target path.
var bulkVerbs = map[string]string{
	"close":    "closed",
	"flag":     "flagged",
	"defer":    "deferred",
	"priority": "reprioritized",
	"assign":   "reassigned",
	"label":    "labeled",
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
		// Snapshot the row so `u` can reopen it without re-fetching
		// the closed list. Cleared on reopen success (or on a
		// second close, which overwrites this one).
		m.lastClosed = msg.issue
	case "reopen":
		m.setStatus("reopened " + msg.id)
		m.lastClosed = beads.Issue{} // consumed
	case "defer":
		m.setStatus("deferred " + msg.id)
	case "assign":
		m.setStatus("reassigned " + msg.id)
	case "edit":
		m.setStatus("edited " + msg.id)
	case "flag":
		m.setStatus("flagged " + msg.id + " for human")
	case "unflag":
		m.setStatus("unflagged " + msg.id)
	case "note":
		m.setStatus("noted " + msg.id)
	case "create":
		m.setStatus("created " + msg.id)
	default:
		// Compound actions like "label:foo" / "unlabel:foo" carry
		// the label name in the action string itself so the
		// status banner can read "labeled foo a-1" instead of a
		// generic "label a-1". Plain actions (no `:`) fall
		// through to the action-then-id format.
		if name, label, ok := strings.Cut(msg.action, ":"); ok {
			switch name {
			case "label":
				m.setStatus("labeled " + msg.id + " " + label)
			case "unlabel":
				m.setStatus("removed " + label + " from " + msg.id)
			default:
				m.setStatus(msg.action + " " + msg.id)
			}
		} else {
			m.setStatus(msg.action + " " + msg.id)
		}
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

// updateColumns handles input in the column-visibility overlay.
// Numbers 1-N toggle columns in the toggleableColumns registry
// order; multi-only columns silently no-op while single-repo. esc
// or `o` closes the overlay and persists the new state if a
// uiConfigPath is set. Persistence failure is surfaced as a status
// banner but doesn't block closing — the toggle is still in effect
// for the current session, only the next launch loses it.
func (m Model) updateColumns(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		// Persist before quitting so toggles made in the overlay
		// aren't silently lost when the user closes via ctrl+c
		// instead of esc. Best-effort; we're exiting anyway, so a
		// save error has nowhere useful to surface.
		_ = m.persistColumns()
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc", "o", "q":
		if err := m.persistColumns(); err != nil {
			m.status = "ui.json save failed: " + err.Error()
		}
		m.mode = modeList
		return m, nil
	}
	// Digit toggles. Build the index from the rune so non-digit
	// keystrokes inside the overlay (typing junk by accident) are
	// ignored rather than triggering an out-of-range slot.
	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < len(toggleableColumns) {
				col := toggleableColumns[idx]
				if col.MultiOnly && !m.isMultiRepo() {
					return m, nil // overlay shows the note; treat as no-op
				}
				if m.colsHidden == nil {
					m.colsHidden = map[string]bool{}
				}
				m.colsHidden[col.ID] = !m.colsHidden[col.ID]
			}
		}
	}
	return m, nil
}

// persistColumns serialises m.colsHidden back to ui.json via the
// uiconfig package. Returns nil (best-effort) when no path is set
// — tests and embedded uses can run without a real config file.
func (m Model) persistColumns() error {
	if m.uiConfigPath == "" {
		return nil
	}
	hidden := make([]string, 0, len(m.colsHidden))
	// Walk toggleableColumns rather than ranging the map so the
	// on-disk list order matches the overlay order — easier to
	// hand-edit when a user opens ui.json directly.
	for _, c := range toggleableColumns {
		if m.colsHidden[c.ID] {
			hidden = append(hidden, c.ID)
		}
	}
	return uiconfig.Save(m.uiConfigPath, uiconfig.Config{
		Version:       uiconfig.CurrentVersion,
		HiddenColumns: hidden,
	})
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
		// Trim once so the lookup key, the applied query, and the
		// status banner all agree — a stray trailing space on
		// "@nope " used to keep the raw value as the literal
		// query while the banner reported the trimmed form.
		raw := strings.TrimSpace(m.input.Value())
		// Alias expansion: a value of `@name` swaps the query
		// for the saved alias before applying. A miss keeps the
		// raw `@name` as a literal fuzzy query (so a row with
		// `@name` in its title still matches) and surfaces a
		// status banner so the user knows the alias didn't
		// resolve. Multi-word values starting with `@` (e.g.
		// "@blocked something") are not expanded — keeps the
		// rule narrow and predictable.
		if q, ok := m.filterAliases.Lookup(raw); ok {
			m.query = q
		} else {
			m.query = raw
			if strings.HasPrefix(raw, "@") {
				m.setStatus("no filter alias for " + raw)
			}
		}
		m.mode = modeList
		m.input.Blur()
		m.recomputeVisible()
		m.ensureCursorVisible()
		if m.status != "" {
			return m, flashClearCmd(m.statusGen)
		}
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
		// No fuzzy filter: clone so the sort below doesn't reorder
		// m.all, which the rest of the model expects to be in
		// bd's native order.
		out := make([]beads.Issue, len(pool))
		copy(out, pool)
		applySort(out, m.sortBy, m.sortDesc)
		m.visible = out
		m.titleMatches = nil // no filter → no highlight
		if m.cursor >= len(m.visible) {
			m.cursor = max(0, len(m.visible)-1)
		}
		return
	}

	best := make(map[int]int, len(pool))
	m.titleMatches = make(map[string][]int, len(pool))
	for _, mt := range fuzzy.FindFrom(m.query, titleSource(pool)) {
		best[mt.Index] = mt.Score
		// Capture rune-index positions so renderRow can style
		// each matched rune. fuzzy.MatchedIndexes are byte
		// offsets into the source string; convert here once so
		// renderRow stays a fast formatter. Key by issueKey (not
		// bare ID) so two cross-repo issues with colliding IDs
		// don't overwrite each other's match indices.
		m.titleMatches[issueKey(pool[mt.Index])] = byteToRuneIdxs(pool[mt.Index].Title, mt.MatchedIndexes)
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
	// Sort overrides the fuzzy-score ordering when set — the user
	// asked for a specific axis, honour it.
	applySort(out, m.sortBy, m.sortDesc)
	m.visible = out

	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// applySort orders the issue slice in place per the chosen sort
// key. sortNone is a no-op so callers can pass through without
// branching. Priority ASC (P0 first); updated DESC (newest first);
// repo / id ASC (alphabetical).
func applySort(issues []beads.Issue, k sortKey, reverse bool) {
	// Each axis declares its NATURAL direction (priority asc =
	// P0 first; updated desc = newest first). Reverse flips the
	// less-func so a single bool drives every axis the same way.
	var less func(i, j int) bool
	switch k {
	case sortPriority:
		less = func(i, j int) bool { return issues[i].Priority < issues[j].Priority }
	case sortUpdated:
		less = func(i, j int) bool { return issues[i].UpdatedAt.After(issues[j].UpdatedAt) }
	case sortRepo:
		less = func(i, j int) bool { return issues[i].Repo < issues[j].Repo }
	case sortID:
		less = func(i, j int) bool { return issues[i].ID < issues[j].ID }
	default:
		return
	}
	if reverse {
		base := less
		less = func(i, j int) bool { return base(j, i) }
	}
	sort.SliceStable(issues, less)
}

// setPriorityCap updates the priority filter and re-runs the
// visible-row pipeline. Cursor resets to 0 since the previous
// position is meaningless against a different filter; scroll
// re-clamps so the (now smaller or larger) list doesn't leave the
// cursor offscreen. Param named capLevel to avoid shadowing Go's
// builtin cap() — protects future edits that might add a
// slice-capacity check inside the function.
func (m Model) setPriorityCap(capLevel int) (tea.Model, tea.Cmd) {
	m.priorityCap = capLevel
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
}

// setSortKey rotates the active sort and re-runs the visible-row
// pipeline. Cursor resets to 0 because the user's previous
// position has no meaning against a re-ordered list. Direction
// resets to natural (sortDesc=false) on axis change — preserving
// the reverse across an axis switch would carry an "unexpected
// direction" surprise into the next sort.
func (m Model) setSortKey(k sortKey) (tea.Model, tea.Cmd) {
	m.sortBy = k
	m.sortDesc = false
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
}

// reverseSort flips m.sortDesc and re-runs the visible-row
// pipeline. No-op when no axis is active — sortNone has no
// direction to reverse, and a status banner is more useful than
// a silent no-press.
func (m Model) reverseSort() (tea.Model, tea.Cmd) {
	if m.sortBy == sortNone {
		m.setStatus("S: pick a sort first (press s)")
		return m, flashClearCmd(m.statusGen)
	}
	m.sortDesc = !m.sortDesc
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
}

// handleYank copies the cursor issue's full ID to the system
// clipboard via OSC 52 and surfaces a status banner. The full ID
// (not the display-prefix-trimmed version) is what's useful for
// pasting into bd commands or chat — partial IDs would just
// silently fail elsewhere. Empty-list / past-end cursor states
// produce no-ops with a clear status, never a silent failure.
func (m Model) handleYank() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	id := m.visible[m.cursor].ID
	if err := clipboardCopy(id); err != nil {
		m.setStatus("yank failed: " + err.Error())
		// No auto-clear on failure — the user needs to see why.
		return m, nil
	}
	m.setStatus("copied " + id)
	return m, flashClearCmd(m.statusGen)
}

// clipboardCopy is the seam tests can swap to skip /dev/tty I/O.
// Production points at the real OSC 52 emitter.
var clipboardCopy = clipboard.Copy

// handleUndo reopens the most-recently-closed issue captured by
// handleWriteResult. Empty m.lastClosed.ID means "nothing to undo"
// — a friendly status banner is more useful than silently doing
// nothing. Read-only sources surface the same "read-only" hint
// the rest of the write keys use.
func (m Model) handleUndo() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if m.lastClosed.ID == "" {
		m.setStatus("nothing to undo")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.lastClosed
	return m, runWriteWithIssue("reopen", target, func(ctx context.Context) error {
		return mu.Reopen(ctx, target)
	})
}

// issueKey is the composite key the model uses to address an
// Issue in the marked / titleMatches maps. Bare Issue.ID can
// collide in multi-repo mode (two workspaces using the same ID
// scheme), so we prefix with Repo when set. Single-repo mode has
// Repo=="" and falls back to plain ID — preserving the existing
// behaviour where there's no collision to disambiguate.
func issueKey(i beads.Issue) string {
	if i.Repo == "" {
		return i.ID
	}
	return i.Repo + "/" + i.ID
}

// toggleMark flips the multi-select state on the cursor row.
// First mark allocates m.marked lazily; removing the last mark
// drops the map back to nil so len(m.marked)>0 stays the
// single source of truth for "selection active". Status banner
// surfaces the current count so the user always knows what
// bulk-c/H/d would act on.
func (m Model) toggleMark() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	key := issueKey(m.visible[m.cursor])
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	if m.marked[key] {
		delete(m.marked, key)
	} else {
		m.marked[key] = true
	}
	if len(m.marked) == 0 {
		m.marked = nil
		m.setStatus("no marks")
	} else {
		m.setStatus(fmt.Sprintf("%d marked", len(m.marked)))
	}
	return m, flashClearCmd(m.statusGen)
}

// markedIssues returns every row in m.all whose composite key is
// in m.marked. We scan m.all rather than m.visible so a fuzzy
// filter that hides part of the selection doesn't silently drop
// rows from a bulk dispatch — marks survive filter changes by
// design, and "close 5 rows" should mean five even if only three
// are on screen. Stable ordering follows m.all (bd's native
// order, mirrored by the visible list when no sort is active).
func (m Model) markedIssues() []beads.Issue {
	if len(m.marked) == 0 {
		return nil
	}
	out := make([]beads.Issue, 0, len(m.marked))
	for _, i := range m.all {
		if m.marked[issueKey(i)] {
			out = append(out, i)
		}
	}
	return out
}

// bumpPriority nudges the cursor row's (or every marked row's)
// priority by `delta` steps and dispatches the writes. delta == -1
// means "more urgent" (priority--), +1 means "less urgent"
// (priority++). bd's range is 0–4; results are clamped, and a
// no-op (already at the edge) silently passes — bd's update
// command is idempotent on a no-change so re-writing the same
// priority is harmless.
func (m Model) bumpPriority(delta int) (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		// "priority" is the bulkVerbs key; "reprioritized N rows"
		// reads correctly in the banner. The prior copy-paste
		// from the close/flag/defer handlers used "flag", which
		// produced "flagged N rows" for a priority change.
		return m, runBulkWrite("priority", targets, func(ctx context.Context, i beads.Issue) error {
			return mu.SetPriority(ctx, i, clampPriority(i.Priority+delta))
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	i := m.visible[m.cursor]
	newP := clampPriority(i.Priority + delta)
	if newP == i.Priority {
		m.setStatus(fmt.Sprintf("%s already at P%d", i.ID, i.Priority))
		return m, flashClearCmd(m.statusGen)
	}
	return m, runWrite(fmt.Sprintf("set P%d", newP), i.ID, func(ctx context.Context) error {
		return mu.SetPriority(ctx, i, newP)
	})
}

// clampPriority keeps priority inside bd's 0–4 range. Values
// outside that range get rejected by bd; clamping silently turns
// the no-op edge case (press + on a P0 row) into a tolerable
// "stay put" instead of a spurious error banner.
func clampPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 4 {
		return 4
	}
	return p
}

// manualRefresh is the shared body of the `r` key and the
// `:refresh` command. Triggers a fetch and, if a prior fetch
// landed us in a terminal-error state (so the auto-tick chain
// suspended itself), restarts the tick with a fresh generation
// so the old in-flight tick — if any — gets retired by the
// generation check in Update's tickMsg handler.
//
// We do NOT set m.loading here: the existing rows stay on screen
// while the refresh runs in the background, and a small ↻ glyph
// appears in the status bar (see statusBar). Replacing the table
// with "loading…" on every keypress produced a jarring
// full-canvas blank.
func (m Model) manualRefresh() (tea.Model, tea.Cmd) {
	m.refreshing = true
	cmds := []tea.Cmd{m.fetchCmd()}
	if isTerminalErr(m.lastErr) {
		m.tickGen++
		cmds = append(cmds, tickCmd(m.tickGen))
	}
	return m, tea.Batch(cmds...)
}

// beginCommand opens the `:` command-palette prompt. Empty
// submission cancels; otherwise updateCommand dispatches through
// commandTable.
func (m Model) beginCommand() (tea.Model, tea.Cmd) {
	m.mode = modeCommand
	m.input.SetValue("")
	m.input.Prompt = ":"
	m.input.Placeholder = "refresh / preset <name> / sort <axis> / reverse / filter save <name>"
	m.input.Focus()
	return m, textinput.Blink
}

// updateCommand drives the `:` prompt. esc cancels; enter parses
// the value into a command + args and dispatches through
// commandTable. Unknown commands surface a status banner that
// names the known set so the user can recover.
func (m Model) updateCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		raw := strings.TrimSpace(m.input.Value())
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if raw == "" {
			return m, nil
		}
		return m.dispatchCommand(raw)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// dispatchCommand splits the input into name + remaining args
// and routes to the matching handler. Unknown commands set a
// status banner with the supported list. The list is small
// enough that a flat switch is more readable than a registry
// pattern.
func (m Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	name, rest, _ := strings.Cut(raw, " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "refresh":
		return m.manualRefresh()
	case "preset":
		p := filter.Preset(rest)
		// Reject unknown presets early — silently switching to a
		// no-op preset would just confuse the user about why the
		// list didn't change.
		known := false
		for _, q := range filter.AllPresets() {
			if q == p {
				known = true
				break
			}
		}
		if !known {
			m.setStatus(":preset: unknown name " + fmt.Sprintf("%q", rest))
			return m, flashClearCmd(m.statusGen)
		}
		return m.switchPreset(p)
	case "sort":
		// Bare `:sort` lands here with rest == "" — treat as a
		// usage error so the user sees the expected axes
		// instead of silently switching to no-sort. The explicit
		// way to clear is `:sort none`.
		if rest == "" {
			m.setStatus(":sort: axis required (one of none, priority, updated, repo, id)")
			return m, flashClearCmd(m.statusGen)
		}
		k, ok := parseSortKey(rest)
		if !ok {
			m.setStatus(":sort: unknown axis. Try one of none, priority, updated, repo, id")
			return m, flashClearCmd(m.statusGen)
		}
		return m.setSortKey(k)
	case "reverse":
		return m.reverseSort()
	case "filter":
		return m.dispatchFilterCommand(rest)
	case "bd":
		return m.runRawBD(rest)
	case "help":
		return m.openHelp()
	default:
		m.setStatus(":" + name + ": unknown command. Known: refresh, preset, sort, reverse, filter save <name>, bd <args>, help")
		return m, flashClearCmd(m.statusGen)
	}
}

// runRawBD shells out a `bd <args>` invocation in the cursor
// row's workspace and switches to modeOutput to show stdout. If
// the source doesn't implement rawBDInvoker (e.g. a test stub),
// surface a status banner so the user knows the command isn't
// available. Empty args is a usage error — bare `:bd` would
// surface bd's own usage anyway, but we save the round-trip.
func (m Model) runRawBD(rest string) (tea.Model, tea.Cmd) {
	if rest == "" {
		m.setStatus(":bd: args required (try :bd ready, :bd show <id>, …)")
		return m, flashClearCmd(m.statusGen)
	}
	raw, ok := m.src.(rawBDInvoker)
	if !ok {
		m.setStatus(":bd: this source doesn't support raw invocations")
		return m, flashClearCmd(m.statusGen)
	}
	// Pick the cursor row's repo so the bd subprocess lands in the
	// right workspace; empty (no rows / out-of-range cursor)
	// falls back to whatever the source picks (first sub in
	// multi, the single client in single).
	repo := ""
	if len(m.visible) > 0 && m.cursor >= 0 && m.cursor < len(m.visible) {
		repo = m.visible[m.cursor].Repo
	}
	args := shellFields(rest)
	return m, func() tea.Msg {
		out, err := raw.RawBD(context.Background(), repo, args)
		return rawBDMsg{args: rest, out: out, err: err}
	}
}

// shellFields splits s into args, honoring "..." and '...' quoting
// so `:bd query "p0"` reaches bd as ["query", "p0"] instead of
// ["query", "\"p0\""]. Doesn't handle escapes (\"), backticks, or
// $() — wyk is a TUI launcher for bd commands, not a shell, and
// the simpler grammar is easier to reason about. Mixed quoting
// inside a single token (e.g. foo"bar") is preserved as-is.
//
// An explicitly-empty quoted argument is preserved: `--desc ""`
// emits ["--desc", ""] so a user can clear a bd field that
// accepts an empty value. Without the `started` flag this would
// silently drop the empty token (no runes written).
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inDouble, inSingle := false, false
	started := false // true once the current token has any content (including an empty quoted span)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case (r == ' ' || r == '\t') && !inDouble && !inSingle:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

// rawBDInvoker is the optional capability the `:bd <args>`
// command needs — wired by BDSource and MultiBDSource. The model
// type-asserts at the call site so a read-only or test source
// can still load.
type rawBDInvoker interface {
	RawBD(ctx context.Context, repo string, args []string) ([]byte, error)
}

// rawBDMsg carries the result of a `:bd <args>` invocation back
// to the model. err non-nil means bd exited non-zero (or the
// subprocess failed entirely); in either case we still want to
// show whatever stdout we captured plus the error.
type rawBDMsg struct {
	args string
	out  []byte
	err  error
}

// updateOutput drives the read-only modeOutput overlay. q / esc /
// enter all close it; any other key is dropped.
func (m Model) updateOutput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc", "q", "enter":
		m.mode = modeList
		m.outputText = ""
		m.outputVP.SetContent("")
		return m, nil
	}
	// Anything else (j/k/PgUp/PgDn/g/G/d/u/ctrl+f/ctrl+b) flows
	// to the viewport, which has its own KeyMap for vim-style +
	// half-page scrolling.
	var cmd tea.Cmd
	m.outputVP, cmd = m.outputVP.Update(msg)
	return m, cmd
}

// viewOutput renders the captured bd output through the scrollable
// viewport so a long `bd list --all` doesn't lose the header and
// footer to terminal scroll. Footer shows ScrollPercent when the
// body actually overflows — mirrors the detail view's pattern.
func (m Model) viewOutput() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("bd output"))
	b.WriteString("\n")
	b.WriteString(m.outputVP.View())
	b.WriteString("\n")
	footer := "esc / q / enter to close   j/k ↑↓ scroll"
	if m.outputVP.TotalLineCount() > m.outputVP.Height {
		pct := int(m.outputVP.ScrollPercent() * 100)
		footer = fmt.Sprintf("%d%%   %s", pct, footer)
	}
	b.WriteString(helpStyle.Render(footer))
	return b.String()
}

// dispatchFilterCommand handles the `:filter <sub> <args>` family.
// Only `save <name>` is supported today; the function exists as a
// branch point so a future `:filter clear`, `:filter list`, etc.
// don't bloat the main dispatchCommand switch.
func (m Model) dispatchFilterCommand(rest string) (tea.Model, tea.Cmd) {
	sub, args, _ := strings.Cut(rest, " ")
	args = strings.TrimSpace(args)
	switch sub {
	case "save":
		if args == "" {
			m.setStatus(":filter save: missing alias name")
			return m, flashClearCmd(m.statusGen)
		}
		if m.query == "" {
			m.setStatus(":filter save: no active query to save")
			return m, flashClearCmd(m.statusGen)
		}
		// Compose the would-be aliases (in-memory state stays
		// untouched until Save succeeds) so a persistence failure
		// can't leave the session showing an alias that won't
		// survive a restart.
		path, err := filters.DefaultPath()
		if err != nil {
			m.setStatus(":filter save failed: " + err.Error())
			return m, nil
		}
		// cloneAliases guarantees a non-nil Aliases map; no nil
		// guard needed here.
		next := cloneAliases(m.filterAliases)
		next.Aliases[args] = m.query
		if err := filters.Save(path, next); err != nil {
			m.setStatus(":filter save failed: " + err.Error())
			return m, nil
		}
		m.filterAliases = next
		m.setStatus("saved @" + args)
		return m, flashClearCmd(m.statusGen)
	case "list":
		// Show every saved alias in a sorted plain-text overlay
		// (reuses modeOutput's viewport so a registry of 50+
		// aliases stays scrollable). Empty registry shows a
		// status banner instead of an empty overlay — saves a
		// keystroke for the common "I haven't saved any" case.
		if len(m.filterAliases.Aliases) == 0 {
			m.setStatus(":filter list: no aliases saved (use :filter save <name>)")
			return m, flashClearCmd(m.statusGen)
		}
		names := make([]string, 0, len(m.filterAliases.Aliases))
		for k := range m.filterAliases.Aliases {
			names = append(names, k)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString("saved filter aliases\n\n")
		for _, name := range names {
			fmt.Fprintf(&b, "  @%-12s  %s\n", name, m.filterAliases.Aliases[name])
		}
		m.outputText = b.String()
		m.outputVP.SetContent(m.outputText)
		m.outputVP.GotoTop()
		m.mode = modeOutput
		return m, nil
	case "remove":
		if args == "" {
			m.setStatus(":filter remove: missing alias name")
			return m, flashClearCmd(m.statusGen)
		}
		if _, ok := m.filterAliases.Aliases[args]; !ok {
			m.setStatus(":filter remove: no alias @" + args)
			return m, flashClearCmd(m.statusGen)
		}
		// Stage the deletion on a clone so a persist failure
		// keeps the in-memory view consistent with disk — a
		// stale ":filter list" showing a still-saved alias is
		// less confusing than an alias that silently reappears
		// after a restart.
		path, err := filters.DefaultPath()
		if err != nil {
			m.setStatus(":filter remove failed: " + err.Error())
			return m, nil
		}
		next := cloneAliases(m.filterAliases)
		delete(next.Aliases, args)
		if err := filters.Save(path, next); err != nil {
			m.setStatus(":filter remove failed: " + err.Error())
			return m, nil
		}
		m.filterAliases = next
		m.setStatus("removed @" + args)
		return m, flashClearCmd(m.statusGen)
	default:
		m.setStatus(":filter: unknown subcommand. Try: save <name>, list, remove <name>")
		return m, flashClearCmd(m.statusGen)
	}
}

// cloneAliases returns a deep copy of the on-disk Aliases shape
// so the filter-save/remove flows can stage their mutation on a
// clone and only commit it to m.filterAliases when the persist
// succeeds. Without this, a persist failure would leave the
// in-memory map mutated while the user sees ":filter * failed",
// surfacing the divergence on the next ":filter list".
func cloneAliases(a filters.Aliases) filters.Aliases {
	out := filters.Aliases{Version: a.Version, Aliases: make(map[string]string, len(a.Aliases))}
	for k, v := range a.Aliases {
		out.Aliases[k] = v
	}
	return out
}

// parseSortKey maps a string axis name to its sortKey constant.
// Used by `:sort` so the command palette can drive the same
// rotation the `s` key cycles through. Empty input is rejected
// here — `:sort` with no args is a usage error, handled in the
// caller before reaching this helper.
func parseSortKey(s string) (sortKey, bool) {
	switch strings.ToLower(s) {
	case "none":
		return sortNone, true
	case "priority", "p":
		return sortPriority, true
	case "updated":
		return sortUpdated, true
	case "repo":
		return sortRepo, true
	case "id":
		return sortID, true
	}
	return sortNone, false
}

// beginEdit suspends the TUI, opens $EDITOR on a temp file
// seeded with the cursor row's description, and on return
// dispatches Mutator.SetDescription if the body changed.
// Multi-line and arbitrary-character editing that the textinput
// modes can't do. Uses Detailer (when available) to pull the
// full description rather than the slim list-row copy.
func (m Model) beginEdit() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to edit")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	body := target.Description
	if d, ok := m.src.(Detailer); ok {
		if full, err := d.Detail(context.Background(), target); err == nil {
			body = full.Description
		}
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "wyk-edit-*.md")
	if err != nil {
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	cmd := exec.Command(editor, f.Name())
	path := f.Name()
	originalBody := body
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editFinishedMsg{target: target, path: path, originalBody: originalBody, err: err}
	})
}

// editFinishedMsg lands after $EDITOR exits. The handler reads
// the temp file and dispatches SetDescription if the body
// changed; either way the temp is removed before the message is
// fully consumed.
type editFinishedMsg struct {
	target       beads.Issue
	path         string
	originalBody string
	err          error
}

// handleEditFinished processes the ExecProcess callback: if the
// editor exited cleanly AND the body changed AND the target row
// still exists, dispatch SetDescription. Otherwise surface an
// appropriate status banner. Temp file is removed regardless so
// /tmp doesn't fill up with abandoned drafts.
func (m Model) handleEditFinished(msg editFinishedMsg) (tea.Model, tea.Cmd) {
	defer func() { _ = os.Remove(msg.path) }()
	if msg.err != nil {
		m.setStatus("edit aborted: " + msg.err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.setStatus("edit read failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	// Normalize trailing newlines on both sides before comparing:
	// vi/vim and most editors append a final '\n' when saving a
	// file that doesn't have one, so an open-and-quit on a body
	// without a trailing newline would otherwise trip the
	// "changed" branch and dispatch a spurious SetDescription. We
	// also send the trimmed body so the stored description
	// doesn't silently accumulate trailing whitespace over
	// repeated edits.
	newBody := strings.TrimRight(string(b), "\n")
	if newBody == strings.TrimRight(msg.originalBody, "\n") {
		m.setStatus("edit: no change")
		return m, flashClearCmd(m.statusGen)
	}
	if !m.issueExists(msg.target.ID) {
		m.setStatus("edit cancelled: " + msg.target.ID + " was removed by a refresh")
		return m, flashClearCmd(m.statusGen)
	}
	target := msg.target
	mu := m.mutator()
	return m, runWriteWithIssue("edit", target, func(ctx context.Context) error {
		return mu.SetDescription(ctx, target, newBody)
	})
}

// beginLabel opens the arbitrary-label prompt. The cursor row's
// label set is the toggle target: if the user enters a label
// already on the row, it's removed; otherwise it's added.
// Mirrors how H toggles `human` specifically, but for any label.
// Bulk path is add-only (matches H's bulk) so a typo can't bulk-
// remove an unrelated label across the selection.
func (m Model) beginLabel() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to label")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeLabel
	m.input.SetValue("")
	if len(m.marked) > 0 {
		m.input.Prompt = fmt.Sprintf("add label to %d rows ▸ ", len(m.marked))
	} else {
		m.input.Prompt = "label ▸ "
	}
	m.input.Placeholder = "name (toggle on cursor; bulk path is add-only)"
	m.input.Focus()
	return m, textinput.Blink
}

// updateLabel drives the label prompt. enter dispatches the
// AddLabel/RemoveLabel pair based on whether the cursor row
// already carries the label; bulk path always adds. Empty
// submission cancels.
func (m Model) updateLabel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		label := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if label == "" {
			m.setStatus("label cancelled (empty)")
			return m, flashClearCmd(m.statusGen)
		}
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("label", targets, func(ctx context.Context, i beads.Issue) error {
				if i.HasLabel(label) {
					return nil // idempotent add
				}
				return mu.AddLabel(ctx, i, label)
			})
		}
		if !m.issueExists(target.ID) {
			m.setStatus("label cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		if target.HasLabel(label) {
			return m, runWrite("unlabel:"+label, target.ID, func(ctx context.Context) error {
				return mu.RemoveLabel(ctx, target, label)
			})
		}
		return m, runWrite("label:"+label, target.ID, func(ctx context.Context) error {
			return mu.AddLabel(ctx, target, label)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// beginAssign opens the owner-change prompt. Seeded with the
// current owner so the common "fix a typo" or "keep me, just
// confirm" cases are one keystroke instead of a re-type.
// Bulk-aware via the marks set; single path snapshots the cursor
// row into pendingTarget so a concurrent refetch can't shift the
// target out from under the prompt.
func (m Model) beginAssign() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to reassign")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeAssign
	if len(m.marked) > 0 {
		m.input.SetValue("")
		m.input.Prompt = fmt.Sprintf("owner for %d rows ▸ ", len(m.marked))
	} else {
		m.input.SetValue(m.pendingTarget.Owner)
		m.input.Prompt = "owner ▸ "
	}
	m.input.Placeholder = "ev@example.com (empty = clear)"
	m.input.Focus()
	return m, textinput.Blink
}

// updateAssign drives the owner-change prompt. enter dispatches
// SetAssignee with the typed value; esc cancels. Empty value is
// honored as a deliberate clear (matches bd's behaviour for
// `--assignee ""`).
func (m Model) updateAssign(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		owner := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("assign", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.SetAssignee(ctx, i, owner)
			})
		}
		if !m.issueExists(target.ID) {
			m.setStatus("owner change cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		return m, runWriteWithIssue("assign", target, func(ctx context.Context) error {
			return mu.SetAssignee(ctx, target, owner)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// beginDefer enters modeDefer with a textinput prompt for the
// defer value (+1d, +1w, tomorrow, 2026-06-15 — bd owns parsing).
// Snapshots the cursor row into pendingTarget so a concurrent
// refetch can't shift the target out from under the user mid-
// prompt. Read-only sources show the standard read-only hint.
func (m Model) beginDefer() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	// Bulk path: marks are the targets. Single path: snapshot the
	// cursor row into pendingTarget. Either way we need at least
	// one target to proceed.
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to defer")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeDefer
	m.input.SetValue("")
	if len(m.marked) > 0 {
		m.input.Prompt = fmt.Sprintf("defer %d rows until ▸ ", len(m.marked))
	} else {
		m.input.Prompt = "defer until ▸ "
	}
	m.input.Placeholder = "+1d, +1w, tomorrow, next monday, 2026-06-15…"
	m.input.Focus()
	return m, textinput.Blink
}

// updateDefer drives the defer-until prompt. Submitting an empty
// value cancels; submitting any other value passes through to bd
// via Mutator.SetDefer (which sends it unparsed to bd update
// --defer — bd is the source of truth on what parses).
func (m Model) updateDefer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		when := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if when == "" {
			m.setStatus("defer cancelled (empty value)")
			return m, flashClearCmd(m.statusGen)
		}
		// Bulk path: dispatch SetDefer across every marked row.
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("defer", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.SetDefer(ctx, i, when)
			})
		}
		// Mirror the close/note handlers: if a refetch deleted
		// the target while the prompt was open, surface the same
		// friendly cancellation banner instead of shelling out a
		// stale ID to bd and exposing a raw error.
		if !m.issueExists(target.ID) {
			m.setStatus("defer cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		return m, runWriteWithIssue("defer", target, func(ctx context.Context) error {
			return mu.SetDefer(ctx, target, when)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// toggleShowClosed flips the include-closed flag on both the model
// (for chip rendering) and the underlying Source (for the next
// fetch query / bd subcommand choice), then triggers a refetch so
// the rows reflect the new scope.
func (m Model) toggleShowClosed() (tea.Model, tea.Cmd) {
	m.showClosed = !m.showClosed
	if tog, ok := m.src.(ClosedToggler); ok {
		tog.SetIncludeClosed(m.showClosed)
	}
	m.cursor = 0
	m.refreshing = true
	return m, m.fetchCmd()
}

// View dispatches to the per-mode renderer.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeHelp:
		return m.viewHelp()
	case modeColumns:
		return m.viewColumns()
	case modeOutput:
		return m.viewOutput()
	default:
		return m.viewList()
	}
}

// viewColumns renders the column-visibility overlay. Each
// toggleable column appears as a numbered row with a [x]/[ ]
// checkbox; the number is the toggle key. Multi-only columns
// render greyed out in single-repo mode so a user toggling 4
// (Branch) sees why nothing happens.
func (m Model) viewColumns() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("Columns"))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render(fmt.Sprintf("Press 1-%d to toggle. ID, P, and Title are always shown.", len(toggleableColumns))))
	b.WriteString("\n\n")
	multi := m.isMultiRepo()
	for i, col := range toggleableColumns {
		check := "[ ]"
		if !m.colsHidden[col.ID] {
			check = "[x]"
		}
		line := fmt.Sprintf("  %d. %s  %s", i+1, check, col.Label)
		if col.MultiOnly && !multi {
			line += "  " + helpStyle.Render("(multi-repo only)")
			b.WriteString(helpStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("esc / o / q to close (saves to ~/.config/wyk/ui.json)"))
	return b.String()
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
		{"Filters", []key.Binding{m.keys.Filter, m.keys.Human, m.keys.Cycle, m.keys.SortCycle, m.keys.ShowClosed, m.keys.Columns}},
		{"Writes", []key.Binding{m.keys.Close, m.keys.ToggleHuman, m.keys.AddNote, m.keys.QuickAdd}},
		{"Meta", []key.Binding{m.keys.Refresh, m.keys.Help, m.keys.Quit}},
	}
	for _, g := range groups {
		b.WriteString("\n")
		b.WriteString(detailLabelStyle.Render(g.title))
		b.WriteString("\n")
		for _, kb := range g.bindings {
			h := kb.Help()
			fmt.Fprintf(&b, "  %-6s  %s\n", h.Key, h.Desc)
		}
	}
	b.WriteString("\n")
	b.WriteString(detailLabelStyle.Render("Notes"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  IDs in the table are shown without the repeated workspace prefix\n"))
	b.WriteString(helpStyle.Render("  (e.g. \"ma5.2.1\" stands for \"" + exampleFullID(m) + "ma5.2.1\").\n"))
	b.WriteString(helpStyle.Render("  Press ⏎ to expand a row and see the full ID in the detail view.\n"))
	b.WriteString(helpStyle.Render("  Mouse: click a row to set the cursor; wheel scrolls up/down.\n"))
	b.WriteString(helpStyle.Render("  Yank (y) uses OSC 52 so the copy reaches your local clipboard\n"))
	b.WriteString(helpStyle.Render("  even over SSH; in tmux, enable `set -g allow-passthrough on`.\n"))
	b.WriteString("\n")

	// Status column legend — each row renders the value with its
	// actual style (so the user sees the color/strike treatment
	// used in the table) followed by a plain-text gloss. The
	// `wip` row is intentional: bd's underlying status is
	// `in_progress`, but the Status column abbreviates it.
	b.WriteString(detailLabelStyle.Render("Status column"))
	b.WriteString("\n")
	legend := []struct {
		display string
		raw     string // input to statusStyleFor (uses the real bd value)
		gloss   string
	}{
		{"open", "open", "available for work"},
		{"wip", "in_progress", "in progress (abbreviated in the table)"},
		{"blocked", "blocked", "has an open dependency or is human-blocked"},
		{"deferred", "deferred", "hidden from `bd ready` until a date (set via `d`)"},
		{"closed", "closed", "done; strikethrough"},
	}
	for _, e := range legend {
		styled := statusStyleFor(e.raw).Render(fmt.Sprintf("%-9s", e.display))
		fmt.Fprintf(&b, "  %s  %s\n", styled, helpStyle.Render(e.gloss))
	}
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
	if chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); chips != "" {
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
	case modeFilter, modeNote, modeQuickAdd, modeDefer, modeCommand, modeAssign, modeLabel:
		b.WriteString("\n")
		b.WriteString(m.input.View())
	case modeConfirmClose:
		// Render the captured ID, not the cursor's current target —
		// a refetch may have shifted things since the prompt opened.
		// Bulk path: pendingTarget.ID is "" and the prompt counts
		// the marked rows; single path: prompt shows the ID.
		b.WriteString("\n")
		if n := len(m.marked); n > 0 {
			b.WriteString(confirmStyle.Render(
				fmt.Sprintf("close %d marked rows? [y/N]", n)))
		} else if m.pendingTarget.ID != "" {
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
	colWyk     = 3  // wyk-hook indicator: ✓ if installed, blank if not. Header reads "wyk" so the column is self-explanatory.
	colRepo    = 18
	colBranch  = 10
	colID      = 12
	colType    = 4
	colStatus  = 8
	colPrio    = 2
	colUpdated = 8 // 8 chars so the "Updated↓" sort-arrow decoration fits without overflowing into the Title column. relTime values ("4h ago", "2 weeks", etc.) are all ≤ 7 chars so the extra slack is harmless when no sort is active.
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
	var b strings.Builder
	b.WriteString(cursor)
	if m.colVisible(colIDOwner) {
		fmt.Fprintf(&b, "%-*s  ", colResp, "owner")
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDWyk) {
			fmt.Fprintf(&b, "%-*s  ", colWyk, "wyk")
		}
		if m.colVisible(colIDRepo) {
			fmt.Fprintf(&b, "%-*s  ", colRepo, sortDecorate("Repo", m.sortBy == sortRepo, "↑", m.sortDesc))
		}
		if m.colVisible(colIDBranch) {
			fmt.Fprintf(&b, "%-*s  ", colBranch, "Branch")
		}
	}
	fmt.Fprintf(&b, "%-*s  ", colID, sortDecorate("ID", m.sortBy == sortID, "↑", m.sortDesc))
	if m.colVisible(colIDType) {
		fmt.Fprintf(&b, "%-*s  ", colType, "T")
	}
	if m.colVisible(colIDStatus) {
		fmt.Fprintf(&b, "%-*s  ", colStatus, "Status")
	}
	fmt.Fprintf(&b, "%-*s  ", colPrio, sortDecorate("P", m.sortBy == sortPriority, "↑", m.sortDesc))
	if m.colVisible(colIDUpdated) {
		fmt.Fprintf(&b, "%-*s  ", colUpdated, sortDecorate("Updated", m.sortBy == sortUpdated, "↓", m.sortDesc))
	}
	b.WriteString("Title")
	return tableHeaderStyle.Render(b.String())
}

// sortDecorate appends an arrow to a column header when that
// column is the active sort axis. natural is the arrow for the
// axis's natural direction (priority asc → ↑, updated desc → ↓);
// reverse flips it. Lets renderHeader stay a flat fmt.Fprintf
// rather than carrying a separate "active? reversed?" branch per
// column.
func sortDecorate(label string, active bool, natural string, reverse bool) string {
	if !active {
		return label
	}
	if reverse {
		return label + flipArrow(natural)
	}
	return label + natural
}

func flipArrow(a string) string {
	switch a {
	case "↑":
		return "↓"
	case "↓":
		return "↑"
	}
	return a
}

func (m Model) renderRow(i beads.Issue, selected bool) string {
	cursor := "  "
	if selected {
		cursor = cursorStyle.Render("▶ ")
	}
	// Marked rows get a ✓ in the cursor cell (replacing the
	// leading space) so the multi-select is visible at a glance.
	// Selected-and-marked shows ▶ (cursor wins; the user knows
	// they're on a marked row from the context).
	if !selected && m.marked[issueKey(i)] {
		cursor = cursorStyle.Render("✓ ")
	}
	var b strings.Builder
	b.WriteString(cursor)
	if m.colVisible(colIDOwner) {
		b.WriteString(paddedResponsibilityBadge(i))
		b.WriteString("  ")
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDWyk) {
			w := strings.Repeat(" ", colWyk)
			if i.WykHooked {
				w = " " + wykIndicatorStyle.Render("✓") + " "
			}
			b.WriteString(w)
			b.WriteString("  ")
		}
		if m.colVisible(colIDRepo) {
			b.WriteString(typeStyle.Render(fmt.Sprintf("%-*s", colRepo, trunc(i.Repo, colRepo))))
			b.WriteString("  ")
		}
		if m.colVisible(colIDBranch) {
			b.WriteString(typeStyle.Render(fmt.Sprintf("%-*s", colBranch, trunc(i.Branch, colBranch))))
			b.WriteString("  ")
		}
	}
	b.WriteString(idStyle.Render(fmt.Sprintf("%-*s", colID, trunc(m.displayID(i), colID))))
	b.WriteString("  ")
	if m.colVisible(colIDType) {
		b.WriteString(typeStyle.Render(fmt.Sprintf("%-*s", colType, abbrevType(i.IssueType))))
		b.WriteString("  ")
	}
	if m.colVisible(colIDStatus) {
		b.WriteString(statusStyleFor(i.Status).Render(fmt.Sprintf("%-*s", colStatus, abbrevStatus(i.Status))))
		b.WriteString("  ")
	}
	fmt.Fprintf(&b, "P%d", i.Priority)
	b.WriteString("  ")
	if m.colVisible(colIDUpdated) {
		b.WriteString(updatedStyle.Render(fmt.Sprintf("%-*s", colUpdated, relTime(i.UpdatedAt))))
		b.WriteString("  ")
	}
	// Truncate the title to whatever space remains after every
	// preceding column. Without this, long titles wrap or overflow
	// the right edge — most existing rows in real use spill past
	// the terminal. Detail view (enter) still shows the full text.
	title := i.Title
	origLen := utf8.RuneCountInString(title)
	if avail := m.titleBudget(); avail > 0 {
		title = trunc(title, avail)
	}
	// Apply fuzzy-match highlighting after truncation. When trunc
	// inserts an ellipsis (`runes[:n-1] + "…"`, taken when n >= 2
	// AND the string was longer than n), drop any match at or
	// after the ellipsis position so we don't style the `…` glyph
	// itself. trunc's n==1 branch returns a bare first rune with
	// NO ellipsis, so we keep the visibleLen >= 2 guard to avoid
	// suppressing a legitimate index-0 highlight in that case.
	// Matches past the truncated tail are silently dropped (no
	// off-screen ANSI).
	if idxs := m.titleMatches[issueKey(i)]; len(idxs) > 0 {
		if visibleLen := utf8.RuneCountInString(title); visibleLen < origLen && visibleLen >= 2 {
			ceiling := visibleLen - 1
			filtered := make([]int, 0, len(idxs))
			for _, ix := range idxs {
				if ix < ceiling {
					filtered = append(filtered, ix)
				}
			}
			idxs = filtered
		}
		title = highlightRunes(title, idxs, fuzzyMatchStyle)
	}
	b.WriteString(title)
	return b.String()
}

// byteToRuneIdxs converts the byte-offset slice fuzzy returns into
// rune-index positions inside the source string. sahilm/fuzzy
// works on bytes, but renderer logic for highlighting wants
// rune-aligned positions so a multi-byte glyph isn't half-styled.
// Assumes byteIdxs is sorted ascending (which fuzzy guarantees).
func byteToRuneIdxs(s string, byteIdxs []int) []int {
	if len(byteIdxs) == 0 {
		return nil
	}
	out := make([]int, 0, len(byteIdxs))
	runeIdx := 0
	next := 0
	for byteOff := range s {
		if next < len(byteIdxs) && byteOff == byteIdxs[next] {
			out = append(out, runeIdx)
			next++
			if next == len(byteIdxs) {
				break
			}
		}
		runeIdx++
	}
	return out
}

// highlightRunes returns s with the runes at the given rune
// indices wrapped in style.Render. Indices past the end of s
// (e.g. matches in a truncated title) are silently dropped.
func highlightRunes(s string, idxs []int, style lipgloss.Style) string {
	if len(idxs) == 0 {
		return s
	}
	set := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		set[i] = true
	}
	var b strings.Builder
	pos := 0
	for _, r := range s {
		if set[pos] {
			b.WriteString(style.Render(string(r)))
		} else {
			b.WriteRune(r)
		}
		pos++
	}
	return b.String()
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
	// non-final column to mirror what renderRow prints. Hidden
	// columns contribute 0 — the saved width flows into the title
	// cell, which is exactly what users hide columns for.
	const sep = 2
	used := 2 // cursor (▶ or 2 spaces is 2 visual cols either way)
	if m.colVisible(colIDOwner) {
		used += colResp + sep
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDWyk) {
			used += colWyk + sep
		}
		if m.colVisible(colIDRepo) {
			used += colRepo + sep
		}
		if m.colVisible(colIDBranch) {
			used += colBranch + sep
		}
	}
	used += colID + sep
	if m.colVisible(colIDType) {
		used += colType + sep
	}
	if m.colVisible(colIDStatus) {
		used += colStatus + sep
	}
	used += colPrio + sep // "Pn" is 2 chars
	if m.colVisible(colIDUpdated) {
		used += colUpdated + sep
	}
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
//   - has `human` label → plain "HUMAN" (the human-needs-to-act
//     signal trumps everything else; src distinction is dropped —
//     a glance at the column should give a yes/no answer, not a
//     three-way categorisation that buries the lede)
//   - has `src:agent` and no `human` → AGENT or HUMAN-BLOCK
//     depending on dep state
//   - otherwise → empty (no responsibility signal applies)
func responsibilityBadgeFor(i beads.Issue) string {
	if i.IsHuman() {
		return humanBadge.Render("HUMAN")
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
	if stats := m.renderStatsLine(); stats != "" {
		left += "  " + stats
	}
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

// renderStatsLine builds the "· N human · M mine" suffix appended
// to the status bar's left side. Computed from m.all (no extra
// fetch) and intentionally excludes "ready" — bd ready has
// blocker-aware semantics that a label count can't approximate,
// and a wrong number in a stats line is worse than no number.
// Empty when there's nothing to display (no human, no me set).
//
// IMPORTANT: counts are scoped to the *current preset*'s fetch.
// m.all holds only the rows the active preset returned, so "N
// human" under PresetReady counts human-flagged ready issues
// (not workspace-wide). The `N/M` cell to the left of this
// suffix already advertises the preset name, so the scoping is
// implicit — but if a future preset is added where the count
// could mislead, surface the scoping explicitly here.
func (m Model) renderStatsLine() string {
	human := 0
	mine := 0
	for _, i := range m.all {
		for _, l := range i.Labels {
			if l == "human" {
				human++
				break
			}
		}
		if m.me != "" && i.Owner == m.me {
			mine++
		}
	}
	var parts []string
	if human > 0 {
		parts = append(parts, fmt.Sprintf("%d human", human))
	}
	if m.me != "" {
		// Show the mine slot even at 0 so the user knows it's
		// computed — silently dropping when zero would make a
		// user think their identity isn't wired up.
		parts = append(parts, fmt.Sprintf("%d mine", mine))
	}
	if len(parts) == 0 {
		return ""
	}
	return "· " + strings.Join(parts, " · ")
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
	if m.preset != filter.PresetAll || m.priorityCap >= 0 || m.sortBy != sortNone || m.showClosed {
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
	// Every mode that renders a textinput prompt at the bottom of
	// viewList (see line ~2972) costs 2 lines of chrome — a blank
	// separator + the input itself. Keeping this list in sync with
	// the viewList switch is the only way bodyHeight stays
	// accurate; an under-count pushes the title/last rows past the
	// terminal edge while a prompt is open.
	case modeFilter, modeNote, modeQuickAdd, modeDefer, modeCommand, modeAssign, modeLabel:
		n += 2 // blank + textinput
	case modeConfirmClose:
		if m.pendingTarget.ID != "" || len(m.marked) > 0 {
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
// is the default `all`, no priority cap, no sort, no show-closed)
// so a fresh view stays chrome-free. Each active filter renders
// as an amber pill.
func renderFilterChips(p filter.Preset, priorityCap int, sortBy sortKey, showClosed bool) string {
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
	if sortBy != sortNone {
		parts = append(parts, chipActiveStyle.Render(" ↕ "+sortBy.label()+" "))
	}
	if showClosed {
		parts = append(parts, chipActiveStyle.Render(" +closed "))
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
