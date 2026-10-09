package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/lease"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// claimTTLEnvVar overrides the configured claim_ttl for one run — the
// same env-beats-config precedence WYK_DEFAULT_SCOPE / WYK_AGENT_IDENTITY
// follow, so an orchestrator can hand each agent its own TTL.
const claimTTLEnvVar = "WYK_CLAIM_TTL"

// Exit code for a claim refused because another identity holds a live
// lease. Distinct from 1 (bd failure) so an agent loop can branch:
// "pick something else" versus "something is broken".
const exitHeldByOther = 3

// resolveClaimTTL picks the lease TTL: $WYK_CLAIM_TTL, then config.json's
// claim_ttl, then lease.DefaultTTL. A set-but-invalid value is an error
// rather than a silent fallthrough — a typo'd TTL must not quietly make
// every lease two hours.
func resolveClaimTTL(cfg wykconfig.Config) (time.Duration, error) {
	if v := strings.TrimSpace(os.Getenv(claimTTLEnvVar)); v != "" {
		d, err := lease.ParseTTL(v)
		if err != nil {
			return 0, fmt.Errorf("$%s: %w", claimTTLEnvVar, err)
		}
		return d, nil
	}
	if v := strings.TrimSpace(cfg.ClaimTTL); v != "" {
		d, err := lease.ParseTTL(v)
		if err != nil {
			return 0, fmt.Errorf("config claim_ttl: %w", err)
		}
		return d, nil
	}
	return lease.DefaultTTL, nil
}

