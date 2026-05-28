package tui

import (
	"context"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// BDSource is a Source backed by the bd CLI. It centralises the
// preset → bd-command mapping so the TUI itself stays free of
// command-line semantics.
type BDSource struct {
	Client *beads.Client
	// Me is the current user, used by PresetMine. Empty means
	// "mine" degrades to all open issues.
	Me string
}

// Fetch dispatches to the right bd subcommand for the preset.
func (s *BDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	switch p {
	case filter.PresetReady:
		// bd ready has blocker-aware semantics that bd query cannot
		// reproduce; defer to it.
		return s.Client.Ready(ctx)
	case filter.PresetAll:
		return s.Client.ListAll(ctx)
	default:
		return s.Client.Query(ctx, filter.Query(p, s.Me))
	}
}
