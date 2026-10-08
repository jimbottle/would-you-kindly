package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/lease"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

var claimNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// fakeClaimClient records the writes claimIssue / releaseIssue make
// against one issue and lets a test script bd's claim refusal.
type fakeClaimClient struct {
	issue    beads.Issue
	showErr  error
	claimErr error
	calls    []string
	meta     beads.Metadata
	noteText string
}

func (f *fakeClaimClient) Show(context.Context, string) (beads.Issue, error) {
	f.calls = append(f.calls, "show")
	return f.issue, f.showErr
}
func (f *fakeClaimClient) Claim(_ context.Context, _, actor string, meta beads.Metadata) error {
	f.calls = append(f.calls, "claim:"+actor)
	f.meta = meta
	return f.claimErr
}
func (f *fakeClaimClient) Reassign(_ context.Context, _, actor string, meta beads.Metadata) error {
	f.calls = append(f.calls, "reassign:"+actor)
	f.meta = meta
	return nil
}
func (f *fakeClaimClient) SetMetadata(_ context.Context, _ string, meta beads.Metadata) error {
	f.calls = append(f.calls, "setmeta")
	f.meta = meta
	return nil
}
func (f *fakeClaimClient) Release(_ context.Context, _, actor string, keys []string) error {
	f.calls = append(f.calls, "release:"+actor+":"+strings.Join(keys, ","))
	return nil
}
func (f *fakeClaimClient) SetAssignee(_ context.Context, _ string, who string) error {
	f.calls = append(f.calls, "assign:"+who)
	return nil
}
func (f *fakeClaimClient) Note(_ context.Context, _ string, text string) error {
	f.calls = append(f.calls, "note")
	f.noteText = text
	return nil
}

func stampAt(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func baseOpts() claimOpts {
	return claimOpts{me: "claude-a", ttl: 2 * time.Hour, branch: "feat/x", now: claimNow}
}

func TestClaimIssue_UnclaimedUsesAtomicClaimWithStamp(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open"}}
	out, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if out.Action != actionClaimed || out.Owner != "claude-a" || !out.Until.Equal(claimNow.Add(2*time.Hour)) {
		t.Fatalf("outcome=%+v", out)
	}
	if strings.Join(f.calls, " ") != "show claim:claude-a" {
		t.Fatalf("calls=%v, want show then an atomic claim", f.calls)
	}
	if f.meta[lease.KeyOwner] != "claude-a" || f.meta[lease.KeyUntil] != stampAt(claimNow.Add(2*time.Hour)) || f.meta[lease.KeyBranch] != "feat/x" {
		t.Fatalf("stamp=%v", f.meta)
	}
}

func TestClaimIssue_MineRenewsWithoutTouchingStatus(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "claude-a", Metadata: beads.Metadata{
		lease.KeyOwner: "claude-a", lease.KeyUntil: stampAt(claimNow.Add(10 * time.Minute))}}}
	out, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil || out.Action != actionRenewed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if strings.Join(f.calls, " ") != "show setmeta" {
		t.Fatalf("calls=%v, want a metadata-only renewal", f.calls)
	}
}

func TestClaimIssue_LiveLeaseOfAnotherIsRefused(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "codex-1", Metadata: beads.Metadata{
		lease.KeyOwner: "codex-1", lease.KeyUntil: stampAt(claimNow.Add(time.Hour))}}}
	_, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	var held *errHeldByOther
	if !errors.As(err, &held) || held.l.Owner != "codex-1" {
		t.Fatalf("err=%v, want held-by codex-1", err)
	}
	if !strings.Contains(err.Error(), "codex-1") || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("message should name the holder and the override: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls=%v, want no write on a refusal", f.calls)
	}
}

func TestClaimIssue_ExpiredLeaseIsTakenOverWithNote(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "codex-1", Metadata: beads.Metadata{
		lease.KeyOwner: "codex-1", lease.KeyUntil: stampAt(claimNow.Add(-15 * time.Minute))}}}
	out, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil || out.Action != actionTookOver || out.Previous != "codex-1" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if strings.Join(f.calls, " ") != "show reassign:claude-a note" {
		t.Fatalf("calls=%v, want a forced reassign then a note", f.calls)
	}
	if !strings.Contains(f.noteText, "codex-1") || !strings.Contains(f.noteText, "expired") || !strings.Contains(f.noteText, "claude-a") {
		t.Fatalf("note=%q should name both parties and the lapse", f.noteText)
	}
}