// gitConfigValue is a swappable seam for `git config <key>` so the
// identity fallback is testable without a git checkout.
var gitConfigValue = func(key string) string {
	out, err := exec.Command("git", "config", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sessionIdentityPrefix heads an identity derived from a Claude Code
// session ID. Recognisable in a badge ("@claude-88ef57f5") as "an agent
// session with no role name", as opposed to a role-named agent.
const sessionIdentityPrefix = "claude-"

// sessionIdentity derives the default agent identity from a Claude Code
// session ID: "claude-" + its first 8 label-safe characters. Every
// concurrent session therefore gets a DISTINCT identity with nothing to
// configure — the gap that made several agents on one machine all claim
// as the same git user (would-you-kindly-2j7b). Empty when no session.
//
// Tying the default to the session is safe here because leases expire:
// a session that ends stops renewing, and its claims lapse back into the
// pool one TTL later. An agent with a role and the context to name
// itself sets $WYK_AGENT_IDENTITY instead, which outranks this.
func sessionIdentity(session string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(session)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == 8 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return sessionIdentityPrefix + b.String()
}

// resolveClaimIdentity is resolveClaimIdentityFor with the session taken
// from $CLAUDE_CODE_SESSION_ID — the right source for every command an
// agent runs from its shell.
func resolveClaimIdentity(flagVal string) (name, source string, err error) {
	return resolveClaimIdentityFor(flagVal, os.Getenv(sessionEnvVar))
}

// resolveClaimIdentityFor is the identity a claim is recorded under:
//
//  1. -identity, then $WYK_AGENT_IDENTITY — an agent with a role names
//     itself ("reviewer", "release-bot"); the name survives restarts.
//  2. the Claude Code session ("claude-<8 chars>") — the default for an
//     ordinary agent session: unique per concurrent agent, zero setup.
//  3. bd's actor chain ($BEADS_ACTOR, git user.name, $USER) — a human at
//     the CLI or TUI, outside any agent session. The same value a bare
//     `bd update --claim` writes as assignee, so wyk and raw-bd claims
//     agree on who "me" is. Not slugified: assignee is free text.
//
// session is passed in rather than read here because the Stop hook gets
// it from its JSON payload, not its environment. Only CLAIMS use the
// session default; inbox routing (resolveIdentity) deliberately does not,
// since a bounce-back routed to a session would be stranded when it ends.
// The source is returned so output can say where the name came from.
func resolveClaimIdentityFor(flagVal, session string) (name, source string, err error) {
	ident, err := resolveIdentity(flagVal)
	if err != nil {
		return "", "", err
	}
	if ident != "" {
		if flagVal != "" {
			return ident, "-identity", nil
		}
		return ident, "$" + identityEnvVar, nil
	}
	if id := sessionIdentity(session); id != "" {
		return id, "Claude session", nil
	}
	for _, c := range []struct{ val, src string }{
		{os.Getenv("BEADS_ACTOR"), "$BEADS_ACTOR"},
		{gitConfigValue("user.name"), "git user.name"},
		{os.Getenv("USER"), "$USER"},
	} {
		if v := strings.TrimSpace(c.val); v != "" {
			return v, c.src, nil
		}
	}
	return "", "", fmt.Errorf("no agent identity: set $%s (or pass -identity) so the claim records who holds it", identityEnvVar)
}

// isActorFallback reports whether an identity source is bd's actor chain
// — the one source several concurrent agents can share.
func isActorFallback(source string) bool {
	switch source {
	case "$BEADS_ACTOR", "git user.name", "$USER":
		return true
	}
	return false
}

// currentBranch is the git branch of dir ("" when not a git checkout or
// detached). Swappable for tests. Recorded on the lease so a human (or
// another agent) can see where the holder's work lives.
var currentBranch = func(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		return ""
	}
	return b
}

// claimClient is the slice of beads.Client the claim state machine
// needs, so tests drive it with a fake.
type claimClient interface {
	Show(ctx context.Context, id string) (beads.Issue, error)
	Claim(ctx context.Context, id, actor string, meta beads.Metadata) error
	Reassign(ctx context.Context, id, actor string, meta beads.Metadata) error
	SetMetadata(ctx context.Context, id string, meta beads.Metadata) error
	Release(ctx context.Context, id, actor string, unsetKeys []string) error
	Note(ctx context.Context, id, text string) error
	SetAssignee(ctx context.Context, id, assignee string) error
}

// claimAction is what claimIssue did.
type claimAction string

const (
	actionClaimed  claimAction = "claimed"
	actionRenewed  claimAction = "renewed"
	actionTookOver claimAction = "took-over"
	actionReleased claimAction = "released"
)

// claimOutcome is the -json shape and the source for the text line.
type claimOutcome struct {
	ID       string      `json:"id"`
	Repo     string      `json:"repo,omitempty"`
	Action   claimAction `json:"action"`
	Owner    string      `json:"owner"`
	Until    time.Time   `json:"until,omitzero"`
	Branch   string      `json:"branch,omitempty"`
	Previous string      `json:"previous_owner,omitempty"`
	// Identity source ("-identity", "$WYK_AGENT_IDENTITY", "git user.name"…)
	// so a surprising owner can be traced to where it came from.
	IdentitySource string `json:"identity_source,omitempty"`
}

// errHeldByOther is returned by claimIssue when a live lease belongs to
// someone else and -force wasn't given. The message names the holder
// and how long they have left.
type errHeldByOther struct {
	id  string
	l   lease.Lease
	now time.Time // the instant the decision was made, so the message agrees with it
}

func (e *errHeldByOther) Error() string {
	// The holder comes from bd metadata / assignee — untrusted text bound
	// for a terminal, so strip escapes (would-you-kindly-5zlr).
	owner := orUnrecorded(e.l.Owner)
	return fmt.Sprintf("%s is held by %s (%s); pick something else, or -force to take it over", e.id, owner, lease.Remaining(e.l, e.now))
}

// claimOpts parameterises one claimIssue run.
type claimOpts struct {
	me     string
	ttl    time.Duration
	branch string
	now    time.Time
	force  bool
	renew  bool // renew-only: refuse rather than claim when not held by me
}

// claimIssue is the lease state machine for one issue:
//
//   - no lease            → bd --claim (atomic; a concurrent claimer loses)
//   - mine (live or lapsed) → renew (metadata-only write, status untouched)
//   - live, someone else  → refuse (errHeldByOther) unless force
//   - expired             → take over: force assignee + stamp, and leave a
//     note naming the lapsed holder so the trail is visible on the issue
//
// The stamp written is the same in every branch: owner, now+ttl, branch.
func claimIssue(ctx context.Context, c claimClient, id string, o claimOpts) (claimOutcome, error) {
	i, err := c.Show(ctx, id)
	if err != nil {
		return claimOutcome{}, err
	}
	if i.Status == "closed" {
		return claimOutcome{}, fmt.Errorf("%s is closed; reopen it before claiming", id)
	}
	until := o.now.Add(o.ttl)
	stamp := lease.Stamp(o.me, until, o.branch)
	out := claimOutcome{ID: id, Owner: o.me, Until: until, Branch: o.branch}
	l := lease.Of(i, o.now, o.ttl)
	// Fences: a `human` issue is the human's move and an `agent-handoff`
	// issue belongs to another agent by human orchestration — neither is
	// ours to check out, whatever bd's assignee says. `wyk next` never
	// offers them; a direct `wyk claim` needs -force. My own existing
	// lease is exempt so renewing it keeps working.
	mine := l.State != lease.None && l.Owner == o.me
	if !o.force && !mine && (i.IsHuman() || i.IsAgentHandoff()) {
		label := "human"
		if !i.IsHuman() {
			label = beads.LabelAgentHandoff
		}
		return out, &errFenced{id: id, label: label}
	}

	switch {
	case l.State != lease.None && l.Owner == o.me:
		// Mine — live, or lapsed with nobody having taken it since. Either
		// way the assignee is still me, so re-stamping IS the resume; going
		// through the takeover path would note "taken over by me from me".
		if err := c.SetMetadata(ctx, id, stamp); err != nil {
			return out, err
		}
		out.Action = actionRenewed
		return out, nil
	case l.State == lease.Live && !o.force:
		return out, &errHeldByOther{id: id, l: l, now: o.now}
	case o.renew:
		return out, fmt.Errorf("%s is not held by %s (lease %s); run `wyk claim %s` to claim it", id, o.me, l.State, id)
	case l.State == lease.Live && o.force, l.State == lease.Expired:
		// Takeover. bd's --claim would refuse (assignee is someone else),
		// so force the assignee; the note keeps the previous holder on the
		// record since the metadata is about to be overwritten.
		if err := c.Reassign(ctx, id, o.me, stamp); err != nil {
			return out, err
		}
		out.Action = actionTookOver
		out.Previous = l.Owner
		note := takeoverNote(l, o.me, o.now)
		if err := c.Note(ctx, id, note); err != nil {
			// The takeover landed; only the trail note failed. Report it
			// without undoing the claim — a retry would not help.
			return out, fmt.Errorf("took over %s but could not record the note: %w", id, err)
		}
		return out, nil
	default:
		return claimUnleased(ctx, c, i, o, stamp, out)
	}
}

// claimUnleased claims an issue nobody holds a lease on. The usual case
// is one atomic `bd --claim`. But under this project's conventions most
// open issues are ASSIGNED at creation (to the human who owns them, or a
// named agent) without being checked out, and bd's --claim refuses any
// issue whose assignee isn't the claimer. Assignment is not a checkout —
// the lease contract's lock is assignee + in_progress — so an open issue
// assigned to someone else is claimable: clear the assignee, then claim.
//
// Concurrency: bd's --claim is the only compare-and-set bd offers. It
// refuses an in_progress issue outright and a foreign assignee, so two
// agents racing for the same issue can never BOTH win the claim. What the
// clear CAN do is land late: A clears and claims, then B's clear (from a
// read taken before A's claim) wipes A's assignee. The lease survives in
// metadata, but `ListInProgressBy(A)` would stop returning the issue, so
// A's heartbeat would never renew it and it would lapse mid-work. So every
// time our clear may have hit someone else's claim — right after it, and
// after a refused claim — repairCleared re-reads and hands the assignee
// back to the lease owner recorded in metadata. Residual gap: a claimer
// using bare `bd --claim` (no wyk stamp) records no owner to restore.
func claimUnleased(ctx context.Context, c claimClient, i beads.Issue, o claimOpts, stamp beads.Metadata, out claimOutcome) (claimOutcome, error) {
	id := i.ID
	prevAssignee := ""
	if i.Assignee != "" && i.Assignee != o.me {
		prevAssignee = i.Assignee
		if err := c.SetAssignee(ctx, id, ""); err != nil {
			return out, fmt.Errorf("%s is assigned to %s (not in progress); clearing the assignment to claim it failed: %w", id, sanitize.Inline(prevAssignee), err)
		}
		// Did someone check it out between our read and our clear?
		cur, err := c.Show(ctx, id)
		if err != nil {
			return out, fmt.Errorf("cleared the assignment on %s but could not re-read it: %w", id, err)
		}
		if cur.Status != i.Status {
			repairCleared(ctx, c, cur, i, prevAssignee, o.me)
			return out, &errHeldByOther{id: id, l: lease.Of(cur, o.now, o.ttl), now: o.now}
		}
	}
	if err := c.Claim(ctx, id, o.me, stamp); err != nil {
		cur, rerr := c.Show(ctx, id)
		if rerr != nil {
			return out, err
		}
		repairCleared(ctx, c, cur, i, prevAssignee, o.me)
		if l := lease.Of(cur, o.now, o.ttl); l.State == lease.Live && l.Owner != o.me {
			return out, &errHeldByOther{id: id, l: l, now: o.now}
		}
		if beads.IsAlreadyClaimed(err) {
			return out, fmt.Errorf("%s: bd refused the claim (assigned to %s, status %s, not checked out); retry: %w",
				id, orUnrecorded(cur.Assignee), cur.Status, err)
		}
		return out, err
	}
	if prevAssignee != "" {
		out.Previous = prevAssignee
		_ = c.Note(ctx, id, fmt.Sprintf("wyk claim: was assigned to %s (open, not checked out); claimed by %s at %s",
			prevAssignee, o.me, o.now.UTC().Format(time.RFC3339)))
	}
	out.Action = actionClaimed
	return out, nil
}

// repairCleared undoes collateral damage from our assignee clear, given a
// fresh read cur of an issue we read earlier as orig:
//
//   - someone checked it out meanwhile (in_progress, empty assignee, a
//     wyk lease owner in metadata) → our clear wiped THEIR assignee: give
//     it back, so their heartbeat keeps finding and renewing it;
//   - nobody took it (still orig's status, still unassigned) → our claim
//     failed after the clear: restore the original assignment rather
//     than orphan the issue.
//
// Best-effort: a failed repair leaves the issue as the next read finds it.
func repairCleared(ctx context.Context, c claimClient, cur, orig beads.Issue, prevAssignee, me string) {
	if prevAssignee == "" || cur.Assignee != "" {
		return
	}
	if cur.Status == orig.Status {
		_ = c.SetAssignee(ctx, cur.ID, prevAssignee)
		return
	}
	if owner := cur.Metadata[lease.KeyOwner]; owner != "" && owner != me && cur.Status == "in_progress" {
		_ = c.SetAssignee(ctx, cur.ID, owner)
	}
}

// errFenced is a claim refused because the issue is fenced off for a
// human (`human`) or another agent (`agent-handoff`). Exit 3 like a live
// lease: "not yours — pick something else".
type errFenced struct {
	id    string
	label string
}

func (e *errFenced) Error() string {
	if e.label == "human" {
		return fmt.Sprintf("%s carries the human label — it is a human's move, not an agent's; pick something else (or -force if a human handed it back without removing the label)", e.id)
	}
	return fmt.Sprintf("%s carries the %s label — another agent owns it and a human coordinates; pick something else (or -force once the coordination is resolved)", e.id, e.label)
}

// isNotYours reports whether err means "someone else's — pick another":
// a live lease or a fence. Both map to exit 3.
func isNotYours(err error) bool {
	var held *errHeldByOther
	var fenced *errFenced
	return errors.As(err, &held) || errors.As(err, &fenced)
}

// takeoverNote is the audit line left on an issue whose lease was
// taken over. Previous owner and lapse time are what the next reader
// needs to understand why the assignee changed under them.
func takeoverNote(prev lease.Lease, me string, now time.Time) string {
	owner := prev.Owner
	if owner == "" {
		owner = "an unrecorded holder"
	}
	if prev.State == lease.Live {
		return fmt.Sprintf("wyk claim: lease forcibly taken from %s by %s at %s (was live, %s)",
			owner, me, now.UTC().Format(time.RFC3339), lease.Remaining(prev, now))
	}
	return fmt.Sprintf("wyk claim: lease from %s expired at %s; taken over by %s at %s",
		owner, prev.Until.UTC().Format(time.RFC3339), me, now.UTC().Format(time.RFC3339))
}

// releaseIssue drops my lease: clears assignee + metadata and reopens.
// Refuses to release someone else's live lease unless forced, so one
// agent can't silently unseat another by "releasing".
func releaseIssue(ctx context.Context, c claimClient, id string, o claimOpts) (claimOutcome, error) {
	i, err := c.Show(ctx, id)
	if err != nil {
		return claimOutcome{}, err
	}
	l := lease.Of(i, o.now, o.ttl)
	out := claimOutcome{ID: id, Owner: o.me, Action: actionReleased, Previous: l.Owner}
	if l.State == lease.None {
		return out, fmt.Errorf("%s is not claimed (status %s)", id, i.Status)
	}
	if l.State == lease.Live && l.Owner != o.me && !o.force {
		return out, &errHeldByOther{id: id, l: l, now: o.now}
	}
	if err := c.Release(ctx, id, o.me, lease.Keys()); err != nil {
		return out, err
	}
	return out, nil
}

// needsRenewal decides whether renewHeld re-stamps a lease. It must be
// mine — held or LAPSED: a lease whose holder outlived the TTL (one long
// turn, an idle stretch) is still assigned to me, and the rest of the
// contract treats it as mine to resume, so the heartbeat revives it
// rather than leaving it for takeover. renewBelow throttles the
// heartbeat: only leases with less than that left are re-stamped, so a
// Stop hook firing every turn doesn't write (one Dolt commit each) to a
// lease that still has most of its TTL. renewBelow <= 0 renews every
// lease I hold (an explicit `wyk claim -renew`).
func needsRenewal(l lease.Lease, me string, now time.Time, renewBelow time.Duration) bool {
	if l.State == lease.None || me == "" || l.Owner != me {
		return false
	}
	if renewBelow <= 0 || l.State == lease.Expired || l.Until.IsZero() {
		return true
	}
	return l.Until.Sub(now) < renewBelow
}

// renewHeld renews the leases identity `me` holds across subs — the
// Stop-hook auto-renew and `wyk claim -renew` with no id. Rows whose
// lease is implicit (a bare bd claim under this actor) get a proper
// stamp from here on. Returns the renewed IDs and per-repo errors;
// best-effort by design, since a renewal that fails just expires later.
func renewHeld(ctx context.Context, subs []inboxSub, me string, ttl, renewBelow time.Duration, now time.Time) (renewed []string, errs []subError) {
	type res struct {
		ids []string
		err error
	}
	results := make([]res, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s inboxSub) {
			defer wg.Done()
			held, err := s.client.ListInProgressBy(ctx, me)
			if err != nil {
				results[i].err = err
				return
			}
			for _, is := range held {
				l := lease.Of(is, now, ttl)
				if !needsRenewal(l, me, now, renewBelow) {
					continue
				}
				branch := l.Branch
				if branch == "" {
					branch = currentBranch(s.client.Dir)
				}
				if err := s.client.SetMetadata(ctx, is.ID, lease.Stamp(me, now.Add(ttl), branch)); err != nil {
					results[i].err = errors.Join(results[i].err, fmt.Errorf("%s: %w", is.ID, err))
					continue
				}
				results[i].ids = append(results[i].ids, is.ID)
			}
		}(i, s)
	}
	wg.Wait()
	for i, s := range subs {
		renewed = append(renewed, results[i].ids...)
		if results[i].err != nil {
			errs = append(errs, subError{repo: s.name, err: results[i].err})
		}
	}
	return renewed, errs
}

