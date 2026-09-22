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
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/filters"
)

// refreshInterval is how often the TUI polls bd for changes. A timer
// keeps things simple and avoids a filesystem-watcher dependency;
// .beads/issues.jsonl rewrites are cheap to re-query.
//
// Intentionally LARGER than — and decoupled from — the beads client
// per-call timeout (beads.perCallTimeout, 10s). When the two were
// equal, a refresh that nearly filled its 10s budget was immediately
// re-triggered by the next tick, so the machine never idled long
// enough to recover and the timeouts became self-sustaining. The
// extra headroom (plus the tickMsg in-flight guard below) guarantees
// a slow refresh finishes and the engines quiesce before the next
// poll starts. Warm-start paints cached rows on launch, so the
// longer poll is invisible to first paint.
const refreshInterval = 20 * time.Second

// minFSRefreshGap is the floor between two fs-watch-driven refreshes.
// The watcher's 250ms debounce coalesces ONE write's event storm (a
// single bd write emits ~130 fsnotify events as it streams the
// issues.jsonl export) into one refresh, but it can't rate-limit
// ACROSS writes: a chatty writer touching any registered repo's
// .beads/ — an agent looping bd commands, a git pull importing
// several repos, a sync daemon — would otherwise fire a full
// cross-repo refetch per write and churn the view continuously. This
// caps fs-driven refreshes to one per gap; the first event after a
// quiet period still refreshes near-instantly, and the poll tick
// backstops any change that lands during the cooldown. Measured from
// the last fetch's COMPLETION (m.lastSync) so a slow multi-repo fetch
// gets a real idle window after it before the next fs refresh.
const minFSRefreshGap = 5 * time.Second

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
// a / H / n keystrokes dispatch through it. A read-only Source
// remains valid — the write keys show a "read-only" hint instead.
//
// The methods take a full beads.Issue rather than a bare ID so a
// multi-repo Mutator can route on issue.Repo. With bare IDs, two
// workspaces that happen to use the same ID (or any non-prefixed
// scheme) would silently mis-route — see the regression test
// TestMultiBDSource_WriteRoutesByRepoNotID for the case that drove
// this interface shape.
// BulkCloser is an optional Mutator extension: close many issues in as
// few bd calls as possible. The TUI's bulk close (mark rows, a, y) uses
// it when present — one `bd close id1 id2 …` per workspace instead of a
// subprocess per row (would-you-kindly-cexj). Returns the issues that
// did NOT close, each with its error; nil means every issue closed.
type BulkCloser interface {
	CloseMany(ctx context.Context, issues []beads.Issue) []BulkFailure
}

// BulkFailure is one issue a BulkCloser could not close.
type BulkFailure struct {
	Issue beads.Issue
	Err   error
}

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
	// SetIssueType changes the issue's type. The caller is
	// responsible for passing a bd-accepted value; the T key
	// cycles through the known list (task / bug / feature /
	// chore / epic / decision / spike / story / milestone).
	SetIssueType(ctx context.Context, issue beads.Issue, issueType string) error
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

// DepLister exposes one issue's direct dependencies (ListDeps) and
// dependents (ListDependents) so the model's topological deps-sort
// can build the edge set for the visible rows and the detail view
// can show both directions. Optional — when the Source doesn't
// satisfy it the deps sort degrades to the bd-supplied
// DependencyCount level proxy (see applySort's sortDeps branch) and
// the detail view simply omits the dependency sections. Both
// BDSource and MultiBDSource implement it; the model type-asserts
// m.src at runtime, mirroring the Mutator / Detailer pattern.
//
// Both methods take a bare ID rather than a full Issue (unlike the
// write methods): the deps sort keys everything off Issue.ID, and a
// MultiBDSource routes by ID prefix anyway, so threading the Repo
// through would add nothing the ID doesn't already encode.
type DepLister interface {
	ListDeps(ctx context.Context, id string) ([]beads.Issue, error)
	ListDependents(ctx context.Context, id string) ([]beads.Issue, error)
}

