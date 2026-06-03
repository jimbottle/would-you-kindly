package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// inboxQuery is the canonical "what's been bounced back to me" query.
// Issues an agent originally filed (`src:agent`) that no longer carry
// the `human` label and aren't closed — the convention is: the human
// removes `human` to say "back to you", and the agent picks the issue
// up from this inbox. `agent-handoff` rows are excluded: another agent
// owns them and a human orchestrates, so the "work it" imperative must
// not fire here. Kept in lockstep with cmd/wyk.agentInboxQuery. See
// docs/CONTRACT.md.
const inboxQuery = `label=src:agent AND NOT label=human AND NOT label=agent-handoff AND status!=closed`

// runInbox implements `wyk inbox`: the agent-side view of the
// handoff loop. Prints issues across every registered workspace
// that have been bounced back by a human. Defaults to a tabular
// human-readable format; --json emits a {issues, errors} envelope
// (issues decorated with Repo/Branch) for LLM consumption — the errors
// array names any workspaces that failed so a partial multi-repo
// result is honestly labelled rather than silently truncated.
//
// Exit codes:
//
//	0   success, including a PARTIAL result (some repos failed but at
//	    least one responded — the errors array / text footer says so)
//	1   total failure (every queried repo errored); -json still emits
//	    the envelope with an empty issues array so output stays parseable
//	2   bd missing or no workspace
//	64  usage error
func runInbox(args []string) int {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	dir := fs.String("C", "", "scope to a single workspace; default is every registered repo")
	asJSON := fs.Bool("json", false, "emit a JSON {issues, errors} envelope for LLM consumption (errors names any repos that failed)")
	slim := fs.Bool("slim", false, "drop the heavy description/notes bodies from each issue (with -json; keeps the lightweight metadata)")
	compact := fs.Bool("compact", false, "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)")
	// -priority caps the inbox at priority N or higher (lower N
	// = higher priority in bd's convention). -1 (the default)
	// disables the cap. A user passing -priority 1 gets P0 + P1
	// only — exactly the "what should I attack first" set.
	maxPriority := fs.Int("priority", -1, "cap the inbox at priority N or higher (lower number = higher priority; -1 disables)")
	repoName := fs.String("repo", "", "restrict the inbox to the registered repo with this name (mutually exclusive with -C)")
	limit := fs.Int("limit", -1, "cap the inbox at N rows (after priority/repo filtering; -1 disables)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk inbox [-C <dir>] [-json] [-priority N] [-repo name] [-limit N]")
		return 64
	}
	if *dir != "" && *repoName != "" {
		fmt.Fprintln(os.Stderr, "wyk inbox: -C and -repo are mutually exclusive")
		return 64
	}

	subs, code := inboxSubs(*dir, *repoName)
	if code != 0 {
		return code
	}

	all, subErrs := fetchInbox(subs)
	if len(subErrs) > 0 && len(subErrs) == len(subs) {
		// Total failure = EVERY queried repo errored. Keyed off the
		// error count (not len(all)==0) so a healthy-but-empty repo
		// alongside a failing one is still a partial success — exit 0
		// with the data + errors — matching the activity command.
		// Distinguish the typed bd sentinels so the documented exit
		// codes (2 for bd-missing / no-workspace) actually fire,
		// matching wyk handoff's behavior.
		first := subErrs[0].err
		switch {
		case errors.Is(first, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(first, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		}
		// Other total failures: in -json still emit the envelope (empty
		// issues + the errors array) so an agent gets parseable output
		// rather than nothing; exit 1 to signal the failure.
		if *asJSON {
			emitInboxJSON(nil, subErrs, *compact)
		} else {
			fmt.Fprintln(os.Stderr, "wyk inbox:", joinRepoErrors(subErrorsToRepoErrors(subErrs)))
		}
		return 1
	}

	if *maxPriority >= 0 {
		all = filterByMaxPriority(all, *maxPriority)
	}
	all = limitByPriority(all, *limit)
	if *slim {
		// Drop the heavy bodies; only meaningful in -json mode (the
		// text view never prints description/notes anyway).
		slimIssues(all)
	}

	// Partial success (some repos failed, some succeeded) falls through
	// here: emit what we have AND the errors so the result is honestly
	// labelled incomplete. Exit 0 — the agent got actionable data.
	if *asJSON {
		emitInboxJSON(all, subErrs, *compact)
		return 0
	}
	renderInboxText(all, subErrs)
	return 0
}

// emitInboxJSON writes the inbox envelope ({issues, errors}) to stdout.
// A nil/empty issues slice still renders as `"issues": []` (not null)
// so a consumer can iterate unconditionally.
func emitInboxJSON(all []beads.Issue, subErrs []subError, compact bool) {
	if all == nil {
		all = []beads.Issue{}
	}
	res := inboxResult{Issues: all, Errors: subErrorsToRepoErrors(subErrs)}
	if err := emitJSON(os.Stdout, res, compact); err != nil {
		fmt.Fprintln(os.Stderr, "wyk inbox: encode:", err)
	}
}

// subErrorsToRepoErrors converts the typed-error subError slice to the
// JSON-shaped repoError slice. The single conversion point lets inbox
// and stats share one renderer (joinRepoErrors) and one envelope type
// with activity instead of carrying parallel helpers.
func subErrorsToRepoErrors(subErrs []subError) []repoError {
	out := make([]repoError, 0, len(subErrs))
	for _, e := range subErrs {
		out = append(out, repoError{Repo: e.repo, Error: e.err.Error()})
	}
	return out
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
// registry; -repo selects one entry from the registry by name; an
// empty registry falls back to cwd (matches the TUI's buildSource
// rules). dir and repoName are mutually exclusive — that's
// enforced by the caller.
func inboxSubs(dir, repoName string) ([]inboxSub, int) {
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
	if repoName != "" {
		filtered, err := filterRegistryByName(reg, repoName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk inbox:", err)
			return nil, 1
		}
		reg = filtered
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
// subError pairs a failing repo's name with its error so the inbox
// can report EVERY per-repo failure (not just the first), both for the
// -json `errors` array and for the text footer. The typed err is kept
// so the caller can still classify bd sentinels (ErrBDNotFound /
// ErrNoWorkspace) for the documented exit codes.
type subError struct {
	repo string
	err  error
}

// repoError is the JSON shape of one per-repo failure in the inbox
// envelope: {"repo": "...", "error": "..."}.
type repoError struct {
	Repo  string `json:"repo,omitempty"`
	Error string `json:"error"`
}

// inboxResult is the -json envelope: the gathered issues plus an
// errors array naming any repos that failed. The envelope (rather than
// a bare issue array) lets an agent reading stdout SEE that a result
// is partial and which workspaces it's missing, instead of silently
// receiving an incomplete inbox. errors is omitted when every repo
// responded.
type inboxResult struct {
	Issues []beads.Issue `json:"issues"`
	Errors []repoError   `json:"errors,omitempty"`
}

func fetchInbox(subs []inboxSub) ([]beads.Issue, []subError) {
	issues := make([][]beads.Issue, len(subs))
	errs := make([]error, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s inboxSub) {
			defer wg.Done()
			issues[i], errs[i] = s.client.Query(context.Background(), inboxQuery)
		}(i, s)
	}
	wg.Wait()
	return splitInboxResults(subs, issues, errs)
}

// splitInboxResults is the pure half of fetchInbox: it partitions the
// parallel per-sub (issues, err) results into the merged issue list
// (each row stamped with its repo) and EVERY per-repo error — not just
// the first — preserving sub order. Collecting all errors is what lets
// the envelope honestly name every failed workspace.
func splitInboxResults(subs []inboxSub, issues [][]beads.Issue, errs []error) ([]beads.Issue, []subError) {
	var all []beads.Issue
	var subErrs []subError
	for i, s := range subs {
		if errs[i] != nil {
			subErrs = append(subErrs, subError{repo: s.name, err: errs[i]})
			continue
		}
		for j := range issues[i] {
			issues[i][j].Repo = s.name
		}
		all = append(all, issues[i]...)
	}
	return all, subErrs
}

// filterByMaxPriority keeps only issues at priority <= max. bd
// uses lower numbers for higher priority (P0 most urgent), so
// "cap at N" means "drop anything with priority > N." Splitting
// the filter out keeps the call site flat and tests trivial.
func filterByMaxPriority(issues []beads.Issue, max int) []beads.Issue {
	out := issues[:0]
	for _, i := range issues {
		if i.Priority <= max {
			out = append(out, i)
		}
	}
	return out
}

// limitByPriority returns the top-`limit` issues by Priority (P0
// most urgent) across the input, breaking ties on ID for
// determinism. A negative limit is a no-op (returns the input
// unchanged); a limit >= len(issues) is also a no-op so the
// caller's prior ordering is preserved when no truncation
// would actually happen.
//
// fetchInbox concatenates per-repo results in registry order
// with no global sort, so a naive head-of-slice truncation
// would return "a prefix of repo 1, then repo 2…" rather than
// the highest-priority N. Sorting before truncating addresses
// that.
func limitByPriority(issues []beads.Issue, limit int) []beads.Issue {
	if limit < 0 || limit >= len(issues) {
		return issues
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Priority != issues[j].Priority {
			return issues[i].Priority < issues[j].Priority
		}
		return issues[i].ID < issues[j].ID
	})
	return issues[:limit]
}

// renderInboxText prints the inbox as a compact list — one line per
// issue, repo-prefixed when multiple workspaces are in scope. A
// trailing "N repo(s) failed" line warns when the result is partial so
// the human (like the agent reading the -json errors array) knows the
// list may be incomplete.
func renderInboxText(all []beads.Issue, subErrs []subError) {
	if len(all) == 0 {
		fmt.Println("inbox empty (no agent-filed issues currently bounced back).")
	} else {
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
	if len(subErrs) > 0 {
		fmt.Printf("\n%d repo(s) failed (inbox may be incomplete): %s\n", len(subErrs), joinRepoErrors(subErrorsToRepoErrors(subErrs)))
	}
}
