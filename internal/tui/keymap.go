package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
)

// Compile-time check that keyMap satisfies bubbles/help's key.Map
// interface so it can drive the help footer + overlay.
var _ help.KeyMap = keyMap{}

// keyMap collects all bindings the TUI responds to. Kept on the
// model so the help line in the status bar can render them.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Top     key.Binding
	Bottom  key.Binding
	Open    key.Binding
	Back    key.Binding
	Filter  key.Binding
	Human   key.Binding
	Cycle   key.Binding
	Refresh key.Binding
	Quit    key.Binding

	// Write actions (Phase 2). These are only honored when the Source
	// also implements Mutator; otherwise they print a "read-only" hint.
	Close       key.Binding // c — close the cursor issue (with confirmation)
	ToggleHuman key.Binding // H — add/remove the 'human' label on the cursor issue
	AddNote     key.Binding // n — append a note to the cursor issue
	QuickAdd    key.Binding // N — file a new issue inline (title-only prompt)

	// Navigation jumps (Phase 3.B.2): bracket through the human-flagged
	// subset of the current view without leaving the active preset.
	JumpNextHuman key.Binding // ] — next human-flagged issue (wraps)
	JumpPrevHuman key.Binding // [ — previous human-flagged issue (wraps)

	// Help overlay (Phase 3.B.3).
	Help key.Binding // ? — modal listing every binding

	// Priority quick-filters. Cap the visible rows at "<= Pn"
	// without dropping into the / fuzzy prompt — the most common
	// triage move ("show me P0/P1 only") becomes one keystroke.
	// FilterPAll clears the cap.
	FilterP0   key.Binding // 1 — only P0 (highest)
	FilterP1   key.Binding // 2 — P0 and P1
	FilterP2   key.Binding // 3 — P0..P2
	FilterP3   key.Binding // 4 — P0..P3
	FilterPAll key.Binding // 0 — clear the priority cap

	// Sort cycling. Cycles through {none, priority, updated, repo,
	// id} and surfaces the active axis BOTH as a chip in the
	// filter strip ('↕ priority') AND as an arrow on the active
	// column header (↑ for asc, ↓ for desc). Lets the user pivot
	// the table for different triage moves without leaving the TUI.
	SortCycle key.Binding // s — advance sort key

	// ShowClosed toggles whether closed issues appear in the
	// current preset. PresetAll switches between `bd list` (open)
	// and `bd list --all`; other presets drop the `status!=closed`
	// clause from their query. A chip surfaces in the filter strip
	// when active.
	ShowClosed key.Binding // C — toggle show-closed

	// Columns opens the column-visibility overlay so a user
	// triaging on a narrow pane can hide secondary columns
	// (owner badge, repo, branch, type, …). Selections persist to
	// ~/.config/wyk/ui.json so the next launch keeps the layout.
	Columns key.Binding // o — column overlay

	// Yank copies the cursor issue's full ID to the system
	// clipboard via OSC 52 (works over SSH and in tmux). Status
	// banner confirms; failures surface as an error so the user
	// doesn't think the copy worked and paste stale content.
	Yank key.Binding // y — copy ID to clipboard

	// Undo reopens the most-recently-closed issue from this
	// session. One-deep only — vim-style single-step undo, not a
	// stack. Cleared once the reopen lands so a second press is a
	// no-op rather than reopening some older row.
	Undo key.Binding // u — undo last close

	// Defer hides the cursor issue from bd ready until a date.
	// Opens a textinput prompt for the offset (+1d, +1w, tomorrow,
	// 2026-06-15 — bd parses); empty submission cancels. Backed by
	// bd's `update --defer` flag so the persistence semantics
	// match `bd ready` exactly.
	Defer key.Binding // d — defer cursor issue

	// Mark toggles the multi-select state on the cursor row. When
	// at least one row is marked, the bulk-capable write keys
	// (c/H/d) operate on every marked row instead of the cursor —
	// matches vim's visual-mode mental model. esc clears all
	// marks.
	Mark key.Binding // v — toggle mark on cursor row
}

func defaultKeyMap() keyMap {
	return keyMap{
		Up:      key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k", "up")),
		Down:    key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "nav")), // ShortHelp uses Down's help string for the combined j/k hint; Up's "k up" still surfaces in the FullHelp overlay.
		Top:     key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top")),
		Bottom:  key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),
		Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Human:   key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "human")),
		Cycle:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "preset")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		Close:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "close")),
		ToggleHuman: key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "±human")),
		AddNote:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "note")),
		QuickAdd:    key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "new issue")),

		JumpNextHuman: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next human")),
		JumpPrevHuman: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "prev human")),

		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),

		FilterP0:   key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "P0 only")),
		FilterP1:   key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "≤P1")),
		FilterP2:   key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "≤P2")),
		FilterP3:   key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "≤P3")),
		FilterPAll: key.NewBinding(key.WithKeys("0"), key.WithHelp("0", "all P")),

		SortCycle:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		ShowClosed: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "±closed")),
		Columns:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "columns")),
		Yank:       key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank ID")),
		Undo:       key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo close")),
		Defer:      key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "defer")),
		Mark:       key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "mark")),
	}
}

// ShortHelp is the one-line "what can I press right now" hint the
// help footer renders. Order matters: the bubbles/help package
// renders them left-to-right with a separator, and clips on the
// right when width is tight — so the most important bindings come
// first. Writes appear here too; if the source is read-only the
// model swaps in shortHelpReadOnly when rendering.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.Down, k.Open, k.Filter, k.Human, k.Cycle, k.Refresh,
		k.Close, k.ToggleHuman, k.AddNote, k.Help, k.Quit,
	}
}

// shortHelpReadOnly is ShortHelp without the write bindings, used
// when no Mutator is wired up so the footer doesn't advertise
// keys that won't do anything (or worse, surface a "read-only
// mode" banner on press).
func (k keyMap) shortHelpReadOnly() []key.Binding {
	return []key.Binding{
		k.Down, k.Open, k.Filter, k.Human, k.Cycle, k.Refresh,
		k.Help, k.Quit,
	}
}

// FullHelp drives the help overlay's grouped view. The model also
// renders its own grouped overlay (viewHelp) for nicer copy
// (section headings, notes), so this is primarily here to satisfy
// the help.KeyMap interface contract — keeping the data source
// unified so a binding change in defaultKeyMap propagates
// automatically.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.Open, k.Back, k.JumpPrevHuman, k.JumpNextHuman},
		{k.Filter, k.Human, k.Cycle, k.FilterP0, k.FilterP1, k.FilterP2, k.FilterP3, k.FilterPAll, k.SortCycle, k.ShowClosed, k.Columns},
		{k.Close, k.ToggleHuman, k.AddNote, k.QuickAdd, k.Yank, k.Undo, k.Defer, k.Mark},
		{k.Refresh, k.Help, k.Quit},
	}
}
