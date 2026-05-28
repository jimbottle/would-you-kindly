package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// inboxQuery is the canonical "what's been bounced back to me" query.
// Issues an agent originally filed (`src:agent`) that no longer carry
// the `human` label and aren't closed — the convention is: the human
// removes `human` to say "back to you", and the agent picks the issue
// up from this inbox. See docs/CONTRACT.md.
const inboxQuery = `label=src:agent AND NOT label=human AND status!=closed`

// runInbox implements `wyk inbox`: the agent-side view of the
// handoff loop. Prints issues across every registered workspace
// that have been bounced back by a human. Defaults to a tabular
// human-readable format; --json emits the raw bd issue array
// (decorated with Repo/Branch) for LLM consumption.
//
// Exit codes:
//
//	0   success (any count, including zero — empty inbox is normal)
//	1   filesystem / bd error
//	2   bd missing or no workspace
//	64  usage error
func runInbox(args []string) int {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	dir := fs.String("C", "", "scope to a single workspace; default is every registered repo")
	asJSON := fs.Bool("json", false, "emit a JSON array of issues for LLM consumption")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk inbox [-C <dir>] [-json]")
		return 64
	}

	subs, code := inboxSubs(*dir)
	if code != 0 {
		return code
	}

	all, firstErr := fetchInbox(subs)
	if len(all) == 0 && firstErr != nil {
		// Distinguish the typed bd sentinels so the documented exit
		// codes (2 for bd-missing / no-workspace) actually fire,
		// matching wyk handoff's behavior.
		switch {
		case errors.Is(firstErr, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(firstErr, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "wyk inbox:", firstErr)
			return 1
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(all); err != nil {
			fmt.Fprintln(os.Stderr, "wyk inbox: encode:", err)
			return 1
		}
		return 0
	}
	renderInboxText(all)
	return 0
}

// inboxSub bundles a client with its display name — same shape as
// the multi-repo TUI source but local to the inbox subcommand to
// keep the dependency graph clean. (No branch field today; the inbox
// is repo-scoped, not branch-scoped.)
type inboxSub struct {
	client *beads.Client
	name   string
}

// inboxSubs returns one entry per repo to query. -C overrides the
// registry; an empty registry falls back to cwd (matches the TUI's
// buildSource rules).
func inboxSubs(dir string) ([]inboxSub, int) {
	if dir != "" {
		c := beads.NewClient()
		c.Dir = dir
		return []inboxSub{{client: c, name: ""}}, 0
	}
	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk inbox:", err)
		return nil, 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk inbox:", err)
		return nil, 1
	}
	if len(reg.Repos) == 0 {
		c := beads.NewClient()
		return []inboxSub{{client: c, name: ""}}, 0
	}
	out := make([]inboxSub, len(reg.Repos))
	for i, r := range reg.Repos {
		c := beads.NewClient()
		c.Dir = r.Path
		out[i] = inboxSub{client: c, name: r.Name}
	}
	return out, 0
}

// fetchInbox queries every sub in parallel, decorating each issue
// with its Repo so a JSON consumer can disambiguate cross-repo IDs.
// Returns the first per-sub error only when no sub produced data —
// otherwise partial failures are tolerated, matching MultiBDSource.
func fetchInbox(subs []inboxSub) ([]beads.Issue, error) {
	type result struct {
		issues []beads.Issue
		err    error
	}
	results := make([]result, len(subs))

	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s inboxSub) {
			defer wg.Done()
			issues, err := s.client.Query(context.Background(), inboxQuery)
			results[i] = result{issues: issues, err: err}
		}(i, s)
	}
	wg.Wait()

	var all []beads.Issue
	var firstErr error
	for i, s := range subs {
		r := results[i]
		if r.err != nil {
			if firstErr == nil {
				if s.name != "" {
					firstErr = fmt.Errorf("%s: %w", s.name, r.err)
				} else {
					firstErr = r.err
				}
			}
			continue
		}
		for j := range r.issues {
			r.issues[j].Repo = s.name
		}
		all = append(all, r.issues...)
	}
	return all, firstErr
}

// renderInboxText prints the inbox as a compact list — one line per
// issue, repo-prefixed when multiple workspaces are in scope.
func renderInboxText(all []beads.Issue) {
	if len(all) == 0 {
		fmt.Println("inbox empty (no agent-filed issues currently bounced back).")
		return
	}
	multiRepo := false
	for _, i := range all {
		if i.Repo != "" {
			multiRepo = true
			break
		}
	}
	fmt.Printf("%d issue(s) in inbox:\n", len(all))
	for _, i := range all {
		if multiRepo {
			fmt.Printf("  [%s] %-22s P%d  %s\n", i.Repo, i.ID, i.Priority, i.Title)
		} else {
			fmt.Printf("  %-22s P%d  %s\n", i.ID, i.Priority, i.Title)
		}
	}
}