// Model is the Bubble Tea model.
type Model struct {
	src    Source
	keys   keyMap
	mode   mode
	preset filter.Preset

	// humanReturnPreset is the preset the user was on when they
	// pressed `h` to jump into the human view, so a second `h`
	// toggles back to it instead of being a one-way trip. Empty (or
	// itself "human") falls back to PresetAll. Recorded only on the
	// way IN so a stray double-press can't strand the return target
	// on "human".
	humanReturnPreset filter.Preset

	// presetCache holds the last successful fetch for each preset so
	// switching back to a preset paints its rows INSTANTLY instead of
	// showing the previous preset's list for the 2-3s a cold
	// multi-repo bd round-trip takes. A switch still dispatches an
	// authoritative fetch to reconcile (cached rows are at most one
	// refresh-interval stale, and the refreshing indicator signals
	// it). Entries are scoped to the current showClosed setting, so
	// toggling C clears the map. nil until the first fetch lands — a
	// cache miss falls back to the old keep-stale-rows-until-fetch
	// behavior. Reworked from PR #18 onto the preset-tagged
	// fetchedMsg architecture (would-you-kindly-tjiy).
	presetCache map[filter.Preset][]beads.Issue
	query       string

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

	// promptReturn is the mode a write prompt (modeConfirmClose /
	// modeDefer / modeNote) returns to when it closes. Zero value is
	// modeList — the long-standing behaviour for list-initiated
	// prompts. The detail view sets it to modeDetail so a close /
	// defer / note opened while reading one task returns to that
	// task instead of dropping back to the list. (Close is the lone
	// exception: a successful close forces modeList because the
	// just-closed issue leaves the open view.) The prompt overlay
	// also renders over viewDetail vs viewList based on this.
	promptReturn mode

	// detailIssue is the enriched (full-field, includes notes) issue
	// shown in the detail view. Populated by a Detail Cmd dispatched
	// on enter; before the result arrives the view falls back to the
	// slim Issue from m.visible.
	detailIssue beads.Issue

	// detailLinkIdx is the cursor over the detail view's selectable
	// dependency/dependent rows (the flattened deps++dependents list
	// for detailIssue). -1 means no link is highlighted (the default
	// on entry); Tab/Shift-Tab cycle it, Enter opens the highlighted
	// link. j/k still scroll the body — link selection is the separate
	// Tab axis (see updateDetail).
	detailLinkIdx int

	// detailStack is the drill-in history: opening a highlighted link
	// pushes the current detailIssue here so Esc/Back pops to the
	// issue you came from rather than all the way out to the list.
	// Empty stack → Back returns to the list as before.
	detailStack []beads.Issue

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

	// cachePath is where SaveCache writes the on-disk snapshot of
	// each successful fetch; empty disables persistence. Set by
	// main via WithCacheSnapshot so test models default to no
	// persistence (no surprise writes when t.TempDir isn't set).
	cachePath string
	// cacheScope is the CacheScope fingerprint of the source this
	// model was built against. Stamped into every saved snapshot and
	// required to match before a snapshot is seeded (see Cache.Scope).
	cacheScope string
	// cacheStale is true while m.all is sourced from the on-disk
	// cache and no live fetch has landed yet. Drives a subtle
	// "cached <relative> · refreshing" indicator in the status bar
	// so the user knows the rows they're looking at are not
	// guaranteed current. Cleared on the first successful
	// fetchedMsg.
	cacheStale bool
	// cacheSavedAt is the SavedAt timestamp of the seeded cache,
	// used by the status indicator. Zero when no seed happened.
	cacheSavedAt time.Time

	// scroll is the row index at the top of the rendered window —
	// used to keep the column header visible when m.visible has
	// more rows than the terminal can fit. Without it, the terminal
	// scrolls overflow off the top and the header disappears.
	// Maintained by ensureCursorVisible whenever the cursor moves
	// or the data set changes shape.
	scroll int

	// mouse drives the terminal's mouse-capture state directly —
	// *tea.Program satisfies it. Direct calls instead of
	// tea.EnableMouseCellMotion/DisableMouse cmds because batching
	// those with other cmds delays or drops the terminal write (the
	// PR #24 live finding); a synchronous method call cannot be
	// reordered or dropped. nil (tests, or before main wires the
	// program) = no-op.
	mouse MouseController

	// mouseOff disables mouse capture: no wheel/click navigation,
	// but the terminal's native click-drag text selection works
	// without a Shift/Option modifier. Toggled with `m`, restored
	// from state.json (zero value = captured, the default). Mouse
	// capture was originally dropped entirely for native selection
	// (would-you-kindly-p2hn); the toggle serves both wants.
	mouseOff bool

	// dragScroll is set while the left button is held on the list
	// scrollbar's thumb; motion events then map the pointer's row to
	// a scroll offset. dragGrab is where inside the thumb the press
	// landed, so the thumb doesn't jump to put its top under the
	// pointer on the first motion.
	dragScroll bool
	dragGrab   int

	// layoutPref is the user's split-vs-stacked choice for the detail
	// pane (`p`), restored from state.json. Zero value = auto: split
	// when the terminal clears splitMinWidth×splitMinHeight. See
	// split.go.
	layoutPref layoutPref

	// followGen tags the debounced detail-follow tick so a tick
	// scheduled for a row the cursor has since left is dropped.
	followGen int

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

	// sessionPath is where persistSession writes the last
	// filter/sort/cursor on quit so the next launch can restore it;
	// empty disables persistence (tests, read-only runs). Wired by
	// WithSession. See internal/tui/session.go.
	sessionPath string

	// pendingCursorID is the issue ID restored from the session file,
	// consumed once when the first successful fetch populates
	// m.visible: restoreCursorFromSession finds the row and moves the
	// cursor there (or falls back to the top if it's gone), then
	// clears this so subsequent refresh ticks don't keep yanking the
	// cursor back. Empty means "no cursor to restore".
	pendingCursorID string

	// depLister resolves an issue's direct dependencies for the
	// topological deps-sort. Set in New from m.src when the Source
	// satisfies DepLister (both BDSource and MultiBDSource do); nil
	// when the Source is read-only / a test stub without dep
	// support, in which case the deps sort falls back to the
	// DependencyCount level proxy.
	depLister DepLister

	// depCache memoises the direct-dependency edge set per issue ID
	// so the deps-sort doesn't re-shell `bd dep list` on every
	// re-sort / re-filter. Populated asynchronously: when the deps
	// sort is active and some visible row's edges aren't cached
	// yet, recomputeVisible dispatches a resolveDepsCmd and re-sorts
	// when the depsResolvedMsg lands. A nil/missing entry is treated
	// as "unknown" (the sort falls back to the count proxy until
	// every visible row is resolved); a present-but-empty entry
	// means "resolved, no deps".
	depCache map[string][]beads.Issue

	// depErr records the IDs whose ListDeps call failed so the
	// resolver doesn't spin re-fetching a workspace that keeps
	// erroring. An errored ID is treated as resolved-with-no-deps
	// for sort purposes (in-degree 0), matching the "unknown deps
	// are satisfied" rule the off-screen-dependency case relies on.
	depErr map[string]bool

	// dependentCache / dependentErr are the reverse-direction twins
	// of depCache / depErr: dependentCache memoises the issues each
	// ID blocks (its direct dependents), dependentErr records IDs
	// whose ListDependents call failed. Both are populated lazily on
	// detail entry (the deps-sort path doesn't need the reverse edge)
	// and keyed per issue ID, cached for the session so re-opening
	// the same detail view doesn't re-shell `bd dep list`.
	dependentCache map[string][]beads.Issue
	dependentErr   map[string]bool

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

	// priorityEmphasis colour-codes the P column (P0 loud, P3–P4 dim)
	// when on. Off by default to keep the flat uniform-white table;
	// loaded from uiconfig and toggled with `p` in the `o` overlay,
	// persisted alongside the column visibility.
	priorityEmphasis bool

	// autoHidden is the set of columns hidden PURELY to fit the current
	// terminal width, on top of the user's own colsHidden. Render-
	// transient: viewList recomputes it each paint, so widening the
	// terminal brings every column back without touching saved prefs.
	// Never persisted.
	autoHidden map[string]bool

	// cw holds the per-paint display width of each fixed column, sized
	// to its content (header vs widest value, clamped). viewList
	// recomputes it each paint before rendering; renderHeader/renderRow/
	// titleBudget/computeAutoHidden read it. Never persisted.
	cw colWidths

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

	// closing holds the issueKeys of closes currently in flight.
	// A bd close can take seconds on a cold multi-repo workspace,
	// and until this existed NOTHING on screen changed during that
	// window: the row sat there un-marked, so a user who thought
	// the keypress hadn't landed would press again — against
	// whatever row the cursor was on by then — and close the WRONG
	// task (would-you-kindly-khtw). Two jobs: the marked rows render
	// as "closing" for immediate feedback, and closeBlockedBy
	// refuses to start another close while the set is non-empty.
	// Populated at dispatch, cleared when the write result lands
	// (success drops the row from the list via optimisticListUpdate;
	// failure leaves it with an error banner) — so the block lifts
	// exactly when the closing row stops being actionable.
	closing map[string]bool

	// lastAction snapshots the most recent write so `.` can
	// re-apply it to the cursor row without re-prompting. The
	// kind tag (close/defer/assign/label/unlabel/priority/flag/
	// unflag) plus a stringified arg is enough to re-dispatch
	// through the same Mutator method. Captured at each
	// successful single-target dispatch site.
	lastAction repeatableAction

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

	// input is the single-line textinput shared by every prompt
	// mode that isn't modeNote: modeFilter, modeQuickAdd,
	// modeDefer, modeCommand, modeAssign, and modeLabel. The
	// modes are mutually exclusive — only one prompt is on
	// screen at a time — so a single field is enough;
	// Prompt/Placeholder are reconfigured on entry. modeNote
	// uses noteArea (textarea) for multi-line drafting.
	input textinput.Model

	// noteArea is the multi-line editor used by modeNote so a
	// long-form note (steps, context, dated entries) doesn't
	// have to be shoved into a single-line textinput. ctrl+s
	// submits; esc cancels; enter inserts a newline. Width/height
	// are set on WindowSizeMsg alongside the other components.
	noteArea textarea.Model
}

