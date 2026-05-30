package tui

import (
	"strings"

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

	// YankRich copies "ID — title" (em-dash separator) so a
	// reference pasted into a commit message or chat reads
	// naturally without re-typing the title. Same OSC 52 path
	// as Yank.
	YankRich key.Binding // Y — copy "ID — title"

	// YankAll copies every currently-visible issue ID (post-filter,
	// post-preset) to the clipboard, newline-separated. Useful for
	// piping a filtered set of IDs into a shell loop or sharing the
	// matching set with another agent run.
	YankAll key.Binding // * — copy every visible ID

	// YankMarkdown copies the cursor row as a markdown task line:
	// "- [ ] <ID> — <title>" for open rows, "- [x] ..." for closed.
	// Useful for assembling a markdown report from filtered TUI
	// rows without retyping IDs and titles.
	YankMarkdown key.Binding // M — copy as markdown task line

	// YankAllMarkdown copies every visible row as a newline-joined
	// markdown task list — the multi-row sibling of YankMarkdown,
	// useful for turning a filtered TUI view into a progress
	// report or todo block.
	YankAllMarkdown key.Binding // _ — copy all visible rows as markdown

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

	// SortReverse flips the active sort's direction without
	// re-cycling the axis. Priority asc ↔ desc, updated desc ↔
	// asc, etc. No-op when no sort axis is active (sortNone has
	// no direction).
	SortReverse key.Binding // S — reverse sort direction

	// Command opens the vim-style ":" command palette. Built-in
	// commands cover the long tail of bindings we don't want a
	// dedicated key for (refresh, preset switch, filter save,
	// etc.). Unknown commands surface a status banner with the
	// list of known names.
	Command key.Binding // : — command palette

	// PriorityUp / PriorityDown bump the cursor issue's priority
	// by one step. `+` is "more urgent" (priority--), `-` is "less
	// urgent" (priority++). Clamped to bd's 0–4 range. When marks
	// are present, the bulk path applies the same single-step
	// nudge to every marked row (it's relative, not absolute, so
	// mixed priorities stay distinguishable).
	PriorityUp   key.Binding // + — more urgent
	PriorityDown key.Binding // - — less urgent

	// TypeCycle rotates the cursor issue's IssueType through the
	// bd-accepted values (task / bug / feature / chore / epic /
	// decision / spike / story / milestone), wrapping at the end.
	// Same shape as 's' (sort cycle): a single keystroke maps to
	// the next-in-sequence write.
	TypeCycle key.Binding // T — cycle issue type

	// AssignOwner opens a textinput prompt for a new assignee
	// (bd's --assignee). Empty submission clears the owner. Bulk-
	// aware: with marks present, the value applies to every
	// marked row.
	AssignOwner key.Binding // O — change owner

	// Label opens a textinput prompt for a label name; on submit,
	// the cursor row toggles that label (add if absent, remove if
	// present) — mirrors how H toggles the `human` label
	// specifically. Empty submission cancels. Bulk path is add-
	// only (matches H's bulk behavior).
	Label key.Binding // L — toggle an arbitrary label

	// Editor suspends the TUI (tea.ExecProcess), opens the cursor
	// issue's description in $EDITOR (fallback `vi`), and on
	// return dispatches Mutator.SetDescription if the body
	// changed. Multi-line editing for descriptions that the
	// single-line textinput modes can't handle.
	Editor key.Binding // e — edit description in $EDITOR

	// Repeat re-applies the last write action (close / defer /
	// assign / label / priority / flag) to the cursor row. The
	// model snapshots (action, arg) at each successful dispatch
	// site so `.` can re-fire without re-prompting.
	Repeat key.Binding // . — repeat last write
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

		SortCycle:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		ShowClosed:   key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "±closed")),
		Columns:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "columns")),
		Yank:         key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank ID")),
		YankRich:     key.NewBinding(key.WithKeys("Y"), key.WithHelp("Y", "yank ID — title")),
		YankAll:      key.NewBinding(key.WithKeys("*"), key.WithHelp("*", "yank all visible IDs")),
		YankMarkdown:    key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "yank as markdown task")),
		YankAllMarkdown: key.NewBinding(key.WithKeys("_"), key.WithHelp("_", "yank all as markdown")),
		Undo:         key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo close")),
		Defer:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "defer")),
		Mark:         key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "mark")),
		SortReverse:  key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "reverse sort")),
		Command:      key.NewBinding(key.WithKeys(":"), key.WithHelp(":", "command")),
		PriorityUp:   key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "↑prio")),
		TypeCycle:    key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "cycle type")),
		PriorityDown: key.NewBinding(key.WithKeys("-"), key.WithHelp("-", "↓prio")),
		AssignOwner:  key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "owner")),
		Label:        key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "label")),
		Editor:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Repeat:       key.NewBinding(key.WithKeys("."), key.WithHelp(".", "repeat last")),
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

