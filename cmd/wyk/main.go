// wyk (would-you-kindly) is a terminal UI over the bd (beads) issue
// tracker. It is designed to surface tasks an agent has handed to a
// human — see docs/CONTRACT.md for the convention it follows.
//
// This is the Phase 1 entry point: read-only TUI over real bd JSON.
// Until the bd client is wired in, the binary renders a small static
// fixture so the UI can be iterated on without a live database.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/tui"
)

func main() {
	src := staticSource{}
	p := tea.NewProgram(tui.New(src), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}
}

// staticSource returns a hardcoded fixture so the TUI can render
// before the bd CLI client lands. Will be replaced in the next
// commit by a Source that shells out to `bd query --json`.
type staticSource struct{}

func (staticSource) Fetch(_ filter.Preset) ([]beads.Issue, error) {
	return []beads.Issue{
		{
			ID: "demo-001", Title: "Rotate the staging database password",
			Status: "open", Priority: 1, IssueType: "task",
			Labels: []string{"human", "src:agent"},
			Description: "1. Open 1Password.\n2. Generate a new password.\n3. Update Heroku config.",
		},
		{
			ID: "demo-002", Title: "Approve the v0.3.0 release on GitHub",
			Status: "open", Priority: 2, IssueType: "task",
			Labels: []string{"human", "src:agent"},
			Description: "Review the release notes and click Publish.",
		},
		{
			ID: "demo-003", Title: "Implement the bd client",
			Status: "in_progress", Priority: 1, IssueType: "feature",
			Labels: []string{"src:agent"},
			Description: "Wire internal/beads to shell out to the bd CLI.",
		},
	}, nil
}
