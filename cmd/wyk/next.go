package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/lease"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
)

// Exit code for `wyk next -claim` when the ranked list is empty: nothing
// to claim is a normal, branchable outcome for an agent loop, distinct
// from a failure (1) and from a contended claim (3).
const exitNothingToClaim = 4

// nextSource says which pool a row came from; it is the primary sort key.
type nextSource string

const (
	// sourceMine: I already hold a live lease on it — resume before
	// starting anything new.
	sourceMine nextSource = "mine"
	// sourceInbox: a human bounced it back (the `wyk inbox` set).
	sourceInbox nextSource = "inbox"
	// sourceReady: `bd ready` — open, unblocked, unclaimed.
	sourceReady nextSource = "ready"
	// sourceExpired: in_progress under someone else's LAPSED lease — work
	// an abandoned session left behind, claimable by takeover. Ranked in
	// the same band as ready so an abandoned P0 still beats a fresh P3.
	sourceExpired nextSource = "expired"
)

// sourceRank orders the pools: resume mine, then the human's bounce-backs
// (the round-trip the contract exists for), then fresh ready work and
// lapsed claims together.
func sourceRank(s nextSource) int {
	switch s {
	case sourceMine:
		return 0
	case sourceInbox:
		return 1
	default:
		return 2
	}
}

// nextRow is one ranked candidate: the issue plus what `wyk next`
// concluded about it. The lease is included so a JSON consumer sees
// why a row is "mine" without recomputing the expiry rules.
type nextRow struct {
	beads.Issue
	Source nextSource   `json:"source"`
	Lease  *lease.Lease `json:"lease,omitempty"`
}

// nextResult is the -json envelope.
type nextResult struct {
	Issues   []nextRow `json:"issues"`
	Identity string    `json:"identity"`
	// Claimed is set with -claim: the outcome of claiming the top row.
	Claimed *claimOutcome `json:"claimed,omitempty"`
	// Degraded / Errors mirror the inbox envelope: an empty list with
	// degraded=true must not be read as "nothing to do".
	Degraded bool        `json:"degraded"`
	Errors   []repoError `json:"errors,omitempty"`
}

// nextSub is one workspace's three fetches, kept together so the ranking
// can dedupe within a repo and -claim can find the right client.
type nextFetch struct {
	sub   inboxSub
	inbox []beads.Issue
	ready []beads.Issue
	// inProgress is every claimed issue in the workspace: mine to resume,
	// others' to skip while live and offer for takeover once lapsed.
	inProgress []beads.Issue
	err        error
}

// fetchNext pulls the inbox, ready, and in-progress sets from every
// sub in parallel. A sub whose inbox query fails is reported as a
// sub-error (its ready/mine sets are dropped too, so a repo is either
// fully in or honestly out); a failed ready or mine fetch alone is
// folded into the same error since the ranking would be misleading
// without it.
func fetchNext(ctx context.Context, subs []inboxSub) []nextFetch {
	out := make([]nextFetch, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s inboxSub) {
			defer wg.Done()
			f := nextFetch{sub: s}
			var errs []error
			var err error
			if f.inbox, err = s.client.Query(ctx, inboxQuery); err != nil {
				errs = append(errs, fmt.Errorf("inbox: %w", err))
			}
			if f.ready, err = s.client.Ready(ctx); err != nil {
				errs = append(errs, fmt.Errorf("ready: %w", err))
			}
			if f.inProgress, err = s.client.ListInProgress(ctx); err != nil {
				errs = append(errs, fmt.Errorf("in-progress: %w", err))
			}
			f.err = errors.Join(errs...)
			out[i] = f
		}(i, s)
	}
	wg.Wait()
	return out
}