// New constructs a Model with the given Source and a sensible default
// preset (all). For a startup hint banner (e.g. "no repos registered;
// run wyk init -scan ~/Projects to discover them"), use NewWithHint.
func New(src Source) Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "fuzzy filter… (↑↓ select)"
	ti.CharLimit = 200

	na := textarea.New()
	na.Placeholder = "append a note (ctrl+s submit, esc cancel; enter inserts a newline)"
	na.SetWidth(80)
	na.SetHeight(6)
	na.ShowLineNumbers = false

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = setupHintStyle

	m := Model{
		src:            src,
		keys:           defaultKeyMap(),
		mode:           modeList,
		preset:         filter.PresetAll,
		input:          ti,
		noteArea:       na,
		loading:        true, // first paint shows "loading…" until Init's fetch returns
		detailVP:       viewport.New(80, 20),
		outputVP:       viewport.New(80, 20),
		spinner:        sp,
		priorityCap:    -1,
		detailLinkIdx:  -1,
		depCache:       map[string][]beads.Issue{},
		depErr:         map[string]bool{},
		dependentCache: map[string][]beads.Issue{},
		dependentErr:   map[string]bool{},
	}
	// Adopt the dep-resolution seam when the Source supports it so
	// the deps sort can fetch real edges. Read-only / stub Sources
	// leave this nil and the sort degrades to the count proxy.
	if dl, ok := src.(DepLister); ok {
		m.depLister = dl
	}
	return m
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

// WithPriorityEmphasis seeds the opt-in P-column colour-coding from
// uiconfig at startup. Returns a copy so the builder chains alongside
// WithHiddenColumns.
func (m Model) WithPriorityEmphasis(on bool) Model {
	m.priorityEmphasis = on
	return m
}

