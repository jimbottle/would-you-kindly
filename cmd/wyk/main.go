// wyk (would-you-kindly) is a terminal UI over the bd (beads) issue
// tracker. It surfaces tasks an agent has handed to a human — see
// docs/CONTRACT.md for the convention it follows.
//
// This is the Phase 1 entry point: read-only TUI over real bd JSON.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/tui"
)

func main() {
	dir := flag.String("C", "", "run as if bd had been started in this directory")
	me := flag.String("me", "", "current user, used by the 'mine' preset (default: git user.email or $USER)")
	probe := flag.Bool("probe", false, "non-TTY: print the human-flagged issues and exit (useful in scripts/CI)")
	flag.Parse()

	// Resolve --me lazily so a user supplying --me doesn't pay the cost
	// of shelling out to git, and so startup doesn't depend on git being
	// on PATH unless the default is actually needed.
	if *me == "" {
		*me = defaultMe()
	}

	client := beads.NewClient()
	client.Dir = *dir
	src := &tui.BDSource{Client: client, Me: *me}

	if *probe {
		os.Exit(runProbe(src))
	}

	p := tea.NewProgram(tui.New(src), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}
}

// runProbe fetches the human preset and prints a one-line summary
// per issue. Returns the process exit code: 0 on success (any count),
// 2 if bd is missing or there's no workspace, 1 on other errors.
func runProbe(src *tui.BDSource) int {
	issues, err := src.Fetch(context.Background(), filter.PresetHuman)
	if err != nil {
		switch {
		case errors.Is(err, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(err, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "wyk:", err)
			return 1
		}
	}
	fmt.Printf("%d issue(s) flagged for human:\n", len(issues))
	for _, i := range issues {
		fmt.Printf("  %-24s P%d  %s\n", i.ID, i.Priority, i.Title)
	}
	return 0
}

// defaultMe resolves the current identity the way bd itself does:
// prefer git's configured user.email, then $USER. Empty string is a
// fine fallback — the "mine" preset degrades to "all open" when the
// identity is unknown.
func defaultMe() string {
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return os.Getenv("USER")
}