func TestClaimIssue_ImplicitBareClaimExpiresByUpdatedAt(t *testing.T) {
	// A raw `bd update --claim` by another actor, silent for longer than
	// the TTL, is claimable; silent for less, it isn't.
	stale := beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "jimbottle", UpdatedAt: claimNow.Add(-3 * time.Hour)}
	f := &fakeClaimClient{issue: stale}
	if out, err := claimIssue(context.Background(), f, "x-1", baseOpts()); err != nil || out.Action != actionTookOver {
		t.Fatalf("stale bare claim: out=%+v err=%v", out, err)
	}
	fresh := stale
	fresh.UpdatedAt = claimNow.Add(-10 * time.Minute)
	f = &fakeClaimClient{issue: fresh}
	if _, err := claimIssue(context.Background(), f, "x-1", baseOpts()); err == nil {
		t.Fatal("fresh bare claim by another actor must be refused")
	}
}

func TestClaimIssue_ForceTakesOverLiveLease(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "in_progress", Metadata: beads.Metadata{
		lease.KeyOwner: "codex-1", lease.KeyUntil: stampAt(claimNow.Add(time.Hour))}}}
	o := baseOpts()
	o.force = true
	out, err := claimIssue(context.Background(), f, "x-1", o)
	if err != nil || out.Action != actionTookOver {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if !strings.Contains(f.noteText, "forcibly") {
		t.Fatalf("note=%q should say the lease was live", f.noteText)
	}
}

func TestClaimIssue_RenewOnlyRefusesToClaim(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open"}}
	o := baseOpts()
	o.renew = true
	if _, err := claimIssue(context.Background(), f, "x-1", o); err == nil || len(f.calls) != 1 {
		t.Fatalf("err=%v calls=%v, want refusal with no write", err, f.calls)
	}
}

func TestClaimIssue_ClosedIsRefused(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "closed"}}
	if _, err := claimIssue(context.Background(), f, "x-1", baseOpts()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaimIssue_LostRaceReportsActualHolder(t *testing.T) {
	// Our read saw it open; bd refused the claim because someone else got
	// there first. The refusal must name the winner from a re-read.
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open"},
		claimErr: errors.New("bd update x-1: Error claiming x-1: issue already claimed by codex-1")}
	// After the failed claim the re-read sees the winner.
	f.issue = beads.Issue{ID: "x-1", Status: "open"}
	calls := 0
	orig := f.issue
	_ = orig
	fc := &racingClient{fakeClaimClient: f, onShow: func() {
		calls++
		if calls == 2 {
			f.issue = beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "codex-1", UpdatedAt: claimNow}
		}
	}}
	_, err := claimIssue(context.Background(), fc, "x-1", baseOpts())
	var held *errHeldByOther
	if !errors.As(err, &held) || held.l.Owner != "codex-1" {
		t.Fatalf("err=%v, want held-by codex-1 from the re-read", err)
	}
}

// racingClient lets a test mutate the fake between Show calls.
type racingClient struct {
	*fakeClaimClient
	onShow func()
}

func (r *racingClient) Show(ctx context.Context, id string) (beads.Issue, error) {
	r.onShow()
	return r.fakeClaimClient.Show(ctx, id)
}