// WithCacheSnapshot seeds m.all from a previously-saved Cache so
// the first paint shows rows immediately instead of the empty
// "loading…" stand-in. The live fetch dispatched by Init still
// runs in parallel — when it returns, fresh data replaces the
// cached snapshot.
//
// Path is where future successful fetches will be persisted; main
// passes this through so the model can call SaveCache after each
// fetchedMsg lands. Empty path disables persistence.
//
// The snapshot seeds directly when c.Preset matches the model's
// current preset. When it doesn't — the common case being "quit on
// all, reopen restoring human", which used to stare at the loading
// spinner for the whole multi-repo cold start — an "all" snapshot
// is filtered in memory to approximate the current preset
// (issuesMatchingPreset), since human/mine/blocked are plain field
// predicates over the all-superset. A non-"all" snapshot can't
// reconstruct a different view (a subset has no superset), and
// "ready" has blocker semantics only bd can compute; both fall
// through to the cold start. The snapshot is also dropped when it
// carries zero issues (no value in pre-painting an empty list).
// Reworked from PR #18 (would-you-kindly-tjiy).
func (m Model) WithCacheSnapshot(c Cache, path string) Model {
	m.cachePath = path
	if len(c.Issues) == 0 {
		return m
	}
	if c.Scope != m.cacheScope {
		// Rows from a different registry / workspace: never paint
		// them, even for a beat. The path stays wired so this launch
		// writes a correctly-scoped snapshot for the next one.
		return m
	}
	// Strip closed rows up front, whatever the preset: the snapshot
	// may have been saved while showClosed was on, but SessionState
	// doesn't persist that toggle, so a relaunch is always
	// closed-excluded — seeding raw would paint closed rows into a
	// closed-excluded view until the live fetch lands
	// (roborev #2063; the cross-preset predicates were hardened the
	// same way one round earlier).
	issues := make([]beads.Issue, 0, len(c.Issues))
	for _, i := range c.Issues {
		if i.Status != "closed" {
			issues = append(issues, i)
		}
	}
	if len(issues) == 0 {
		return m
	}
	if filter.Preset(c.Preset) != m.preset {
		filtered, ok := issuesMatchingPreset(issues, filter.Preset(c.Preset), m.preset, m.me)
		if !ok || len(filtered) == 0 {
			// Can't approximate this view — or the approximation is
			// empty, and pre-painting an empty list has no value
			// (same rationale as the zero-issue guard above). Cold
			// start instead.
			return m
		}
		issues = filtered
	}
	m.all = issues
	m.commonPrefix = commonIDPrefix(m.all)
	m.recomputeVisible()
	// Warm-start seeded the visible set, so a session-restored cursor
	// can land on the very first frame. Don't consume it — the
	// authoritative live fetch re-runs the (idempotent) restore and
	// owns the clear.
	m.restoreCursorFromSession(false)
	m.cacheStale = true
	m.cacheSavedAt = c.SavedAt
	return m
}

// WithPreset returns a copy of the model with the given preset
// selected as the startup view. Unknown presets are rejected (a
// silent fall-through to PresetAll would hide a typo from the
// user); the caller is expected to validate via filter.IsPreset
// before this call. main wires this from the -preset flag so a
// shell alias like `wykh = wyk -preset human` lands directly on
// the human view.
// WithCacheScope records the source fingerprint (CacheScope of the
// repo paths) the model was built against. Call it BEFORE
// WithCacheSnapshot so the seed can be matched against it.
func (m Model) WithCacheScope(scope string) Model {
	m.cacheScope = scope
	return m
}

func (m Model) WithPreset(p filter.Preset) Model {
	m.preset = p
	return m
}

// WithSession hydrates the model from a persisted SessionState and
// records the path persistSession writes back to on quit. It restores
// the filter preset (only when it names a known preset — an unknown
// value is ignored rather than coerced), the sort key (matched by its
// label), and stages the cursor ID for restoreCursorFromSession to
// apply once the first fetch lands. main applies this BEFORE WithPreset
// so an explicit `-preset` flag still wins over the persisted view, and
// before WithCacheSnapshot so the warm-start cache is matched against
// the restored preset. An empty path leaves persistence disabled.
func (m Model) WithSession(s SessionState, path string) Model {
	m.sessionPath = path
	if filter.IsPreset(s.Preset) {
		m.preset = filter.Preset(s.Preset)
	}
	if k, ok := sortKeyFromLabel(s.Sort); ok {
		m.sortBy = k
		// Direction only rides along with a restored axis — there's
		// no direction to reverse without one, and the sortNone
		// invariant keeps sortDesc false.
		m.sortDesc = s.SortDesc
	}
	m.pendingCursorID = s.CursorID
	m.mouseOff = s.MouseOff
	m.layoutPref = layoutPrefFromLabel(s.Layout)
	return m
}

// quitNow persists the session state (best-effort) and returns the
// tea.Quit command. Every exit path routes through here — `q`,
// ctrl-c, and the per-mode quit handlers — so the last
// filter/sort/cursor is written regardless of which mode the user
// left from. The save is synchronous on purpose: dispatching it as a
// tea.Cmd alongside tea.Quit races the program's teardown, which can
// terminate before the Cmd goroutine runs and silently drop the
// write. A state.json write is a few hundred bytes and the user is
// leaving anyway, so the cost is invisible.
func (m Model) quitNow() (tea.Model, tea.Cmd) {
	m.persistSession()
	return m, tea.Quit
}

// persistSession writes the current filter/sort/cursor to
// m.sessionPath. Best-effort and defensive: a nil/empty path
// disables it, and the cursor ID is only captured when the cursor
// indexes a real visible row (an error or empty-list state persists
// no cursor, so the next launch falls back to the top). A write
// failure is swallowed — there's no good place to surface it during
// teardown, and a missing restore is a cosmetic loss, not a failure.
func (m Model) persistSession() {
	if m.sessionPath == "" {
		return
	}
	st := SessionState{
		Version: sessionVersion,
		Preset:  string(m.preset),
		Sort:    m.sortBy.label(),
		// sortDesc is always false when sortBy == sortNone (the axis-
		// change reset enforces that), so this naturally stays false
		// when there's no sort to reverse.
		SortDesc: m.sortDesc,
		MouseOff: m.mouseOff,
		Layout:   m.layoutPref.label(),
	}
	if m.cursor >= 0 && m.cursor < len(m.visible) {
		st.CursorID = m.visible[m.cursor].ID
	}
	_ = SaveSession(m.sessionPath, st)
}