// HelpGroup is a labeled set of bindings — the documented unit of
// the keymap. Exported so cmd/wyk's `help --markdown` subcommand
// can render the same grouping the TUI's help overlay uses,
// without duplicating the list and silently drifting from the TUI.
type HelpGroup struct {
	Title    string
	Bindings []key.Binding
}

// DocsKeymap returns the grouped binding list used by both the
// in-TUI help overlay (viewHelp) and the wyk help --markdown
// reference. Single source of truth for the documented keymap.
func DocsKeymap() []HelpGroup {
	k := defaultKeyMap()
	return []HelpGroup{
		{"Navigation", []key.Binding{
			k.Up, k.Down, k.Top, k.Bottom, k.Open, k.Back,
			k.JumpPrevHuman, k.JumpNextHuman,
		}},
		{"Filters & sort", []key.Binding{
			k.Filter, k.Human, k.Cycle, k.SortCycle, k.SortReverse,
			k.ShowClosed, k.Columns,
			k.FilterP0, k.FilterP1, k.FilterP2, k.FilterP3, k.FilterPAll,
		}},
		{"Writes", []key.Binding{
			k.Close, k.ToggleHuman, k.AddNote, k.QuickAdd, k.Defer,
			k.AssignOwner, k.Label, k.Editor, k.PriorityUp, k.PriorityDown, k.TypeCycle,
			k.Mark, k.Undo, k.Repeat,
		}},
		{"Clipboard / command", []key.Binding{k.Yank, k.YankRich, k.YankAll, k.YankMarkdown, k.YankAllMarkdown, k.Command}},
		{"Meta", []key.Binding{k.Refresh, k.Help, k.Quit}},
	}
}

// KeymapMarkdown renders DocsKeymap as a markdown document
// (per-group headings followed by a key|action table). cmd/wyk's
// `help --markdown` emits this for README / docs-site inclusion.
func KeymapMarkdown() string {
	var b strings.Builder
	b.WriteString("# wyk keymap\n\n")
	b.WriteString("Generated by `wyk help --markdown`. The single source of truth is\n")
	b.WriteString("internal/tui/keymap.go (`DocsKeymap`).\n\n")
	for _, g := range DocsKeymap() {
		b.WriteString("## ")
		b.WriteString(g.Title)
		b.WriteString("\n\n")
		b.WriteString("| Key | Action |\n")
		b.WriteString("|-----|--------|\n")
		for _, kb := range g.Bindings {
			h := kb.Help()
			b.WriteString("| `")
			b.WriteString(h.Key)
			b.WriteString("` | ")
			b.WriteString(h.Desc)
			b.WriteString(" |\n")
		}
		b.WriteString("\n")
	}
	return b.String()
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
		{k.Filter, k.Human, k.Cycle, k.FilterP0, k.FilterP1, k.FilterP2, k.FilterP3, k.FilterPAll, k.SortCycle, k.SortReverse, k.ShowClosed, k.Columns},
		{k.Close, k.ToggleHuman, k.AddNote, k.QuickAdd, k.Yank, k.YankRich, k.YankAll, k.YankMarkdown, k.YankAllMarkdown, k.Undo, k.Defer, k.Mark, k.PriorityUp, k.PriorityDown, k.TypeCycle, k.AssignOwner, k.Label, k.Editor, k.Repeat},
		{k.Refresh, k.Help, k.Quit, k.Command},
	}
}