func TestReleaseIssue(t *testing.T) {
	mine := beads.Issue{ID: "x-1", Status: "in_progress", Metadata: beads.Metadata{
		lease.KeyOwner: "claude-a", lease.KeyUntil: stampAt(claimNow.Add(time.Hour))}}
	f := &fakeClaimClient{issue: mine}
	out, err := releaseIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil || out.Action != actionReleased {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if got := f.calls[1]; got != "release:claude-a:wyk.lease.owner,wyk.lease.until,wyk.lease.branch" {
		t.Fatalf("release call=%q", got)
	}

	theirs := mine
	theirs.Metadata = beads.Metadata{lease.KeyOwner: "codex-1", lease.KeyUntil: stampAt(claimNow.Add(time.Hour))}
	f = &fakeClaimClient{issue: theirs}
	if _, err := releaseIssue(context.Background(), f, "x-1", baseOpts()); err == nil {
		t.Fatal("releasing someone else's live lease must be refused")
	}
	o := baseOpts()
	o.force = true
	if _, err := releaseIssue(context.Background(), f, "x-1", o); err != nil {
		t.Fatalf("forced release: %v", err)
	}

	f = &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open"}}
	if _, err := releaseIssue(context.Background(), f, "x-1", baseOpts()); err == nil {
		t.Fatal("releasing an unclaimed issue must be an error")
	}
}

func TestResolveClaimIdentity_PrecedenceAndSlug(t *testing.T) {
	prev := gitConfigValue
	t.Cleanup(func() { gitConfigValue = prev })
	gitConfigValue = func(key string) string { return "Evan Ray" }

	t.Setenv(sessionEnvVar, "") // the actor-chain cases below run outside a session
	t.Setenv(identityEnvVar, "claude-a")
	t.Setenv("BEADS_ACTOR", "bot")
	if name, src, _ := resolveClaimIdentity(""); name != "claude-a" || src != "$"+identityEnvVar {
		t.Fatalf("got %q from %q, want the explicit identity", name, src)
	}
	if name, src, _ := resolveClaimIdentity("codex-1"); name != "codex-1" || src != "-identity" {
		t.Fatalf("got %q from %q, want the flag", name, src)
	}
	t.Setenv(identityEnvVar, "")
	if name, src, _ := resolveClaimIdentity(""); name != "bot" || src != "$BEADS_ACTOR" {
		t.Fatalf("got %q from %q, want $BEADS_ACTOR", name, src)
	}
	t.Setenv("BEADS_ACTOR", "")
	if name, src, _ := resolveClaimIdentity(""); name != "Evan Ray" || src != "git user.name" {
		t.Fatalf("got %q from %q, want bd's raw actor (git user.name), unslugified so it matches a bare bd --claim", name, src)
	}
	gitConfigValue = func(string) string { return "" }
	t.Setenv("USER", "")
	if _, _, err := resolveClaimIdentity(""); err == nil {
		t.Fatal("no identity anywhere must be an error")
	}
	t.Setenv(identityEnvVar, "Bad Name")
	if _, _, err := resolveClaimIdentity(""); err == nil {
		t.Fatal("a malformed explicit identity must be an error, not a fallthrough")
	}
}

func TestResolveClaimTTL(t *testing.T) {
	t.Setenv(claimTTLEnvVar, "")
	if d, err := resolveClaimTTL(wykconfig.Config{}); err != nil || d != lease.DefaultTTL {
		t.Fatalf("default: %v %v", d, err)
	}
	if d, err := resolveClaimTTL(wykconfig.Config{ClaimTTL: "90m"}); err != nil || d != 90*time.Minute {
		t.Fatalf("config: %v %v", d, err)
	}
	t.Setenv(claimTTLEnvVar, "45m")
	if d, err := resolveClaimTTL(wykconfig.Config{ClaimTTL: "90m"}); err != nil || d != 45*time.Minute {
		t.Fatalf("env beats config: %v %v", d, err)
	}
	t.Setenv(claimTTLEnvVar, "soon")
	if _, err := resolveClaimTTL(wykconfig.Config{}); err == nil {
		t.Fatal("invalid env TTL must error, not fall through")
	}
	t.Setenv(claimTTLEnvVar, "")
	if _, err := resolveClaimTTL(wykconfig.Config{ClaimTTL: "0"}); err == nil {
		t.Fatal("invalid config TTL must error")
	}
}

func TestRunClaim_UsageErrors(t *testing.T) {
	cases := [][]string{
		{},                          // no id, no -renew
		{"-renew", "-release", "x"}, // mutually exclusive
		{"a", "b"},                  // two ids
	}
	for _, args := range cases {
		var code int
		captureOutErr(t, func() { code = runClaim(args) })
		if code != 64 {
			t.Errorf("args=%v: exit %d, want 64", args, code)
		}
	}
}

func TestClaimIssue_MyOwnLapsedLeaseRenewsNotTakesOver(t *testing.T) {
	f := &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "claude-a", Metadata: beads.Metadata{
		lease.KeyOwner: "claude-a", lease.KeyUntil: stampAt(claimNow.Add(-time.Hour))}}}
	out, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil || out.Action != actionRenewed {
		t.Fatalf("out=%+v err=%v, want a renewal", out, err)
	}
	if strings.Join(f.calls, " ") != "show setmeta" {
		t.Fatalf("calls=%v, want no takeover note", f.calls)
	}
}