// restoreCursorFromSession moves the cursor to the row matching the
// session-restored pendingCursorID, once. Called after the visible
// set is (re)built on the first fetch / cache seed. Best-effort: if
// the ID is no longer visible (closed, filtered out, deleted) the
// cursor falls back to the top — never a crash. Clears
// pendingCursorID when consume is true so later refresh ticks don't
// keep snapping the cursor back to the saved position; the cache-seed
// caller passes false so the authoritative live fetch still gets to
// run the (idempotent) restore.
func (m *Model) restoreCursorFromSession(consume bool) {
	if m.pendingCursorID == "" {
		return
	}
	m.cursor = 0
	for i, iss := range m.visible {
		if iss.ID == m.pendingCursorID {
			m.cursor = i
			break
		}
	}
	if consume {
		m.pendingCursorID = ""
	}
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

// isTerminalErr reports whether an error is one the auto-refresh tick
// should give up on. These don't self-heal mid-session; the user must
// install bd or move into a workspace and hit `r` to recover.
func isTerminalErr(err error) bool {
	return errors.Is(err, beads.ErrBDNotFound) || errors.Is(err, beads.ErrNoWorkspace)
}

// MouseController is the slice of *tea.Program the model needs to
// switch terminal mouse capture synchronously. ProgramMouse adapts
// the chicken-and-egg construction order (the model exists before
// the program does).
type MouseController interface {
	EnableMouseCellMotion()
	DisableMouse()
}

// ProgramMouse is a late-bound MouseController: main constructs the
// model with an empty one, builds the tea.Program around that model,
// then SetProgram binds it. Calls before binding are no-ops (the
// startup state is handled by the WithMouseCellMotion program option
// instead — see StartWithMouseCapture).
type ProgramMouse struct{ p *tea.Program }

func (pm *ProgramMouse) SetProgram(p *tea.Program) { pm.p = p }
func (pm *ProgramMouse) EnableMouseCellMotion() {
	if pm.p != nil {
		// Deprecated upstream in favor of the startup OPTION — which
		// main does use for launch; this is the runtime half the
		// deprecation note doesn't cover (see DisableMouse below).
		pm.p.EnableMouseCellMotion() //nolint:staticcheck // runtime view-switching, not startup
	}
}
func (pm *ProgramMouse) DisableMouse() {
	if pm.p != nil {
		// Marked deprecated upstream ("the mouse is disabled
		// automatically on exit") but it is the only exported
		// program-level disable, and runtime view-switching is
		// exactly the non-exit use the deprecation note doesn't
		// cover. SGR encoding (1006) stays latched from startup —
		// inert without tracking — so the re-enable resumes
		// SGR-encoded reporting.
		pm.p.DisableMouseCellMotion() //nolint:staticcheck // runtime view-switching, not program exit
	}
}

// WithMouseController wires the capture switch; main passes a
// ProgramMouse it later binds to the program.
func (m Model) WithMouseController(mc MouseController) Model {
	m.mouse = mc
	return m
}

// StartWithMouseCapture reports whether main should start the
// program with tea.WithMouseCellMotion — the launch-time half of
// desiredMouseCapture (the model starts in the list view).
func (m Model) StartWithMouseCapture() bool {
	return m.desiredMouseCapture()
}

// desiredMouseCapture derives whether the terminal mouse should be
// captured right now: never when the user toggled it off (m), and —
// even when on — not on the reading surface. The list and :bd output
// are NAVIGATION surfaces (wheel moves the cursor, click selects a
// row); the detail view — including the prompts and the help overlay
// opened ON TOP of it — is a READING surface where click-drag text
// selection of the runbook is the primary mouse use, so capture
// auto-releases there and re-engages on the way out
// (would-you-kindly-5i0e). Wheel-scrolling the detail body still
// works in most terminals via alternate-scroll (the wheel becomes
// arrow keys while reporting is off).
func (m Model) desiredMouseCapture() bool {
	if m.mouseOff {
		return false
	}
	if m.mode == modeDetail {
		return false
	}
	// Prompts and the help overlay opened ON TOP of the detail view
	// keep rendering the reading surface underneath — flipping
	// capture for the overlay's lifetime would flicker terminal
	// modes mid-read and re-capture text the user may be selecting
	// (the help overlay's own copy calls selecting a keybinding line
	// a plausible click-drag case; roborev #2111).
	switch m.mode {
	case modeConfirmClose, modeDefer, modeNote:
		return m.promptReturn != modeDetail
	case modeHelp:
		return m.helpReturnMode != modeDetail
	}
	return true
}

// Update wraps the real handler (update) to keep terminal mouse
// capture in sync with the mode, CENTRALLY: per-transition
// Enable/Disable cmds scattered across every mode change is the
// drift pattern that ships missed paths, so the one wrapper derives
// the desired state before and after each message and emits the
// switch only on a boundary crossing.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m.desiredMouseCapture()
	nm, cmd := m.update(msg)
	next, ok := nm.(Model)
	if !ok {
		return nm, cmd
	}
	// Split layout upkeep, centrally for the same reason as the mouse
	// capture: the pane follows the cursor no matter which handler
	// moved it, and the viewport's stored size tracks whatever the
	// message did to the chrome.
	if fc := next.followCursor(); fc != nil {
		cmd = tea.Batch(cmd, fc)
	}
	next.syncDetailViewport()
	after := next.desiredMouseCapture()
	if next.mouse != nil {
		switch {
		case after && !before:
			next.mouse.EnableMouseCellMotion()
		case before && !after:
			next.mouse.DisableMouse()
		}
	}
	return next, cmd
}

