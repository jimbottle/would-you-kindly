package tui

import (
	"context"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// BDSource is a Source backed by the bd CLI. It centralises the
// preset → bd-command mapping so the TUI itself stays free of
// command-line semantics. It also satisfies Mutator so the write
// keystrokes (c / H / n) dispatch through it.
type BDSource struct {
	Client *beads.Client
	// Me is the current user, used by PresetMine. Empty means
	// "mine" degrades to all open issues.
	Me string
}

// Compile-time check that BDSource satisfies both interfaces.
var (
	_ Source  = (*BDSource)(nil)
	_ Mutator = (*BDSource)(nil)
)

// Fetch dispatches to the right bd subcommand for the preset.
func (s *BDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	switch p {
	case filter.PresetReady:
		// bd ready has blocker-aware semantics that bd query cannot
		// reproduce; defer to it.
		return s.Client.Ready(ctx)
	case filter.PresetAll:
		// "all" in the TUI means "all non-closed" — opening wyk
		// should show actionable work, not the full history.
		return s.Client.List(ctx)
	default:
		return s.Client.Query(ctx, filter.Query(p, s.Me))
	}
}

// --- Mutator implementation ----------------------------------------
// BDSource also satisfies the Mutator interface by delegating to the
// underlying Client. Keeping reads and writes on the same struct
// matches how callers wire it up in cmd/wyk.

func (s *BDSource) Close(ctx context.Context, id string) error {
	return s.Client.Close(ctx, id)
}

func (s *BDSource) AddLabel(ctx context.Context, id, label string) error {
	return s.Client.AddLabel(ctx, id, label)
}

func (s *BDSource) RemoveLabel(ctx context.Context, id, label string) error {
	return s.Client.RemoveLabel(ctx, id, label)
}

func (s *BDSource) Note(ctx context.Context, id, text string) error {
	return s.Client.Note(ctx, id, text)
}