// locateIssue finds the workspace that owns id. An explicit dir wins.
// Otherwise the cwd workspace is tried first (the common case: an agent
// claiming in the repo it sits in); if cwd isn't a bd workspace, or bd
// there doesn't know the id, the registry is scanned for a repo whose
// name is the id's prefix — so `wyk claim other-repo-1k2j` works from
// anywhere the way the multi-repo views do. Returns the client positioned
// on the owning workspace, its registry name, and the issue.
func locateIssue(ctx context.Context, id, dir string) (*beads.Client, string, beads.Issue, error) {
	if dir != "" {
		c := beads.NewClient()
		c.Dir = dir
		i, err := c.Show(ctx, id)
		return c, "", i, err
	}
	c := beads.NewClient()
	i, err := c.Show(ctx, id)
	if err == nil {
		return c, "", i, nil
	}
	if errors.Is(err, beads.ErrBDNotFound) {
		return nil, "", beads.Issue{}, err
	}
	firstErr := err
	regPath, rerr := registry.DefaultPath()
	if rerr != nil {
		return nil, "", beads.Issue{}, firstErr
	}
	reg, rerr := registry.Load(regPath)
	if rerr != nil {
		return nil, "", beads.Issue{}, firstErr
	}
	for _, r := range reg.Repos {
		if !strings.HasPrefix(id, r.Name+"-") {
			continue
		}
		rc := beads.NewClient()
		rc.Dir = r.Path
		if ri, rerr := rc.Show(ctx, id); rerr == nil {
			return rc, r.Name, ri, nil
		}
	}
	return nil, "", beads.Issue{}, firstErr
}