// update is the main event router; the exported Update wraps it to
// keep terminal mouse capture in sync with the mode.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		// The detail viewport is sized by syncDetailViewport (pane
		// rectangle when the split composition is up, full-screen
		// budget otherwise) — the Update wrapper runs it after every
		// message, this included.
		m.syncDetailViewport()
		// The :bd output overlay uses the same chrome budget as
		// the detail view (header + footer line); a separate
		// chrome const would be over-engineering for a single
		// extra row.
		m.outputVP.Width = msg.Width
		m.outputVP.Height = bodyH
		// Resize the note textarea so a long line wraps inside
		// the terminal width. Height stays at its default (6
		// rows) so the textarea doesn't crowd the underlying
		// row list while in modeNote.
		m.noteArea.SetWidth(msg.Width)
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
		var cacheCmd, depsCmd tea.Cmd
		if msg.err == nil {
			m.all = msg.issues
			// Remember this preset's result so a later switch back to
			// it paints instantly from cache (see switchPreset). CLONED
			// synchronously, here in Update: the optimistic write paths
			// mutate m.all in place (the close path shifts elements out
			// of the backing array), so anything aliasing msg.issues
			// would corrupt — the cache entry on its next instant paint
			// (roborev #2062), and the async cache save below in a
			// straight data race that could PERSIST the corruption into
			// the next launch's warm-start (roborev #2063). One clone
			// serves both; cache and save only read it.
			snap := cloneIssues(msg.issues)
			if m.presetCache == nil {
				m.presetCache = make(map[filter.Preset][]beads.Issue)
			}
			m.presetCache[msg.preset] = snap
			m.commonPrefix = commonIDPrefix(m.all)
			// Freshen cached detail-view dependency rows from the new
			// list so a status that drifted (an external write, or a
			// prior mutation) doesn't linger stale under an issue's
			// deps sections (would-you-kindly-1ym).
			m.refreshDepCachesFromList()
			m.recomputeVisible()
			// Drop marks whose row vanished on this refetch (e.g. after a
			// partial-failure bulk action) so a later bulk op can't target
			// a gone row (would-you-kindly-g00n).
			m.pruneStaleMarks()
			// A refresh that introduces new rows while the deps sort is
			// active leaves those IDs uncached, so depsFullyResolved
			// fails and recomputeVisible silently falls back to the
			// DependencyCount proxy. Re-kick resolution here so the real
			// topological order is restored once the new edges land —
			// without this the order quietly degrades on every refresh
			// until the user re-presses `s`. nil (not deps-sort, or all
			// rows already cached) batches harmlessly.
			depsCmd = m.maybeResolveDeps()
			// First successful fetch is where a session-restored
			// cursor lands: the visible set finally exists, so move
			// the cursor onto the saved issue (consume so refresh
			// ticks don't keep yanking it back).
			m.restoreCursorFromSession(true)
			// New row count → cursor may now sit outside the
			// viewport, or m.scroll may exceed maxScroll.
			m.ensureCursorVisible()
			// Live data has landed; the cache seed (if any) is
			// no longer the source of truth on screen.
			m.cacheStale = false
			// Persist the snapshot so the NEXT wyk launch can
			// warm-start. Dispatched as a tea.Cmd so the fsync +
			// rename run off the Bubble Tea event loop — inline
			// would stutter input handling on slow disks, which
			// would defeat the warm-start latency win.
			if m.cachePath != "" {
				cacheCmd = saveCacheCmd(m.cachePath, string(m.preset), m.cacheScope, snap)
			}
		}
		// Per-sub fetch errors travel on the msg itself so they
		// always reflect THIS fetch — not a concurrent one that
		// happened to win the race for shared state. Always
		// assigned so a partial-failure → total-failure transition
		// clears the per-sub banner cleanly.
		m.fetchErrors = msg.subErrs
		if recovered {
			m.tickGen++
			return m, tea.Batch(tickCmd(m.tickGen), cacheCmd, depsCmd)
		}
		return m, tea.Batch(cacheCmd, depsCmd)

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
		// Coalesce ticks: if a fetch is already in flight (initial
		// load, a manual `r`, or a previous tick whose fetch hasn't
		// returned yet), don't pile on a second overlapping fetch —
		// just reschedule the next tick. Overlapping fetches were a
		// structural cause of the self-sustaining timeouts: a refresh
		// that ran long would be lapped by the next tick, doubling the
		// cold-start load on the embedded-Dolt engines right when they
		// were already struggling. The next tick re-checks and fetches
		// once the in-flight one has cleared (fetchedMsg resets both
		// flags). Manual `r` is unaffected — it forces its own fetch
		// and restarts the chain (see manualRefresh).
		if m.loading || m.refreshing {
			return m, tickCmd(m.tickGen)
		}
		m.refreshing = true
		return m, tea.Batch(m.fetchCmd(), tickCmd(m.tickGen))

	case fsEventMsg:
		// External bd write — refetch AND re-arm the watcher wait so
		// the next event still arrives. We don't bump tickGen; the
		// poll timer keeps running as fallback.
		// Terminal-error suspension still applies: no point
		// refetching when there's no source to query.
		if isTerminalErr(m.lastErr) {
			return m, waitFSEvent(m.fsEvents)
		}
		// Coalesce against any in-flight fetch, exactly like the tick
		// path. Without this, a chatty watcher (sustained .beads/
		// churn fires an event every debounce window) dispatches
		// overlapping cross-repo fetches — the visible "constant rapid
		// refresh" — and each overlap re-thrashes the cold Dolt
		// engines, which is the same self-sustaining timeout storm the
		// tick guard exists to prevent. Dropping the event here is
		// safe: the wait is re-armed below, so the next change after
		// this fetch finishes re-triggers, and the poll tick is the
		// backstop. Setting m.refreshing also lets the tick path see
		// this fetch as in-flight so the two can't double up.
		if m.loading || m.refreshing {
			return m, waitFSEvent(m.fsEvents)
		}
		// Rate-limit fs-driven refreshes to one per minFSRefreshGap.
		// A single bd write emits ~130 fsnotify events and a chatty
		// writer streams many writes; without this floor every write
		// drives a full cross-repo refetch back-to-back. The first
		// event after a quiet spell still refreshes near-instantly
		// (gap already elapsed); a burst collapses to one refresh, and
		// the poll tick catches whatever lands mid-cooldown. Measured
		// from the last fetch's completion so a slow fetch gets a real
		// idle window after it.
		if !m.lastSync.IsZero() && time.Since(m.lastSync) < minFSRefreshGap {
			return m, waitFSEvent(m.fsEvents)
		}
		m.refreshing = true
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
		if msg.note != "" {
			b.WriteString(msg.note)
			b.WriteString("\n\n")
		}
		if len(msg.out) > 0 {
			// bd's stdout can echo issue content (titles/descriptions);
			// strip terminal escapes before showing it (would-you-kindly-waub).
			b.WriteString(sanitizeBlock(string(msg.out)))
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
			// Direct write, NOT setStatus: this IS the clear the
			// generation counter guards. Bumping the gen here would
			// invalidate nothing and cost an ensureCursorVisible on
			// every expiring banner.
			m.status = ""
		}
		return m, nil

	case detailMsg:
		// Late-arriving Detail result. Only adopt it if the user
		// is still looking at the same issue — otherwise the
		// notes would attach to the wrong row.
		if m.detailShown() && msg.err == nil && msg.issue.ID == m.detailIssue.ID {
			// Detail() shells `bd show`, which does NOT run the
			// HUMAN-BLOCK dep-scan that Fetch does — so the enriched
			// issue has BlockedByHuman=false. Adopting it verbatim made
			// the header badge flicker HUMAN-BLOCK → AGENT the instant
			// the enrichment landed. Carry the flag forward from the
			// row we opened (which Fetch already stamped) so the badge
			// stays stable and correct.
			enriched := msg.issue
			enriched.BlockedByHuman = m.detailIssue.BlockedByHuman
			m.detailIssue = enriched
			// Re-seed the viewport now that notes have arrived.
			// Preserve scroll offset: a user who'd already paged
			// to line 40 shouldn't be yanked back to the top.
			prev := m.detailVP.YOffset
			m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
			m.detailVP.SetYOffset(prev)
		}
		return m, nil

	case depsResolvedMsg:
		// Merge freshly-resolved edges (and failures) into the
		// caches, then re-sort if the deps sort is still active so
		// the view switches from the count proxy to the real
		// topological order. A late batch that arrives after the
		// user has moved off the deps sort still updates the cache
		// (cheap, and warms a future re-entry) but skips the
		// re-sort.
		if m.depCache == nil {
			m.depCache = map[string][]beads.Issue{}
		}
		if m.depErr == nil {
			m.depErr = map[string]bool{}
		}
		for id, d := range msg.deps {
			m.depCache[id] = d
		}
		for _, id := range msg.failed {
			m.depErr[id] = true
			// Treat an errored ID as resolved-with-no-deps for the
			// sort: cache an empty edge set so depsFullyResolved can
			// see the whole visible set as covered and switch to the
			// real topo order (in-degree 0 for the failed node).
			if _, ok := m.depCache[id]; !ok {
				m.depCache[id] = nil
			}
		}
		// Reverse-direction merge (detail-entry path only; the
		// deps-sort sender leaves these nil, so the loops no-op).
		if m.dependentCache == nil {
			m.dependentCache = map[string][]beads.Issue{}
		}
		if m.dependentErr == nil {
			m.dependentErr = map[string]bool{}
		}
		for id, d := range msg.dependents {
			m.dependentCache[id] = d
		}
		for _, id := range msg.dependentsFailed {
			m.dependentErr[id] = true
		}
		if m.sortBy == sortDeps {
			m.recomputeVisible()
			m.ensureCursorVisible()
		}
		// If the resolved data is for the issue currently on the
		// detail view, re-seed the body so the dependency sections
		// fill in (preserving scroll). The deps-sort path also lands
		// here but won't be in modeDetail, so this is a no-op there.
		if m.detailShown() && m.detailIssue.ID != "" {
			id := m.detailIssue.ID
			_, gotDeps := msg.deps[id]
			_, gotDependents := msg.dependents[id]
			failedDeps := slices.Contains(msg.failed, id)
			failedDependents := slices.Contains(msg.dependentsFailed, id)
			if gotDeps || gotDependents || failedDeps || failedDependents {
				prev := m.detailVP.YOffset
				m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
				m.detailVP.SetYOffset(prev)
			}
		}
		return m, nil

	case detailFollowMsg:
		return m.handleDetailFollow(msg)

	case tea.MouseMsg:
		// In the split composition the pointer's pane decides, not
		// the mode.
		if m.splitView() {
			return m.handleSplitMouse(msg)
		}
		// Mouse routes by mode (only reachable while capture is on —
		// the `m` toggle releases it):
		// - modeList: wheel moves the cursor; left-click lands it
		// - modeDetail / modeOutput: wheel scrolls the viewport
		// - other modes (help, modals, prompts): keyboard-focused,
		//   the event is dropped
		switch m.mode {
		case modeList:
			return m.handleMouse(msg, m.width, true)
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
			// Direct write: clearing a banner on the next keypress has
			// no stale-flash to lose, and this is the hot key path.
			m.status = ""
			return m.updateList(msg)
		}
	}

	// Forward any other message (e.g. textinput's cursor-blink ticks)
	// to the focused textinput while ANY prompt using it is open.
	// Without this the cursor stops blinking after the initial Blink
	// command — and this used to test modeFilter alone, so every other
	// prompt (quick-add, defer, command, assign, label) had a dead
	// cursor (would-you-kindly-6gjb).
	if m.usesTextInput() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// openCursorRow is the Enter action on the list: open the cursor row's