// rankNext is the pure core: merge the pools into one deduped, ranked
// list, dropping rows that are not this identity's to work —
//
//   - `human`-labelled (a human's move),
//   - `agent-handoff` (fenced for another agent, human-orchestrated),
//   - held by another identity's LIVE lease (the whole point),
//   - closed (defensive: the pools shouldn't contain them).
//
// Order: mine → inbox → ready+expired, then priority (P0 first), then
// repo, id. Rows still assigned to me — live or lapsed — are reported as
// mine (resume); other identities' lapsed claims as expired (takeover).
func rankNext(fetches []nextFetch, me string, now time.Time, ttl time.Duration) (rows []nextRow, subErrs []subError) {
	type key struct{ repo, id string }
	seen := map[key]bool{}
	for _, f := range fetches {
		if f.err != nil {
			subErrs = append(subErrs, subError{repo: f.sub.name, err: f.err})
			continue
		}
		add := func(pool []beads.Issue, src nextSource) {
			for _, i := range pool {
				k := key{f.sub.name, i.ID}
				if seen[k] {
					continue
				}
				if i.Status == "closed" || i.IsHuman() || i.IsAgentHandoff() {
					continue
				}
				l := lease.Of(i, now, ttl)
				if l.State == lease.Live && (me == "" || l.Owner != me) {
					continue
				}
				seen[k] = true
				i.Repo = f.sub.name
				row := nextRow{Issue: i, Source: src}
				if l.State != lease.None {
					ll := l
					row.Lease = &ll
					// Still assigned to me, live or lapsed: resume it. Anyone
					// else's lapsed claim keeps the pool it arrived through;
					// its lease tells the reader it's a takeover.
					if me != "" && l.Owner == me {
						row.Source = sourceMine
					}
				}
				rows = append(rows, row)
			}
		}
		// Mine first so a bounced-back row I hold dedupes as mine.
		var mine, lapsed []beads.Issue
		for _, i := range f.inProgress {
			l := lease.Of(i, now, ttl)
			switch {
			case me != "" && l.Owner == me:
				mine = append(mine, i)
			case l.State == lease.Expired:
				lapsed = append(lapsed, i)
			}
		}
		add(mine, sourceMine)
		add(f.inbox, sourceInbox)
		add(f.ready, sourceReady)
		add(lapsed, sourceExpired)
	}
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := sourceRank(rows[a].Source), sourceRank(rows[b].Source)
		if ra != rb {
			return ra < rb
		}
		if rows[a].Priority != rows[b].Priority {
			return rows[a].Priority < rows[b].Priority
		}
		if rows[a].Repo != rows[b].Repo {
			return rows[a].Repo < rows[b].Repo
		}
		return rows[a].ID < rows[b].ID
	})
	return rows, subErrs
}

// runNext implements `wyk next`: the one command an agent runs to learn
// what to work on in a shared workspace (wyk-contract/v4).
//
// Exit codes: 0 list emitted (possibly empty or partial); 1 total fetch
// failure; 2 bd missing / no workspace; 3 (-claim) the top pick was
// taken by someone else between the read and the claim; 4 (-claim)
// nothing to claim; 64 usage.
func runNext(args []string) int {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	fs.Usage = subcommandUsage(fs, "next")
	cfg := loadConfigBestEffort()
	dir := fs.String("C", "", "scope to a single workspace; default is the configured scope (every registered repo unless default_scope=cwd — see 'wyk config')")
	allFlag := fs.Bool("all", false, "query every registered repo, ignoring the configured default scope")
	repoName := fs.String("repo", "", "restrict to the registered repo with this name (mutually exclusive with -C/-all)")
	identity := fs.String("identity", "", "the agent asking, as identity `name`; falls back to $WYK_AGENT_IDENTITY, then bd's actor ($BEADS_ACTOR / git user.name / $USER). Leases held by this identity count as mine")
	limit := fs.Int("limit", -1, "cap the list at N rows (-1 disables)")
	claim := fs.Bool("claim", false, "atomically claim the top pick (the same lease wyk claim writes); exit 4 when there is nothing to claim")
	asJSON := fs.Bool("json", false, "emit a JSON {issues, degraded, errors, identity, claimed} envelope; each issue carries its lease {owner, until, state}")
	compact := fs.Bool("compact", cfg.CompactJSON, "with -json, emit non-indented JSON")
	slim := fs.Bool("slim", cfg.SlimJSON, "with -json, drop the heavy description/notes bodies")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+usageLine("next"))
		return 64
	}
	me, _, err := resolveClaimIdentity(*identity)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk next:", err)
		return 64
	}
	ttl, err := resolveClaimTTL(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk next:", err)
		return 64
	}
	subs, code := inboxSubs(*dir, *repoName, *allFlag)
	if code != 0 {
		return code
	}

	ctx := context.Background()
	now := time.Now()
	fetches := fetchNext(ctx, subs)
	rows, subErrs := rankNext(fetches, me, now, ttl)
	if code, msg, total := classifyTotalFetchFailure(len(subs), subErrs); total {
		if msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		if code == 1 {
			if *asJSON {
				emitNextJSON(nextResult{Identity: me, Degraded: true, Errors: subErrorsToRepoErrors(subErrs)}, *compact)
			} else {
				fmt.Fprintln(os.Stderr, "wyk next:", joinRepoErrors(subErrorsToRepoErrors(subErrs)))
			}
		}
		return code
	}
	// Per-identity routing (wyk-contract/v3): drop inbox rows routed to a
	// different identity. Ready rows carry no routing, so they pass.
	rows = filterRowsToIdentity(rows, me)
	if *limit >= 0 && *limit < len(rows) {
		rows = rows[:*limit]
	}

	res := nextResult{Issues: rows, Identity: me, Degraded: len(subErrs) > 0, Errors: subErrorsToRepoErrors(subErrs)}
	if res.Issues == nil {
		res.Issues = []nextRow{}
	}

	if *claim {
		if len(rows) == 0 {
			if *asJSON {
				emitNextJSON(res, *compact)
			} else {
				fmt.Println("nothing to claim.")
			}
			return exitNothingToClaim
		}
		top := rows[0]
		sub := subByName(subs, top.Repo)
		opts := claimOpts{me: me, ttl: ttl, branch: currentBranch(sub.client.Dir), now: now}
		out, cerr := claimIssue(ctx, sub.client, top.ID, opts)
		out.Repo = top.Repo
		if cerr != nil {
			fmt.Fprintln(os.Stderr, "wyk next:", cerr)
			var held *errHeldByOther
			if errors.As(cerr, &held) {
				return exitHeldByOther
			}
			return 1
		}
		res.Claimed = &out
		if !*asJSON {
			printClaimOutcome(out, false, false)
		}
	}

	if *asJSON {
		if *slim {
			for i := range res.Issues {
				res.Issues[i].Issue = slimIssue(res.Issues[i].Issue)
			}
		}
		emitNextJSON(res, *compact)
		return 0
	}
	renderNextText(res, now)
	return 0
}

