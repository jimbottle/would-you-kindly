// wyk (would-you-kindly) is a terminal UI over the bd (beads) issue
// tracker. It surfaces tasks an agent has handed to a human — see
// docs/CONTRACT.md for the convention it follows.
//
// Modes:
//   wyk                      TUI (default)
//   wyk --probe              non-TTY one-shot listing the human-flagged issues
//   wyk handoff <id>         hand <id> back to a human; runbook read from stdin
//   wyk init                 install the post-commit auto-close hook
//   wyk hook post-commit     called by the installed hook; closes referenced issues
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/tui"
	"github.com/jimbottle/would-you-kindly/pkg/handoff"
)

func main() {
	// Subcommand dispatch happens before flag.Parse so each subcommand
	// can own its own FlagSet without interfering with the top-level
	// flags. The TUI/probe path keeps the existing flat flag layout.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "handoff":
			os.Exit(runHandoff(os.Args[2:]))
		case "init":
			os.Exit(runInit(os.Args[2:]))
		case "hook":
			os.Exit(runHook(os.Args[2:]))
		}
	}

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

	src, err := buildSource(*dir, *me)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}

	if *probe {
		os.Exit(runProbe(src))
	}

	p := tea.NewProgram(tui.New(src), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}
}

// buildSource picks single-repo vs multi-repo wiring based on the
// flags and the registry state:
//
//   - -C <dir>: explicit single-repo, scoped to that workspace.
//   - registry has 2+ repos: multi-repo source.
//   - registry has 1 repo: single-repo source against that repo
//     (NOT cwd) — a user who registered one project then runs `wyk`
//     from anywhere should land in that project, not get an opaque
//     "no workspace here" failure.
//   - registry is empty: single-repo against cwd, the v0.1.0
//     fallback so a user who hasn't run `wyk init` anywhere still
//     gets a working TUI from inside a bd repo.
func buildSource(dir, me string) (tui.Source, error) {
	if dir != "" {
		c := beads.NewClient()
		c.Dir = dir
		return &tui.BDSource{Client: c, Me: me}, nil
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return nil, err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, err
	}
	switch len(reg.Repos) {
	case 0:
		// Empty registry: behave like v0.1.0 with the cwd.
		c := beads.NewClient()
		return &tui.BDSource{Client: c, Me: me}, nil
	case 1:
		// Single registered repo: use it (not cwd).
		c := beads.NewClient()
		c.Dir = reg.Repos[0].Path
		return &tui.BDSource{Client: c, Me: me}, nil
	default:
		clients := make([]*beads.Client, len(reg.Repos))
		names := make([]string, len(reg.Repos))
		for i, r := range reg.Repos {
			c := beads.NewClient()
			c.Dir = r.Path
			clients[i] = c
			names[i] = r.Name
		}
		return tui.NewMultiBDSource(clients, names, me)
	}
}

// runHandoff implements `wyk handoff <id>`: read a runbook from stdin
// (or --file), then call pkg/handoff.BounceToHuman against the bd
// CLI client. Designed for invocation from an agent skill — short,
// non-interactive, idempotent.
//
// Exit codes:
//   0   success (also returned for --help, which is a deliberate request)
//   1   generic failure (bd error, IO error, …)
//   2   bd missing or no workspace
//   64  usage error (bad flags / missing args / TTY-stdin without --allow-empty)
func runHandoff(args []string) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	dir := fs.String("C", "", "run as if bd had been started in this directory")
	file := fs.String("file", "", "read the runbook from this file (default: stdin)")
	allowEmpty := fs.Bool("allow-empty", false,
		"permit an empty runbook (clears the issue's description). Required when stdin is a TTY.")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// --help is a successful request; flag printed usage already.
			return 0
		}
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: wyk handoff [-C <dir>] [-file <path>] [-allow-empty] <issue-id>")
		return 64
	}
	id := fs.Arg(0)

	// Reading from a TTY would block waiting for user input — easy to
	// hit by accident when invoked interactively without a redirect.
	// If the user then closes stdin with ^D, we'd silently wipe the
	// issue's description. Refuse unless they opted in. Treat a Stat
	// error as "unknown — refuse" rather than "assume non-TTY", so
	// the guard fails closed in the rare case Stat fails.
	if *file == "" && !*allowEmpty {
		stat, statErr := os.Stdin.Stat()
		isTTY := statErr != nil || (stat.Mode()&os.ModeCharDevice) != 0
		if isTTY {
			fmt.Fprintln(os.Stderr,
				"wyk handoff: stdin is a TTY (or its mode could not be determined). Pipe a runbook in, pass -file <path>, or use -allow-empty to deliberately clear the description.")
			return 64
		}
	}

	var runbookBytes []byte
	var err error
	if *file != "" {
		runbookBytes, err = os.ReadFile(*file)
	} else {
		runbookBytes, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk handoff:", err)
		return 1
	}
	runbook := strings.TrimRight(string(runbookBytes), "\n")
	if runbook == "" && !*allowEmpty {
		fmt.Fprintln(os.Stderr,
			"wyk handoff: empty runbook would clear the description. Pass -allow-empty to confirm.")
		return 64
	}

	client := beads.NewClient()
	client.Dir = *dir
	if err := handoff.BounceToHuman(context.Background(), client, id, runbook); err != nil {
		switch {
		case errors.Is(err, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(err, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "wyk handoff:", err)
			return 1
		}
	}
	fmt.Printf("handed %s to human (%d-byte runbook)\n", id, len(runbook))
	return 0
}

// runProbe fetches the human preset and prints a one-line summary
// per issue. Returns the process exit code: 0 on success (any count),
// 2 if bd is missing or there's no workspace, 1 on other errors.
func runProbe(src tui.Source) int {
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