// runClaim implements `wyk claim`: take, renew, or release an identity-
// owned, expiring lease on an issue (wyk-contract/v4).
//
// Exit codes: 0 done; 1 bd error or claim refused for a non-contention
// reason (closed, not held); 2 bd missing / no workspace; 3 held by
// another identity's LIVE lease; 64 usage.
func runClaim(args []string) int {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.Usage = subcommandUsage(fs, "claim")
	cfg := loadConfigBestEffort()
	dir := fs.String("C", "", "workspace the issue lives in; default: the cwd workspace, then the registered repo whose name prefixes the id")
	identity := fs.String("identity", "", "claim as this agent identity `name`; falls back to $WYK_AGENT_IDENTITY, then the Claude session (claude-<id>), then bd's actor ($BEADS_ACTOR / git user.name / $USER)")
	ttlFlag := fs.String("ttl", "", "lease length for THIS claim (duration like 2h / 90m, or whole minutes); default: $WYK_CLAIM_TTL, then config claim_ttl, then "+lease.DefaultTTL.String())
	renew := fs.Bool("renew", false, "extend a lease I already hold (with no <id>: every lease I hold, across the configured scope)")
	release := fs.Bool("release", false, "give the issue back: clear my lease, unassign, and reopen it")
	force := fs.Bool("force", false, "take over (or release) a LIVE lease held by someone else, or claim an issue labelled human / agent-handoff — leaves a note naming the holder; use only when you know the holder is gone")
	asJSON := fs.Bool("json", false, "emit the outcome as JSON ({id, action, owner, until, branch, previous_owner})")
	compact := fs.Bool("compact", cfg.CompactJSON, "with -json, emit non-indented JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if *renew && *release {
		fmt.Fprintln(os.Stderr, "wyk claim: -renew and -release are mutually exclusive")
		return 64
	}
	if fs.NArg() > 1 || (fs.NArg() == 0 && !*renew) {
		fmt.Fprintln(os.Stderr, "usage: "+usageLine("claim"))
		return 64
	}

	me, source, err := resolveClaimIdentity(*identity)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk claim:", err)
		return 64
	}
	ttl, err := resolveClaimTTL(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk claim:", err)
		return 64
	}
	if *ttlFlag != "" {
		if ttl, err = lease.ParseTTL(*ttlFlag); err != nil {
			fmt.Fprintln(os.Stderr, "wyk claim: -ttl:", err)
			return 64
		}
	}
	ctx := context.Background()
	now := time.Now()

	// `wyk claim -renew` with no id: keep everything I hold alive.
	if fs.NArg() == 0 {
		subs, code := inboxSubs(*dir, "", false)
		if code != 0 {
			return code
		}
		renewed, subErrs := renewHeld(ctx, subs, me, ttl, 0, now)
		if *asJSON {
			res := struct {
				Renewed []string    `json:"renewed"`
				Owner   string      `json:"owner"`
				Until   time.Time   `json:"until"`
				Errors  []repoError `json:"errors,omitempty"`
			}{Renewed: renewed, Owner: me, Until: now.Add(ttl), Errors: subErrorsToRepoErrors(subErrs)}
			if res.Renewed == nil {
				res.Renewed = []string{}
			}
			_ = emitJSON(os.Stdout, res, *compact)
		} else {
			fmt.Printf("renewed %d lease(s) held by %s until %s\n", len(renewed), orUnrecorded(me), now.Add(ttl).Local().Format(time.RFC3339))
			for _, id := range renewed {
				fmt.Printf("  %s\n", id)
			}
			if len(subErrs) > 0 {
				fmt.Fprintf(os.Stderr, "wyk claim: %d repo(s) failed: %s\n", len(subErrs), joinRepoErrors(subErrorsToRepoErrors(subErrs)))
			}
		}
		if len(renewed) == 0 && len(subErrs) == len(subs) && len(subs) > 0 {
			return 1
		}
		return 0
	}

	id := fs.Arg(0)
	c, repoName, _, err := locateIssue(ctx, id, *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk claim:", err)
		switch {
		case errors.Is(err, beads.ErrBDNotFound), errors.Is(err, beads.ErrNoWorkspace):
			return 2
		}
		return 1
	}
	maybeAutoRegister("wyk claim", c.Dir, os.Stderr)

	opts := claimOpts{me: me, ttl: ttl, branch: currentBranch(c.Dir), now: now, force: *force, renew: *renew}
	var out claimOutcome
	if *release {
		out, err = releaseIssue(ctx, c, id, opts)
	} else {
		out, err = claimIssue(ctx, c, id, opts)
	}
	out.Repo = repoName
	out.IdentitySource = source
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk claim:", err)
		var held *errHeldByOther
		if errors.As(err, &held) {
			if *asJSON {
				_ = emitJSON(os.Stdout, struct {
					claimOutcome
					HeldBy string    `json:"held_by"`
					Until  time.Time `json:"held_until,omitzero"`
				}{out, held.l.Owner, held.l.Until}, *compact)
			}
			return exitHeldByOther
		}
		if isNotYours(err) {
			return exitHeldByOther
		}
		if out.Action == actionTookOver {
			// The takeover itself landed; only the note failed. Say so
			// on stdout like a success, exit 1 for the partial.
			printClaimOutcome(out, *asJSON, *compact)
		}
		return 1
	}
	printClaimOutcome(out, *asJSON, *compact)
	return 0
}