func TestClaimOutput_SanitizesHolderNames(t *testing.T) {
	// Owner names come from bd metadata / assignee: untrusted text that
	// must not reach the terminal with escape sequences intact.
	evil := "bad\x1b]0;pwned\x07guy"
	err := &errHeldByOther{id: "x-1", l: lease.Lease{Owner: evil, Until: claimNow.Add(time.Hour)}, now: claimNow}
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("refusal leaked an escape: %q", err.Error())
	}
	out := captureHandoffStdout(t, func() {
		printClaimOutcome(claimOutcome{ID: "x-1", Action: actionTookOver, Owner: "me", Previous: evil, Until: claimNow}, false, false)
	})
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("takeover line leaked an escape: %q", out)
	}
}

func TestSessionIdentity(t *testing.T) {
	cases := map[string]string{
		"88ef57f5-1924-4dab-9aab-24e4de33a1d0": "claude-88ef57f5",
		"ABCD-ef12-3456":                       "claude-abcdef12",
		"  ":                                   "",
		"../..":                                "",
	}
	for in, want := range cases {
		got := sessionIdentity(in)
		if got != want {
			t.Errorf("sessionIdentity(%q)=%q, want %q", in, got, want)
		}
		if got != "" && validateIdentity(got) != nil {
			t.Errorf("%q is not a legal identity slug", got)
		}
	}
}

func TestResolveClaimIdentity_SessionDefault(t *testing.T) {
	prev := gitConfigValue
	t.Cleanup(func() { gitConfigValue = prev })
	gitConfigValue = func(string) string { return "jimbottle" }
	t.Setenv("BEADS_ACTOR", "")

	// No role name: each Claude session gets its own identity, ahead of
	// the shared git user.
	t.Setenv(identityEnvVar, "")
	t.Setenv(sessionEnvVar, "88ef57f5-1924-4dab-9aab-24e4de33a1d0")
	if name, src, _ := resolveClaimIdentity(""); name != "claude-88ef57f5" || src != "Claude session" {
		t.Fatalf("got %q from %q, want the session default", name, src)
	}
	// Two concurrent sessions never collide.
	a, _, _ := resolveClaimIdentityFor("", "11111111-aaaa")
	b, _, _ := resolveClaimIdentityFor("", "22222222-bbbb")
	if a == b {
		t.Fatalf("two sessions share identity %q", a)
	}
	// A role-named agent outranks the session.
	t.Setenv(identityEnvVar, "reviewer")
	if name, _, _ := resolveClaimIdentity(""); name != "reviewer" {
		t.Fatalf("got %q, want the explicit role name", name)
	}
	// The Stop hook passes the payload's session even when the env lacks it.
	t.Setenv(identityEnvVar, "")
	t.Setenv(sessionEnvVar, "")
	if name, _, _ := resolveClaimIdentityFor("", "33333333-cccc"); name != "claude-33333333" {
		t.Fatalf("got %q, want the passed-in session", name)
	}
	// Outside any session: bd's actor, as before.
	if name, src, _ := resolveClaimIdentity(""); name != "jimbottle" || !isActorFallback(src) {
		t.Fatalf("got %q from %q, want the actor fallback", name, src)
	}
}

