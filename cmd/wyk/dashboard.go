package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// runDashboard walks every registered bd workspace and emits a
// one-screen weekly summary: per-repo counts (open / human /
// closed-last-7-days) plus a totals line. `-json` produces the
// same data as a structured object for external tooling. Useful
// at end-of-week / start-of-week retrospectives across a polyglot
// project set.
//
// Exit codes:
//
//	0  summary printed
//	1  registry / per-repo I/O error (partial output still emitted)
//	64 usage error
func runDashboard(args []string) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit a structured JSON object instead of the table")
	days := fs.Int("days", 7, "window for the closed-recently column (default 7)")
	repoName := fs.String("repo", "", "restrict the rollup to the registered repo with this name (empty = every registered repo)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk dashboard [-json] [-days N] [-repo name]")
		return 64
	}
	if *days <= 0 {
		fmt.Fprintln(os.Stderr, "wyk dashboard: -days must be positive")
		return 64
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk dashboard:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk dashboard: load registry:", err)
		return 1
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk dashboard: no repos registered. Run `wyk init` in a bd workspace first.")
		return 1
	}

	if *repoName != "" {
		filtered, err := filterRegistryByName(reg, *repoName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk dashboard:", err)
			return 1
		}
		reg = filtered
	}

	cutoff := time.Now().Add(-time.Duration(*days) * 24 * time.Hour)
	rows, hadError := collectDashboard(reg, cutoff)

	if *asJSON {
		emitDashboardJSON(os.Stdout, rows, *days, cutoff)
	} else {
		emitDashboardTable(os.Stdout, rows, *days)
	}
	if hadError {
		return 1
	}
	return 0
}

// dashboardRow captures the aggregate counts for a single repo.
// Sortable by name (the rendering path sorts alphabetically so
// the output is deterministic).
type dashboardRow struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Open           int    `json:"open"`
	Human          int    `json:"human"`
	ClosedInWindow int    `json:"closed_in_window"`
	Err            string `json:"error,omitempty"`
}

// collectDashboard walks the registry sequentially. Sequential
// (not parallel) keeps the bd subprocess fanout matched to the
// TUI's MultiBDSource.HUMAN-BLOCK semaphore — running 10
// concurrent `bd list --all` calls on a busy laptop just heats
// the CPU. Per-repo errors are recorded on the row but don't
// abort the walk; we'd rather emit a partial dashboard than
// nothing.
func collectDashboard(reg *registry.Registry, cutoff time.Time) ([]dashboardRow, bool) {
	rows := make([]dashboardRow, 0, len(reg.Repos))
	hadError := false
	for _, r := range reg.Repos {
		row := dashboardRow{Name: r.Name, Path: r.Path}
		c := beads.NewClient()
		c.Dir = r.Path
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		issues, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			row.Err = err.Error()
			hadError = true
			rows = append(rows, row)
			continue
		}
		row.Open, row.Human, row.ClosedInWindow = tallyIssues(issues, cutoff)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, hadError
}

// tallyIssues classifies a slice of bd Issues into the three
// dashboard buckets. Extracted as a pure function so the
// counting branches — open vs. closed, the human-label flag,
// and the closed-in-window boundary — can be unit-tested
// without a real bd binary. Uses beads.Issue.IsHuman() to
// match the convention used everywhere else in the codebase
// (rather than re-implementing a Labels scan).
//
// A closed issue with a zero ClosedAt is dropped silently —
// shouldn't happen in practice (bd sets ClosedAt on every
// close), but a clock-skew or migration artifact shouldn't
// pollute the recent-closes count.
func tallyIssues(issues []beads.Issue, cutoff time.Time) (open, human, closedInWindow int) {
	for _, i := range issues {
		if i.Status != "closed" {
			open++
			if i.IsHuman() {
				human++
			}
		} else if !i.ClosedAt.IsZero() && i.ClosedAt.After(cutoff) {
			closedInWindow++
		}
	}
	return open, human, closedInWindow
}

// emitDashboardTable prints the human-facing summary. tabwriter
// aligns the four columns regardless of name length; a totals row
// follows the per-repo rows. Errors render inline (as a per-row
// suffix) so a single broken repo doesn't make its row vanish.
func emitDashboardTable(w io.Writer, rows []dashboardRow, days int) {
	fmt.Fprintf(w, "wyk dashboard — week of %s\n\n", time.Now().Format("2006-01-02"))
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintf(tw, "REPO\tOPEN\tHUMAN\tCLOSED ↓ %dd\n", days)
	var totOpen, totHuman, totClosed int
	for _, r := range rows {
		if r.Err != "" {
			// Keep the cell count consistent with normal rows
			// (4 tab-separated cells) so tabwriter aligns the
			// columns the same way; fold the error message into
			// the trailing cell so the CLOSED column doesn't
			// drift between errored and non-errored rows.
			fmt.Fprintf(tw, "%s\t—\t—\t— (%s)\n", r.Name, r.Err)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", r.Name, r.Open, r.Human, r.ClosedInWindow)
		totOpen += r.Open
		totHuman += r.Human
		totClosed += r.ClosedInWindow
	}
	fmt.Fprintf(tw, "\nTOTAL\t%d\t%d\t%d\n", totOpen, totHuman, totClosed)
	_ = tw.Flush()
}

// emitDashboardJSON prints the structured form for external
// tooling. Includes the window metadata so a downstream dashboard
// can render the cutoff alongside the counts.
func emitDashboardJSON(w io.Writer, rows []dashboardRow, days int, cutoff time.Time) {
	out := struct {
		WindowDays   int            `json:"window_days"`
		WindowCutoff time.Time      `json:"window_cutoff"`
		Repos        []dashboardRow `json:"repos"`
	}{
		WindowDays:   days,
		WindowCutoff: cutoff,
		Repos:        rows,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
