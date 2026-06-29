package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
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
	fs.Usage = subcommandUsage(fs, "activity")
	since := fs.Duration("since", 24*time.Hour, "show issues updated within this duration (e.g. 1h, 24h, 168h)")
	asJSON := fs.Bool("json", false, "emit a structured JSON array instead of the table")
	compact := fs.Bool("compact", false, "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)")
	// -priority mirrors wyk inbox: lower number = more urgent in
	// bd's convention. -1 (default) disables the cap. A user
	// passing -priority 1 sees recent activity on P0 + P1 only.
	maxPriority := fs.Int("priority", -1, "cap rows at priority N or higher (lower number = higher priority; -1 disables)")
	repoName := fs.String("repo", "", "restrict the stream to the registered repo with this name (mutually exclusive with -all)")
	allFlag := fs.Bool("all", false, "query every registered repo, ignoring the configured default scope")
	status := fs.String("status", "all", "filter rows by status: open / closed / all")
	limit := fs.Int("limit", -1, "cap the stream at N rows (after every other filter; -1 disables)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk activity [-since 24h] [-all] [-json] [-priority N] [-repo name] [-status open|closed|all] [-limit N]")
		return 64
	}
	switch *status {
	case "all", "open", "closed":
	default:
		fmt.Fprintln(os.Stderr, "wyk activity: -status must be open, closed, or all")
		return 64
	}
	if *since <= 0 {
		fmt.Fprintln(os.Stderr, "wyk activity: -since must be positive")
		return 64
	}

	repos, err := reposToQuery("", *repoName, *allFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk activity:", err)
		return scopeErrExit(err)
	}
	reg := &registry.Registry{Repos: repos}

	cutoff := time.Now().Add(-*since)
	events, repoErrs := collectActivity(reg, cutoff, *maxPriority, *status, defaultActivityClient)
	events = limitActivityEvents(events, *limit)
	if *asJSON {
		emitActivityJSON(os.Stdout, events, cutoff, repoErrs, *compact)
	} else {
		emitActivityTable(os.Stdout, events, cutoff)
		if len(repoErrs) > 0 {
			fmt.Fprintf(os.Stdout, "\n%d repo(s) failed (stream may be incomplete): %s\n", len(repoErrs), joinRepoErrors(repoErrs))
		}
	}
	// Exit 0 on partial success (events + errors surfaced) — matching
	// inbox/stats; reserve a non-zero exit for a TOTAL failure where
	// every registered repo errored, so a caller can still tell the
	// difference while always getting parseable output.
	if len(reg.Repos) > 0 && len(repoErrs) == len(reg.Repos) {
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
// maxPriority of -1 disables the cap (preserves prior behavior);
// any non-negative value drops issues whose Priority exceeds it.
// statusFilter is "all" (no filter), "open" (drop closed), or
// "closed" (drop everything except closed). The caller is
// expected to have validated the string.
func collectActivity(reg *registry.Registry, cutoff time.Time, maxPriority int, statusFilter string, mk func(dir string) activityClient) ([]activityEvent, []repoError) {
	// Initialize as an empty (non-nil) slice so the JSON shape is
	// always `[]` rather than `null` when the window is empty —
	// downstream tools iterating events don't need a null guard.
	events := make([]activityEvent, 0)
	var repoErrs []repoError
	for _, r := range reg.Repos {
		c := mk(r.Path)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		issues, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			// Capture WHICH repo failed (not just a bool) so the
			// -json envelope and the text footer can name it — a
			// silently-dropped repo means the stream is missing
			// events with no clue why.
			repoErrs = append(repoErrs, repoError{Repo: r.Name, Error: err.Error()})
			continue
		}
		for _, i := range issues {
			if i.UpdatedAt.IsZero() || !i.UpdatedAt.After(cutoff) {
				continue
			}
			if maxPriority >= 0 && i.Priority > maxPriority {
				continue
			}
			if statusFilter == "open" && i.Status == "closed" {
				continue
			}
			if statusFilter == "closed" && i.Status != "closed" {
				continue
			}
			events = append(events, activityEvent{
				Repo:      r.Name,
				ID:        i.ID,
				Title:     i.Title,
				Status:    i.Status,
				UpdatedAt: i.UpdatedAt,
			})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].UpdatedAt.After(events[j].UpdatedAt) })
	return events, repoErrs
}

// limitActivityEvents truncates events to the first `limit`
// entries. collectActivity already sorts newest-first, so the
// head-of-slice cut keeps the newest N. A negative limit (or one
// >= len) returns the input unchanged.
func limitActivityEvents(events []activityEvent, limit int) []activityEvent {
	if limit < 0 || limit >= len(events) {
		return events
	}
	return events[:limit]
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
		// Title/Repo are untrusted bd content printed to a terminal —
		// strip escapes (would-you-kindly-5zlr).
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.UpdatedAt.Format("2006-01-02 15:04"),
			sanitize.Inline(e.Repo),
			e.Status,
			e.ID,
			sanitize.Inline(e.Title),
		)
	}
	_ = tw.Flush()
}

// emitActivityJSON prints the structured stream. Includes the
// cutoff so a downstream consumer can stamp its data feed.
func emitActivityJSON(w io.Writer, events []activityEvent, cutoff time.Time, repoErrs []repoError, compact bool) {
	out := struct {
		Cutoff time.Time       `json:"cutoff"`
		Events []activityEvent `json:"events"`
		Errors []repoError     `json:"errors,omitempty"`
	}{Cutoff: cutoff, Events: events, Errors: repoErrs}
	_ = emitJSON(w, out, compact)
}

// joinRepoErrors renders []repoError as one "repo: err; …" line for
// the text footer. (subError has its own joiner that works off the
// typed error; this one works off the already-stringified repoError.)
func joinRepoErrors(errs []repoError) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		// This footer prints to a terminal across inbox/activity/stats.
		// Repo names and the error string (which carries bd's stderr) are
		// untrusted-adjacent, so strip control bytes like the table rows
		// do (would-you-kindly-5zlr / roborev #1858).
		repo, errStr := sanitize.Inline(e.Repo), sanitize.Inline(e.Error)
		if repo != "" {
			parts = append(parts, repo+": "+errStr)
		} else {
			parts = append(parts, errStr)
		}
	}
	return strings.Join(parts, "; ")
}