func TestClaimIssue_OpenButAssignedElsewhereIsClearedThenClaimed(t *testing.T) {
	// Project convention assigns issues at creation without checking them
	// out; bd's --claim refuses those. wyk clears the assignment, claims
	// atomically, confirms, and notes the previous assignee.
	f := &assignTracker{fakeClaimClient: &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open", Assignee: "jimbottle"}}}
	f.afterClaim = func() {
		f.issue = beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "claude-a", Metadata: f.meta}
	}
	out, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	if err != nil || out.Action != actionClaimed || out.Previous != "jimbottle" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if got := strings.Join(f.calls, " "); got != "show assign: claim:claude-a show note" {
		t.Fatalf("calls=%q, want clear → claim → confirm → note", got)
	}
	if !strings.Contains(f.noteText, "was assigned to jimbottle") {
		t.Fatalf("note=%q", f.noteText)
	}
}

func TestClaimIssue_AssignedElsewhereLosesRaceAndReportsWinner(t *testing.T) {
	f := &assignTracker{fakeClaimClient: &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open", Assignee: "jimbottle"},
		claimErr: errors.New("bd update x-1: Error claiming x-1: issue already claimed by codex-1")}}
	f.afterClaim = func() {
		f.issue = beads.Issue{ID: "x-1", Status: "in_progress", Assignee: "codex-1", UpdatedAt: claimNow}
	}
	_, err := claimIssue(context.Background(), f, "x-1", baseOpts())
	var held *errHeldByOther
	if !errors.As(err, &held) || held.l.Owner != "codex-1" {
		t.Fatalf("err=%v, want held by the winner", err)
	}
	for _, c := range f.calls {
		if c == "assign:jimbottle" {
			t.Fatal("must not restore the old assignee over the winner")
		}
	}
}

func TestClaimIssue_ClaimFailureRestoresTheAssignment(t *testing.T) {
	f := &assignTracker{fakeClaimClient: &fakeClaimClient{issue: beads.Issue{ID: "x-1", Status: "open", Assignee: "jimbottle"},
		claimErr: errors.New("bd update x-1: database is locked")}}
	f.afterClaim = func() { f.issue = beads.Issue{ID: "x-1", Status: "open"} }
	if _, err := claimIssue(context.Background(), f, "x-1", baseOpts()); err == nil {
		t.Fatal("want the bd error")
	}
	if last := f.calls[len(f.calls)-1]; last != "assign:jimbottle" {
		t.Fatalf("calls=%v, want the assignment restored", f.calls)
	}
}

// assignTracker records SetAssignee and lets a test change the issue the
// moment Claim runs.
type assignTracker struct {
	*fakeClaimClient
	afterClaim func()
}

func (a *assignTracker) SetAssignee(_ context.Context, _ string, who string) error {
	a.calls = append(a.calls, "assign:"+who)
	return nil
}

func (a *assignTracker) Claim(ctx context.Context, id, actor string, meta beads.Metadata) error {
	err := a.fakeClaimClient.Claim(ctx, id, actor, meta)
	if a.afterClaim != nil {
		a.afterClaim()
	}
	return err
}

func TestNeedsRenewal(t *testing.T) {
	ttl := 2 * time.Hour
	mk := func(owner string, left time.Duration) lease.Lease {
		l := lease.Lease{Owner: owner, Until: claimNow.Add(left), State: lease.Live}
		if left < 0 {
			l.State = lease.Expired
		}
		return l
	}
	cases := []struct {
		name  string
		l     lease.Lease
		below time.Duration
		want  bool
	}{
		{"fresh, throttled", mk("me", 110*time.Minute), ttl / 2, false},
		{"past half, throttled", mk("me", 30*time.Minute), ttl / 2, true},
		{"my lapsed lease is revived", mk("me", -time.Minute), ttl / 2, true},
		{"explicit renew ignores the throttle", mk("me", 110*time.Minute), 0, true},
		{"someone else's", mk("them", 10*time.Minute), ttl / 2, false},
		{"no lease", lease.Lease{State: lease.None}, 0, false},
	}
	for _, tc := range cases {
		if got := needsRenewal(tc.l, "me", claimNow, tc.below); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
