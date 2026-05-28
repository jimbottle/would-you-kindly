package tui

import "github.com/charmbracelet/bubbles/key"

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

	// Navigation jumps (Phase 3.B.2): bracket through the human-flagged
	// subset of the current view without leaving the active preset.
	JumpNextHuman key.Binding // ] — next human-flagged issue (wraps)
	JumpPrevHuman key.Binding // [ — previous human-flagged issue (wraps)

	// Help overlay (Phase 3.B.3).
	Help key.Binding // ? — modal listing every binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Up:      key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k", "up")),
		Down:    key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j", "down")),
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

		JumpNextHuman: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next human")),
		JumpPrevHuman: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "prev human")),

		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}