// filterRowsToIdentity applies the v3 routing rule to ranked rows: keep
// rows routed to me or un-routed; drop rows routed to someone else.
func filterRowsToIdentity(rows []nextRow, me string) []nextRow {
	out := rows[:0]
	for _, r := range rows {
		if issueBelongsToIdentity(r.Issue, me) {
			out = append(out, r)
		}
	}
	return out
}

// subByName finds the sub a ranked row came from. Rows are stamped with
// the sub's name in rankNext; the single -C / cwd sub has an empty name,
// which matches the same way.
func subByName(subs []inboxSub, name string) inboxSub {
	for _, s := range subs {
		if s.name == name {
			return s
		}
	}
	return subs[0]
}

func emitNextJSON(res nextResult, compact bool) {
	if res.Issues == nil {
		res.Issues = []nextRow{}
	}
	if err := emitJSON(os.Stdout, res, compact); err != nil {
		fmt.Fprintln(os.Stderr, "wyk next: encode:", err)
	}
}

// renderNextText prints the ranked list, one row per line, with the
// pool it came from and (for rows I hold) the lease remaining.
func renderNextText(res nextResult, now time.Time) {
	if len(res.Issues) == 0 {
		if res.Degraded {
			fmt.Println("nothing to work on from the repos that responded — but some failed, so this may be INCOMPLETE.")
		} else {
			fmt.Println("nothing to work on: inbox empty and no ready, unclaimed issues.")
		}
	} else {
		multiRepo := false
		for _, r := range res.Issues {
			if r.Repo != "" {
				multiRepo = true
				break
			}
		}
		fmt.Printf("%d candidate(s) for %s, best first:\n", len(res.Issues), res.Identity)
		for _, r := range res.Issues {
			tag := string(r.Source)
			if r.Lease != nil && r.Source == sourceMine {
				tag = "mine, " + lease.Remaining(*r.Lease, now)
			}
			if multiRepo {
				fmt.Printf("  [%s] %-22s P%d  %-18s %s\n", sanitize.Inline(r.Repo), r.ID, r.Priority, tag, sanitize.Inline(r.Title))
			} else {
				fmt.Printf("  %-22s P%d  %-18s %s\n", r.ID, r.Priority, tag, sanitize.Inline(r.Title))
			}
		}
	}
	if res.Degraded {
		fmt.Printf("\n%d repo(s) failed (list may be incomplete): %s\n", len(res.Errors), joinRepoErrors(res.Errors))
	}
}