// detail (or, in the split layout, focus the pane when it already shows
// that row). Shared by the keyboard path and a mouse click on the
// already-selected row, so the two can't drift.
func (m Model) openCursorRow() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeDetail
	if m.splitActive() && issueKey(m.detailIssue) == issueKey(m.visible[m.cursor]) {
		// The pane is already showing this row (possibly
		// enriched with notes, possibly scrolled) — ⏎ just
		// moves focus to it. Re-staging the slim row would
		// blank the notes until bd show returned again.
		return m, nil
	}
	// Fresh entry from the list: no link highlighted, and an
	// empty drill-in stack (Back goes straight to the list).
	m.detailLinkIdx = -1
	m.detailStack = nil
	// Stage the slim row immediately so the view renders
	// with title/description from the list, then dispatch
	// a Detail call to enrich with notes asynchronously.
	m.detailIssue = m.visible[m.cursor]
	// Seed the viewport with the body we have now (notes
	// may be empty until the Detail Cmd resolves); reset
	// scroll to the top so a previous detail view's scroll
	// position doesn't bleed in.
	m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
	m.detailVP.GotoTop()
	// Lazily resolve this issue's dependency + dependent edges
	// for the detail view's bottom sections. nil when no
	// DepLister is wired or both directions are already cached
	// (re-opening the same issue is instant). Runs off the
	// event loop so the bd shell-outs don't block input.
	depsCmd := m.resolveDetailDeps(m.detailIssue.ID)
	if d, ok := m.src.(Detailer); ok {
		target := m.detailIssue
		return m, tea.Batch(depsCmd, func() tea.Msg {
			full, err := d.Detail(context.Background(), target)
			return detailMsg{issue: full, err: err}
		})
	}
	return m, depsCmd
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Quit):
		return m.quitNow()
	case keyHit(msg, m.keys.Back):
		// esc in modeList clears an applied / filter first, then
		// the multi-select. The empty-view hint promises "esc to
		// clear the fuzzy filter", and without this the only way
		// to drop a filter was `/`, backspace it away, enter.
		// Marks come second so one esc never wipes both at once.
		// Other esc uses (cancel prompt, return from detail) live
		// in their own mode handlers and don't reach here.
		if m.query != "" {
			m.query = ""
			m.input.SetValue("")
			m.recomputeVisible()
			m.ensureCursorVisible()
			m.setStatus("cleared filter")
			return m, flashClearCmd(m.statusGen)
		}
		// Without a dedicated escape, the only way to drop a
		// botched mark set would be `v` on each row — too punishing.
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
	case keyHit(msg, m.keys.Layout):
		return m.toggleLayout()
	case keyHit(msg, m.keys.Open):
		return m.openCursorRow()
	case keyHit(msg, m.keys.Filter):
		m.mode = modeFilter
		m.input.SetValue(m.query)
		m.input.Focus()
		m.ensureCursorVisible()
		return m, textinput.Blink
	case keyHit(msg, m.keys.Human):
		// `h` toggles the human view: jump in from wherever you are,
		// press again to return to the view you came from (default
		// all).
		if m.preset == filter.PresetHuman {
			target := m.humanReturnPreset
			if target == "" || target == filter.PresetHuman {
				target = filter.PresetAll
			}
			return m.switchPreset(target)
		}
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
	case keyHit(msg, m.keys.Mouse):
		return m.toggleMouse()
	case keyHit(msg, m.keys.Command):
		return m.beginCommand()
	case keyHit(msg, m.keys.PriorityUp):
		return m.bumpPriority(-1)
	case keyHit(msg, m.keys.PriorityDown):
		return m.bumpPriority(+1)
	case keyHit(msg, m.keys.TypeCycle):
		return m.handleTypeCycle()
	case keyHit(msg, m.keys.AssignOwner):
		return m.beginAssign()
	case keyHit(msg, m.keys.Label):
		return m.beginLabel()
	case keyHit(msg, m.keys.Editor):
		return m.beginEdit()
	case keyHit(msg, m.keys.Repeat):
		return m.handleRepeat()
	case keyHit(msg, m.keys.ShowClosed):
		return m.toggleShowClosed()
	case keyHit(msg, m.keys.Columns):
		m.mode = modeColumns
		return m, nil
	case keyHit(msg, m.keys.Yank):
		return m.handleYank()
	case keyHit(msg, m.keys.YankRich):
		return m.handleYankRich()
	case keyHit(msg, m.keys.YankAll):
		return m.handleYankAll()
	case keyHit(msg, m.keys.YankMarkdown):
		return m.handleYankMarkdown()
	case keyHit(msg, m.keys.YankAllMarkdown):
		return m.handleYankAllMarkdown()
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
	// `r` is the user's "I want fresh data NOW" escape hatch — drop any
	// per-repo fetch cache so every repo is re-queried live, even if its
	// .beads mtime looks unchanged (would-you-kindly-jipr).
	if ci, ok := m.src.(cacheInvalidator); ok {
		ci.InvalidateCache()
	}
	cmds := []tea.Cmd{m.fetchCmd()}
	if isTerminalErr(m.lastErr) {
		m.tickGen++
		cmds = append(cmds, tickCmd(m.tickGen))
	}
	return m, tea.Batch(cmds...)
}

func keyHit(msg tea.KeyMsg, b key.Binding) bool {
	return key.Matches(msg, b)
}

// toggleMouse flips mouse capture at runtime. Capture trades the
// terminal's bare click-drag text selection (Shift/Option-click
// still reaches it in most terminals) for wheel/click navigation;
// the user picks per taste and the choice persists via state.json.
func (m Model) toggleMouse() (tea.Model, tea.Cmd) {
	m.mouseOff = !m.mouseOff
	if m.mouseOff {
		m.setStatus("mouse capture off — click-drag selects text; m to re-enable mouse nav")
	} else {
		m.setStatus("mouse capture on — wheel scrolls, click selects; the detail view still releases it for text selection (m to disable)")
	}
	// The terminal write happens through the MouseController in the
	// Update wrapper (the desiredMouseCapture boundary fires on the
	// mouseOff flip) — synchronously, never as a tea.Cmd: batching
	// those cmds was observed live to delay or drop the mode-switch
	// write (PR #24).
	return m, nil
}
