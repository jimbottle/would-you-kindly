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

// runActivity walks every registered bd workspace, gathers
// recently-touched issues (UpdatedAt within -since), and emits a
// chronological merged stream. bd doesn't expose a workspace-wide
// event log, so wyk derives activity from per-issue UpdatedAt —
// a close imperfect proxy that's still useful for "what
// happened today across my projects" digests.
//
// Exit codes:
//
//	0  activity printed
//	1  registry / per-repo I/O error (partial output still emitted)
//	64 usage error
func runActivity(args []string) int {
	fs := flag.NewFlagSet("activity", flag.ContinueOnError)
	since := fs.Duration("since", 24*time.Hour, "show issues updated within this duration (e.g. 1h, 24h, 168h)")
	asJSON := fs.Bool("json", false, "emit a structured JSON array instead of the table")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk activity [-since 24h] [-json]")
		return 64
	}
	if *since <= 0 {
		fmt.Fprintln(os.Stderr, "wyk activity: -since must be positive")
		return 64
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk activity:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk activity: load registry:", err)
		return 1
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk activity: no repos registered. Run `wyk init` in a bd workspace first.")
		return 1
	}

	cutoff := time.Now().Add(-*since)
	events, hadError := collectActivity(reg, cutoff, defaultActivityClient)
	if *asJSON {
		emitActivityJSON(os.Stdout, events, cutoff)
	} else {
		emitActivityTable(os.Stdout, events, cutoff)
	}
	if hadError {
		return 1
	}
	return 0
}

// activityEvent is one entry in the merged stream — a row touched
// inside the window. Carries the originating repo so the user can
// tell which workspace each line came from.
type activityEvent struct {
	Repo      string    `json:"repo"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// activityClient is the optional Client capability collectActivity
// needs. Production wraps the real beads.Client; tests inject a
// stub via defaultActivityClient.
type activityClient interface {
	ListAll(ctx context.Context) ([]beads.Issue, error)
}

// defaultActivityClient is runActivity's production factory.
// Same shape as defaultExportClient so a future refactor can
// unify the two.
var defaultActivityClient = func(dir string) activityClient {
	c := beads.NewClient()
	c.Dir = dir
	return c
}

// collectActivity walks the registry sequentially (matches the
// dashboard / export concurrency policy). Per-repo errors fold
// into the hadError flag but don't abort — the merged stream is
// more useful with one missing repo than not at all.
func collectActivity(reg *registry.Registry, cutoff time.Time, mk func(dir string) activityClient) ([]activityEvent, bool) {
	// Initialize as an empty (non-nil) slice so the JSON shape is
	// always `[]` rather than `null` when the window is empty —
	// downstream tools iterating events don't need a null guard.
	events := make([]activityEvent, 0)
	hadError := false
	for _, r := range reg.Repos {
		c := mk(r.Path)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		issues, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			hadError = true
			continue
		}
		for _, i := range issues {
			if !i.UpdatedAt.IsZero() && i.UpdatedAt.After(cutoff) {
				events = append(events, activityEvent{
					Repo:      r.Name,
					ID:        i.ID,
					Title:     i.Title,
					Status:    i.Status,
					UpdatedAt: i.UpdatedAt,
				})
			}
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].UpdatedAt.After(events[j].UpdatedAt) })
	return events, hadError
}

// emitActivityTable prints the human-facing stream. Each row is
// "time · repo · status · id · title" via tabwriter so the
// repo / id columns align regardless of name length.
func emitActivityTable(w io.Writer, events []activityEvent, cutoff time.Time) {
	fmt.Fprintf(w, "wyk activity — since %s\n\n", cutoff.Format("2006-01-02 15:04"))
	if len(events) == 0 {
		fmt.Fprintln(w, "(nothing touched in the window)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.UpdatedAt.Format("2006-01-02 15:04"),
			e.Repo,
			e.Status,
			e.ID,
			e.Title,
		)
	}
	_ = tw.Flush()
}

// emitActivityJSON prints the structured stream. Includes the
// cutoff so a downstream consumer can stamp its data feed.
func emitActivityJSON(w io.Writer, events []activityEvent, cutoff time.Time) {
	out := struct {
		Cutoff time.Time       `json:"cutoff"`
		Events []activityEvent `json:"events"`
	}{Cutoff: cutoff, Events: events}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