// printClaimOutcome renders the result line (or JSON) for a single-issue
// claim / renew / release / takeover.
func printClaimOutcome(out claimOutcome, asJSON, compact bool) {
	if asJSON {
		_ = emitJSON(os.Stdout, out, compact)
		return
	}
	where := out.ID
	if out.Repo != "" {
		where = "[" + sanitize.Inline(out.Repo) + "] " + out.ID
	}
	switch out.Action {
	case actionReleased:
		fmt.Printf("released %s (was %s)\n", where, orUnrecorded(out.Previous))
	case actionTookOver:
		fmt.Printf("took over %s from %s as %s until %s%s\n", where, orUnrecorded(out.Previous), orUnrecorded(out.Owner),
			out.Until.Local().Format(time.RFC3339), branchSuffix(out.Branch))
	case actionRenewed:
		fmt.Printf("renewed %s for %s until %s%s\n", where, orUnrecorded(out.Owner), out.Until.Local().Format(time.RFC3339), branchSuffix(out.Branch))
	default:
		was := ""
		if out.Previous != "" {
			was = " (was assigned to " + orUnrecorded(out.Previous) + ")"
		}
		fmt.Printf("claimed %s as %s until %s%s%s\n", where, orUnrecorded(out.Owner), out.Until.Local().Format(time.RFC3339), branchSuffix(out.Branch), was)
	}
}

func branchSuffix(b string) string {
	if b == "" {
		return ""
	}
	return " on " + sanitize.Inline(b)
}

// orUnrecorded renders an owner for terminal output: sanitized (it is bd
// content, or an env value) and never blank.
func orUnrecorded(s string) string {
	if s == "" {
		return "an unrecorded holder"
	}
	return sanitize.Inline(s)
}
