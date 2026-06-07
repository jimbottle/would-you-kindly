package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

func TestBDSource_PickFetchCall(t *testing.T) {
	cases := []struct {
		name          string
		preset        filter.Preset
		me            string
		includeClosed bool
		wantCall      fetchCall
		wantQuery     string // checked only when wantCall == fetchQuery
	}{
		{"ready ignores includeClosed", filter.PresetReady, "ev", true, fetchReady, ""},
		{"all open uses list", filter.PresetAll, "ev", false, fetchList, ""},
		{"all + closed uses listall", filter.PresetAll, "ev", true, fetchListAll, ""},
		{"mine with me uses query", filter.PresetMine, "ev", false, fetchQuery, `assignee=ev AND status!=closed`},
		{"mine with me + closed uses query", filter.PresetMine, "ev", true, fetchQuery, `assignee=ev`},
		// Regression for the MED finding on job 1277: mine + empty
		// me + includeClosed used to produce bd query "" → error.
		// The empty-query branch now routes to listall so the user
		// sees the closest expressible answer (every issue) rather
		// than a bd-error banner.
		{"mine no-me + closed routes to listall", filter.PresetMine, "", true, fetchListAll, ""},
		{"mine no-me open uses query", filter.PresetMine, "", false, fetchQuery, `status!=closed`},
		{"human open uses query", filter.PresetHuman, "", false, fetchQuery, `label=human AND status!=closed`},
		{"human + closed uses query", filter.PresetHuman, "", true, fetchQuery, `label=human`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &BDSource{Me: tc.me, IncludeClosed: tc.includeClosed}
			call, q := s.pickFetchCall(tc.preset)
			if call != tc.wantCall {
				t.Errorf("call = %v, want %v", call, tc.wantCall)
			}
			if tc.wantCall == fetchQuery && q != tc.wantQuery {
				t.Errorf("query = %q, want %q", q, tc.wantQuery)
			}
		})
	}
}

// fakeRepoSource implements both Source and Mutator so it can stand
// in for a single-repo BDSource inside MultiBDSource. Each instance
// records the writes routed to it so the multi-source test can
// assert routing.
type fakeRepoSource struct {
	issues       []beads.Issue
	fetchErr     error
	closed       []string
	reopened     []string
	deferred     []labelOp    // {id, when} parallels notes/added/removed
	priorities   []priorityOp // {id, priority} for SetPriority
	assignees    []labelOp    // {id, owner} for SetAssignee
	descriptions []labelOp    // {id, body} for SetDescription
	issueTypes   []labelOp    // {id, type} for SetIssueType
	added        []labelOp
	removed      []labelOp
	notes        []labelOp

	// fetchDelay, when non-zero, makes Fetch sleep for that long
	// while holding a fan-out slot — long enough for the
	// concurrency-cap test to observe overlapping fetches via
	// curFetch/maxFetch.
	fetchDelay time.Duration
	// curFetch / maxFetch track concurrent Fetch entries so a test
	// can assert FetchWithSubErrors never runs more than
	// fetchConcurrency sub-fetches at once. BOTH must be shared
	// across all subs in a run (they measure the peak across the
	// whole fan-out, not per-sub — each sub's Fetch only ever runs
	// once, so a per-sub counter would never exceed 1). Touched via
	// sync/atomic so the race detector stays quiet under the
	// MultiBDSource fan-out; nil maxFetch disables the tracking
	// entirely so the existing routing tests pay nothing.
	curFetch *int32
	maxFetch *int32

	// timeoutThenOK, when > 0, makes the next that-many Fetch calls
	// return beads.ErrTimedOut before succeeding — exercises the
	// transient-timeout retry in FetchWithSubErrors. fetchCount
	// records total Fetch invocations so a test can assert the retry
	// actually fired. atomic so the fan-out's goroutine is race-clean.
	timeoutThenOK int32
	fetchCount    int32
}

// priorityOp records a SetPriority call so multi-source tests can
// assert routing landed on the right sub with the right value.
type priorityOp struct {
	id string
	p  int
}

func (f *fakeRepoSource) Fetch(_ context.Context, _ filter.Preset) ([]beads.Issue, error) {
	// Track concurrent entries so the cap test can assert the
	// observed peak never exceeds fetchConcurrency. atomic so -race
	// is happy with the MultiBDSource fan-out reading/writing these
	// from several goroutines at once. Skipped entirely when maxFetch
	// is nil (the routing tests), keeping them allocation-free.
	if f.maxFetch != nil {
		n := atomic.AddInt32(f.curFetch, 1)
		for {
			old := atomic.LoadInt32(f.maxFetch)
			if n <= old || atomic.CompareAndSwapInt32(f.maxFetch, old, n) {
				break
			}
		}
		if f.fetchDelay > 0 {
			time.Sleep(f.fetchDelay)
		}
		atomic.AddInt32(f.curFetch, -1)
	}
	if atomic.AddInt32(&f.fetchCount, 1) <= atomic.LoadInt32(&f.timeoutThenOK) {
		return nil, fmt.Errorf("bd list --json: timed out after 10.001s: %w", beads.ErrTimedOut)
	}
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.issues, nil
}
func (f *fakeRepoSource) Close(_ context.Context, i beads.Issue) error {
	f.closed = append(f.closed, i.ID)
	return nil
}
func (f *fakeRepoSource) AddLabel(_ context.Context, i beads.Issue, label string) error {
	f.added = append(f.added, labelOp{i.ID, label})
	return nil
}
func (f *fakeRepoSource) RemoveLabel(_ context.Context, i beads.Issue, label string) error {
	f.removed = append(f.removed, labelOp{i.ID, label})
	return nil
}
func (f *fakeRepoSource) Note(_ context.Context, i beads.Issue, text string) error {
	f.notes = append(f.notes, labelOp{i.ID, text})
	return nil
}
func (f *fakeRepoSource) Create(_ context.Context, _, title, _ string) (string, error) {
	// Stub returns a fake ID derived from the title so tests can
	// assert which sub got routed to without wiring a real bd.
	// The assignee arg is preserved in production but tests that
	// care about it use the stubMutator's `createdAssignees`
	// slice (parallel to `created`).
	return "new-" + title, nil
}
func (f *fakeRepoSource) Detail(_ context.Context, i beads.Issue) (beads.Issue, error) {
	// Stub echoes the input back with a fixed Notes field so tests
	// can verify the Detail call reached the right sub.
	i.Notes = "stub notes from " + i.Repo
	return i, nil
}
func (f *fakeRepoSource) Reopen(_ context.Context, i beads.Issue) error {
	// Record the call so TestMultiBDSource_ReopenRoutesAndErrors
	// can assert routing landed on the right sub.
	f.reopened = append(f.reopened, i.ID)
	return nil
}
func (f *fakeRepoSource) SetDefer(_ context.Context, i beads.Issue, when string) error {
	f.deferred = append(f.deferred, labelOp{i.ID, when})
	return nil
}
func (f *fakeRepoSource) SetPriority(_ context.Context, i beads.Issue, p int) error {
	f.priorities = append(f.priorities, priorityOp{i.ID, p})
	return nil
}
func (f *fakeRepoSource) SetAssignee(_ context.Context, i beads.Issue, assignee string) error {
	f.assignees = append(f.assignees, labelOp{i.ID, assignee})
	return nil
}
func (f *fakeRepoSource) SetDescription(_ context.Context, i beads.Issue, body string) error {
	f.descriptions = append(f.descriptions, labelOp{i.ID, body})
	return nil
}
func (f *fakeRepoSource) SetIssueType(_ context.Context, i beads.Issue, issueType string) error {
	f.issueTypes = append(f.issueTypes, labelOp{i.ID, issueType})
	return nil
}

// ListDeps satisfies DepLister (and thus fullSource) for the
// multi-repo routing tests. The fake has no dependency edges, so
// every issue resolves to an empty set — enough to keep the
// MultiBDSource.ListDeps routing test honest without canned data.
func (f *fakeRepoSource) ListDeps(_ context.Context, _ string) ([]beads.Issue, error) {
	return nil, nil
}

// ListDependents is the reverse-direction twin of ListDeps; same
// best-effort empty set so fakeRepoSource keeps satisfying DepLister
// / fullSource.
func (f *fakeRepoSource) ListDependents(_ context.Context, _ string) ([]beads.Issue, error) {
	return nil, nil
}

// newMultiForTest builds a MultiBDSource directly from fake subs so
// tests don't have to wire up real bd.Clients. The branchFn is a
// constant so assertions can pin the branch column too.
func newMultiForTest(t *testing.T, subs ...struct {
	name   string
	branch string
	src    *fakeRepoSource
}) *MultiBDSource {
	t.Helper()
	m := &MultiBDSource{}
	for _, s := range subs {
		b := s.branch
		m.subs = append(m.subs, subRepo{
			name:     s.name,
			src:      s.src,
			branchFn: func(_ context.Context) string { return b },
		})
	}
	return m
}

func TestDecorateIssues_StampsRepoAndBranchWhenNameSet(t *testing.T) {
	issues := []beads.Issue{
		{ID: "a-1", Title: "one"},
		{ID: "a-2", Title: "two"},
	}
	decorateIssues(issues, "alpha", func() string { return "main" })
	for _, i := range issues {
		if i.Repo != "alpha" || i.Branch != "main" {
			t.Errorf("issue %s: Repo=%q Branch=%q, want alpha/main", i.ID, i.Repo, i.Branch)
		}
	}
}

func TestDecorateIssues_LeavesUntouchedWhenNameEmpty(t *testing.T) {
	// Empty name = legacy path; the branchFn must not even be
	// called (no git shell-out for callers that opt out of
	// decoration). Side-effect on a counter proves the short-circuit.
	calls := 0
	branchFn := func() string {
		calls++
		return "main"
	}
	issues := []beads.Issue{{ID: "a-1", Title: "one", Repo: "preset", Branch: "preset-branch"}}
	decorateIssues(issues, "", branchFn)
	if calls != 0 {
		t.Errorf("branchFn should not be called when name is empty; got %d calls", calls)
	}
	if issues[0].Repo != "preset" || issues[0].Branch != "preset-branch" {
		t.Errorf("decorateIssues with empty name overwrote existing fields: %+v", issues[0])
	}
}

func TestMultiBDSource_FetchUnionsAndDecorates(t *testing.T) {
	a := &fakeRepoSource{issues: []beads.Issue{
		{ID: "alpha-1", Title: "in alpha"},
		{ID: "alpha-2", Title: "also alpha"},
	}}
	b := &fakeRepoSource{issues: []beads.Issue{
		{ID: "beta-9", Title: "in beta"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"beta", "feat/x", b},
	)

	got, err := m.Fetch(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 unioned issues; got %d", len(got))
	}
	// Each issue should carry its repo and branch.
	for _, i := range got {
		switch i.ID {
		case "alpha-1", "alpha-2":
			if i.Repo != "alpha" || i.Branch != "main" {
				t.Errorf("%s decorated as repo=%q branch=%q, want alpha/main",
					i.ID, i.Repo, i.Branch)
			}
		case "beta-9":
			if i.Repo != "beta" || i.Branch != "feat/x" {
				t.Errorf("%s decorated as repo=%q branch=%q, want beta/feat/x",
					i.ID, i.Repo, i.Branch)
			}
		}
	}
}

func TestMultiBDSource_PartialFailureKeepsGood(t *testing.T) {
	good := &fakeRepoSource{issues: []beads.Issue{{ID: "good-1", Title: "ok"}}}
	bad := &fakeRepoSource{fetchErr: errors.New("bd: workspace gone")}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"good", "main", good},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"bad", "main", bad},
	)

	got, err := m.Fetch(context.Background(), filter.PresetAll)
	if err != nil {
		t.Errorf("partial failure should NOT surface as an error when some repos returned data; got %v", err)
	}
	if len(got) != 1 || got[0].ID != "good-1" {
		t.Errorf("expected just the good repo's issue; got %+v", got)
	}
	// FetchWithSubErrors must surface the silent sub failure —
	// the TUI uses it to render the per-sub banner. Pre-m99 these
	// errors were dropped on the floor.
	_, errs, err2 := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err2 != nil {
		t.Fatalf("partial-failure FetchWithSubErrors: %v", err2)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 fetch error tracked; got %d (%+v)", len(errs), errs)
	}
	if errs[0].Repo != "bad" {
		t.Errorf("fetch error repo = %q, want %q", errs[0].Repo, "bad")
	}
	if errs[0].Err == nil || errs[0].Err.Error() != "bd: workspace gone" {
		t.Errorf("fetch error Err = %v, want \"bd: workspace gone\"", errs[0].Err)
	}
}

func TestMultiBDSource_FetchWithSubErrors_ClearsOnSuccess(t *testing.T) {
	// First Fetch errors on one sub; second Fetch (after the sub
	// "recovers") should return zero errors. The model assumes
	// per-fetch errors are a snapshot of *this* fetch, not a
	// cumulative log.
	sub := &fakeRepoSource{fetchErr: errors.New("transient")}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"flaky", "main", sub},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"ok", "main", &fakeRepoSource{issues: []beads.Issue{{ID: "ok-1"}}}},
	)
	_, errs1, err1 := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err1 != nil {
		t.Fatal(err1)
	}
	if len(errs1) != 1 {
		t.Fatalf("first fetch should have 1 err; got %d", len(errs1))
	}
	sub.fetchErr = nil
	sub.issues = []beads.Issue{{ID: "flaky-1"}}
	_, errs2, err2 := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(errs2) != 0 {
		t.Errorf("second fetch should clear errs; got %d (%+v)", len(errs2), errs2)
	}
}

func TestMultiBDSource_FetchRetriesTransientTimeout(t *testing.T) {
	// A sub that times out ONCE then succeeds must end with its issues
	// and no surfaced error — FetchWithSubErrors retries a transient
	// beads.ErrTimedOut once (the warm second attempt wins), exactly
	// what the user's manual `r` does.
	sub := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1"}}, timeoutThenOK: 1}
	m := newMultiForTest(t, struct {
		name   string
		branch string
		src    *fakeRepoSource
	}{"a", "main", sub})

	issues, subErrs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("a transient timeout should be cleared by the retry; got %v", err)
	}
	if len(subErrs) != 0 {
		t.Errorf("no sub error should survive a successful retry; got %+v", subErrs)
	}
	if len(issues) != 1 || issues[0].ID != "a-1" {
		t.Errorf("expected the retried sub's issue; got %v", idsOf(issues))
	}
	if n := atomic.LoadInt32(&sub.fetchCount); n != 2 {
		t.Errorf("expected 2 Fetch calls (timeout + retry); got %d", n)
	}
}

func TestMultiBDSource_FetchDoesNotRetryNonTimeout(t *testing.T) {
	// A permanent (non-timeout) error must fail fast — one Fetch, no
	// retry, so a genuinely broken repo doesn't double the refresh cost.
	sub := &fakeRepoSource{fetchErr: errors.New("workspace gone")}
	m := newMultiForTest(t, struct {
		name   string
		branch string
		src    *fakeRepoSource
	}{"a", "main", sub})

	_, subErrs, _ := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if len(subErrs) != 1 {
		t.Fatalf("a permanent error should surface as a sub error; got %+v", subErrs)
	}
	if n := atomic.LoadInt32(&sub.fetchCount); n != 1 {
		t.Errorf("a non-timeout error must NOT retry; got %d Fetch calls", n)
	}
}

func TestMultiBDSource_DropsForeignIssueIDs(t *testing.T) {
	// A sub that returns issues with the wrong prefix (the
	// cross-workspace leak symptom — bd serving another
	// workspace's data when this one's .beads is broken). The
	// matching rows survive and get decorated; foreign rows are
	// dropped and surface as a FetchError so the user sees the
	// mis-attribution rather than silently consuming bad data.
	clean := &fakeRepoSource{issues: []beads.Issue{
		{ID: "good-1", Title: "ok-1"},
		{ID: "good-2", Title: "ok-2"},
	}}
	leaky := &fakeRepoSource{issues: []beads.Issue{
		{ID: "elsewhere-x", Title: "leaked from another workspace"},
		{ID: "elsewhere-y", Title: "also leaked"},
		{ID: "leaky-1", Title: "legit row from this workspace"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"good", "main", clean},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"leaky", "main", leaky},
	)
	issues, errs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("FetchWithSubErrors: %v", err)
	}
	// 2 from clean + 1 legit from leaky = 3 total. 2 foreign rows dropped.
	if len(issues) != 3 {
		t.Errorf("expected 3 surviving issues; got %d (%+v)", len(issues), idsOf(issues))
	}
	for _, i := range issues {
		if i.Repo == "" {
			t.Errorf("issue %s left undecorated (Repo empty)", i.ID)
		}
		// No surviving issue should still have a foreign prefix.
		if i.Repo == "leaky" && !strings.HasPrefix(i.ID, "leaky-") {
			t.Errorf("foreign issue leaked through: %s under repo %q", i.ID, i.Repo)
		}
	}
	// FetchError for the leaky sub: mentions the foreign count and the expected prefix.
	if len(errs) != 1 {
		t.Fatalf("expected 1 fetch error; got %d (%+v)", len(errs), errs)
	}
	if errs[0].Repo != "leaky" {
		t.Errorf("fetch error repo = %q, want %q", errs[0].Repo, "leaky")
	}
	if !strings.Contains(errs[0].Err.Error(), "foreign or nested-prefix ID") {
		t.Errorf("fetch error message = %q, want it to mention 'foreign or nested-prefix ID'", errs[0].Err.Error())
	}
	if !strings.Contains(errs[0].Err.Error(), "\"leaky-\"") {
		t.Errorf("fetch error message should name the expected prefix %q; got %q", "leaky-", errs[0].Err.Error())
	}
}

func idsOf(issues []beads.Issue) []string {
	out := make([]string, len(issues))
	for i, x := range issues {
		out[i] = x.ID
	}
	return out
}

func TestMultiBDSource_NestedPrefixCollision(t *testing.T) {
	// Regression: when two registered subs have nested prefixes
	// (e.g. `foo` and `foo-bar`), the naive HasPrefix-on-shorter
	// check accepted `foo-bar-1` under the shorter `foo` sub, so
	// a `foo`-sub leak that included `foo-bar`'s data would
	// mis-attribute rather than be rejected. Longest-prefix-match
	// fixes this — `foo-bar-1` resolves to `foo-bar`, not `foo`.
	short := &fakeRepoSource{issues: []beads.Issue{
		{ID: "foo-1", Title: "real foo row"},
		{ID: "foo-bar-1", Title: "actually belongs to foo-bar"},
	}}
	long := &fakeRepoSource{issues: []beads.Issue{
		{ID: "foo-bar-9", Title: "real foo-bar row"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"foo", "main", short},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"foo-bar", "main", long},
	)
	issues, errs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("FetchWithSubErrors: %v", err)
	}
	// foo-1 (legit foo) + foo-bar-9 (legit foo-bar) survive.
	// foo-bar-1 returned by `foo` sub gets rejected because its
	// longest match is `foo-bar`, not `foo`.
	if len(issues) != 2 {
		t.Errorf("expected 2 surviving issues; got %d (%+v)", len(issues), idsOf(issues))
	}
	for _, i := range issues {
		switch i.ID {
		case "foo-1":
			if i.Repo != "foo" {
				t.Errorf("foo-1 attributed to %q, want foo", i.Repo)
			}
		case "foo-bar-9":
			if i.Repo != "foo-bar" {
				t.Errorf("foo-bar-9 attributed to %q, want foo-bar", i.Repo)
			}
		case "foo-bar-1":
			t.Errorf("foo-bar-1 should have been rejected as nested-prefix leak (returned by `foo` sub, longest match is `foo-bar`)")
		}
	}
	// The short sub should have a FetchError for the rejected row.
	var sawShortErr bool
	for _, e := range errs {
		if e.Repo == "foo" {
			sawShortErr = true
		}
	}
	if !sawShortErr {
		t.Errorf("expected a FetchError for sub `foo` (nested-prefix leak); got %+v", errs)
	}
}

func TestMultiBDSource_SatisfiesMultiSource(t *testing.T) {
	// Compile-time check is already in source.go; this is a runtime
	// type-assert pin so a future refactor that accidentally
	// removed the method would surface here too.
	var src Source = &MultiBDSource{}
	if _, ok := src.(MultiSource); !ok {
		t.Fatal("*MultiBDSource no longer satisfies MultiSource — model's type-assert in fetchCmd will silently fall back to plain Fetch")
	}
}

func TestRenderFetchErrorBanner(t *testing.T) {
	// Banner format pins three regimes: single, few-enough-to-list,
	// truncated. Phrasing matters because the user reads this and
	// the next action they take depends on it (press r vs. wyk
	// doctor). All variants — including the +N-more truncation —
	// must carry the actionable retry hint; the truncated case is
	// when retry is most likely the right move.
	mk := func(names ...string) []FetchError {
		out := make([]FetchError, len(names))
		for i, n := range names {
			// Use distinct error text per repo so the name-list
			// regime is exercised (the same-error coalesce has
			// its own test).
			out[i] = FetchError{Repo: n, Err: errors.New("x-" + n)}
		}
		return out
	}
	cases := []struct {
		name     string
		errs     []FetchError
		contains []string
	}{
		{"single", mk("a"), []string{"1 repo failed", "a", "press r to retry", "wyk doctor"}},
		{"few", mk("a", "b", "c"), []string{"3 repos failed", "a, b, c", "press r to retry", "wyk doctor"}},
		{"many", mk("a", "b", "c", "d", "e"), []string{"5 repos failed", "a, b, c", "+2 more", "press r to retry", "wyk doctor"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderFetchErrorBanner(tc.errs, 0) // width=0 disables truncation
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("banner missing %q in %q", want, got)
				}
			}
		})
	}
}

func TestRenderFetchErrorBanner_CoalescesSameError(t *testing.T) {
	// When every sub fails with the same underlying error (the
	// user's machine is under load, every repo hit the 10s
	// timeout), the banner collapses to "N repos all failed: <err>"
	// — surfacing the shared diagnosis rather than a noisy name
	// list that hides it. n==1 keeps its existing "1 repo failed"
	// phrasing; mixed errors fall through to the per-name list.
	sameTimeout := errors.New("bd list --json: timed out after 10s")
	errs := []FetchError{
		{Repo: "alpha", Err: sameTimeout},
		{Repo: "beta", Err: sameTimeout},
		{Repo: "gamma", Err: sameTimeout},
		{Repo: "delta", Err: sameTimeout},
	}
	got := renderFetchErrorBanner(errs, 0)
	for _, want := range []string{
		"4 repos all failed",
		"timed out after 10s",
		"press r to retry",
		"wyk doctor",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("coalesced banner missing %q in %q", want, got)
		}
	}
	// Per-repo name list must NOT be present — coalesced form is
	// the whole point of the collapse.
	for _, unwanted := range []string{"alpha,", "beta,", "gamma,"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("coalesced banner should not list repo names; found %q in %q", unwanted, got)
		}
	}

	// Mixed errors → fall back to name list (don't collapse a
	// false positive: the user needs to know WHICH repo broke when
	// they don't all share the diagnosis).
	mixed := []FetchError{
		{Repo: "alpha", Err: errors.New("timed out after 10s")},
		{Repo: "beta", Err: errors.New("no workspace")},
	}
	got = renderFetchErrorBanner(mixed, 0)
	if !strings.Contains(got, "alpha, beta") {
		t.Errorf("mixed-error banner should list both repos by name; got %q", got)
	}
	if strings.Contains(got, "all failed") {
		t.Errorf("mixed-error banner should not coalesce; got %q", got)
	}

	// Single error keeps its established phrasing.
	one := []FetchError{{Repo: "alpha", Err: sameTimeout}}
	got = renderFetchErrorBanner(one, 0)
	if !strings.Contains(got, "1 repo failed to load: alpha") {
		t.Errorf("n=1 banner should keep its established phrasing; got %q", got)
	}
}

func TestRenderFetchErrorBanner_TruncatesToWidth(t *testing.T) {
	// Three long-named repos with the full retry tail will exceed
	// a narrow terminal; the banner must cap at width with an
	// ellipsis rather than wrap. The +N-more collapse is by COUNT
	// (n > 3), so width-based truncation is the only guard for the
	// wide-names-but-few-of-them case. Measured in runes — the
	// same semantic trunc uses (rune-aware, so multi-byte names
	// can't be split mid-codepoint).
	errs := []FetchError{
		{Repo: "long-name-repository-one", Err: errors.New("a")},
		{Repo: "long-name-repository-two", Err: errors.New("b")},
		{Repo: "long-name-repository-three", Err: errors.New("c")},
	}
	const width = 60
	got := renderFetchErrorBanner(errs, width)
	if rc := utf8.RuneCountInString(got); rc > width {
		t.Errorf("banner exceeds width: runes=%d, width=%d, banner=%q", rc, width, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis-truncated banner; got %q", got)
	}
}

func TestMultiBDSource_AllFailReturnsFirstError(t *testing.T) {
	a := &fakeRepoSource{fetchErr: errors.New("a broke")}
	b := &fakeRepoSource{fetchErr: errors.New("b broke")}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"a", "main", a},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"b", "main", b},
	)
	_, err := m.Fetch(context.Background(), filter.PresetAll)
	if err == nil {
		t.Fatal("expected error when every sub errored")
	}
}

func TestMultiBDSource_WriteRoutesToCorrectRepo(t *testing.T) {
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1", Title: "a"}}}
	b := &fakeRepoSource{issues: []beads.Issue{{ID: "b-9", Title: "b"}}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"beta", "main", b},
	)
	if _, err := m.Fetch(context.Background(), filter.PresetAll); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// Close on b-9 must route to b, NOT a.
	if err := m.Close(context.Background(), beads.Issue{ID: "b-9", Repo: "beta"}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(a.closed) != 0 {
		t.Errorf("alpha got an unrelated close: %+v", a.closed)
	}
	if len(b.closed) != 1 || b.closed[0] != "b-9" {
		t.Errorf("beta should have received Close(b-9); got %+v", b.closed)
	}

	// Same for AddLabel against a-1.
	if err := m.AddLabel(context.Background(), beads.Issue{ID: "a-1", Repo: "alpha"}, "human"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if len(a.added) != 1 || a.added[0] != (labelOp{"a-1", "human"}) {
		t.Errorf("alpha should have received AddLabel(a-1, human); got %+v", a.added)
	}
	if len(b.added) != 0 {
		t.Errorf("beta got an unrelated AddLabel: %+v", b.added)
	}
}

func TestMultiBDSource_ReopenRoutesAndErrors(t *testing.T) {
	// Reopen goes through the same repoForIssue path as Close;
	// without a dedicated test, the routing was an untested
	// surface even though it's the riskiest part (the panic was a
	// gratuitous `.(Mutator)` assertion that has since been
	// removed). Mirror the Close suite: assert routing to the
	// matching sub, plus the ghost-repo error surface.
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1", Title: "a"}}}
	b := &fakeRepoSource{issues: []beads.Issue{{ID: "b-9", Title: "b"}}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"beta", "main", b},
	)
	if _, err := m.Fetch(context.Background(), filter.PresetAll); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if err := m.Reopen(context.Background(), beads.Issue{ID: "b-9", Repo: "beta"}); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if len(a.reopened) != 0 {
		t.Errorf("alpha got an unrelated reopen: %+v", a.reopened)
	}
	if len(b.reopened) != 1 || b.reopened[0] != "b-9" {
		t.Errorf("beta should have received Reopen(b-9); got %+v", b.reopened)
	}

	// Ghost repo must error rather than silently route somewhere.
	if err := m.Reopen(context.Background(), beads.Issue{ID: "z-99", Repo: "ghost"}); err == nil {
		t.Error("Reopen on unknown Repo should error")
	}
}

func TestMultiBDSource_WriteToUnknownRepoErrors(t *testing.T) {
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1", Title: "a"}}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
	)
	_, _ = m.Fetch(context.Background(), filter.PresetAll)
	// An issue carrying a Repo that doesn't match any registered sub
	// must error rather than silently routing somewhere.
	err := m.Close(context.Background(), beads.Issue{ID: "z-99", Repo: "ghost"})
	if err == nil {
		t.Error("Close on unknown Repo should error so the TUI can surface 'not in registry'")
	}
}

func TestMultiBDSource_WriteWithEmptyRepoErrors(t *testing.T) {
	// Programmer-error guardrail: every in-tree caller obtains the
	// Issue from Source.Fetch which populates Repo. An empty Repo on
	// a multi-repo write is therefore a misuse and must surface
	// loudly rather than silently routing somewhere via a bare-ID
	// lookup (which could mis-route on ID collisions across repos).
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1", Title: "a"}}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
	)
	_, _ = m.Fetch(context.Background(), filter.PresetAll)
	err := m.Close(context.Background(), beads.Issue{ID: "a-1"}) // Repo not set
	if err == nil {
		t.Fatal("Close with empty Repo should error")
	}
	if !strings.Contains(err.Error(), "no Repo set") {
		t.Errorf("error should mention the empty-Repo cause; got %q", err.Error())
	}
	if len(a.closed) != 0 {
		t.Errorf("alpha should not have been routed to; got %+v", a.closed)
	}
}

func TestMultiBDSource_WriteRoutesByRepoNotID(t *testing.T) {
	// Regression for job 1165's MED finding: two workspaces that
	// happen to use the same ID must NOT cross-route. Writes follow
	// Issue.Repo, not a bare ID lookup that the last fetch happened
	// to populate.
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "shared-1", Title: "alpha's"}}}
	b := &fakeRepoSource{issues: []beads.Issue{{ID: "shared-1", Title: "beta's"}}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"alpha", "main", a},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"beta", "main", b},
	)
	if _, err := m.Fetch(context.Background(), filter.PresetAll); err != nil {
		t.Fatal(err)
	}

	// Close on shared-1 with Repo=alpha must hit alpha, not whichever
	// the Fetch loop visited last.
	if err := m.Close(context.Background(), beads.Issue{ID: "shared-1", Repo: "alpha"}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(a.closed) != 1 || a.closed[0] != "shared-1" {
		t.Errorf("alpha should have received Close(shared-1); got %+v", a.closed)
	}
	if len(b.closed) != 0 {
		t.Errorf("beta should NOT have received the close; got %+v", b.closed)
	}

	// And the inverse direction:
	if err := m.Close(context.Background(), beads.Issue{ID: "shared-1", Repo: "beta"}); err != nil {
		t.Fatalf("Close (beta): %v", err)
	}
	if len(b.closed) != 1 || b.closed[0] != "shared-1" {
		t.Errorf("beta should have received Close(shared-1); got %+v", b.closed)
	}
}

func TestIsAgentInboxCandidate(t *testing.T) {
	// Pin the predicate the dep-lookup pass uses to filter rows.
	// Wrong here → we either skip rows that need the HUMAN-BLOCK
	// check or do N pointless bd calls for rows that can't be
	// blocked-by-human.
	cases := []struct {
		name string
		i    beads.Issue
		want bool
	}{
		{
			name: "human-flagged-skipped",
			i:    beads.Issue{Labels: []string{"src:agent", "human"}, DependencyCount: 1},
			want: false,
		},
		{
			name: "no-deps-skipped",
			i:    beads.Issue{Labels: []string{"src:agent"}, DependencyCount: 0},
			want: false,
		},
		{
			name: "no-src-agent-skipped",
			i:    beads.Issue{Labels: []string{"src:human"}, DependencyCount: 2},
			want: false,
		},
		{
			name: "agent-with-deps-and-not-human-is-the-target",
			i:    beads.Issue{Labels: []string{"src:agent"}, DependencyCount: 1},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAgentInboxCandidate(c.i); got != c.want {
				t.Errorf("isAgentInboxCandidate(%+v) = %v, want %v", c.i, got, c.want)
			}
		})
	}
}

func TestMarkBlockedByHuman_NilClientNoOps(t *testing.T) {
	// Single-repo callers without a real Client (e.g. test
	// scaffolding) must not crash markBlockedByHuman; it should
	// return immediately and leave the flag unset.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1},
	}
	markBlockedByHuman(t.Context(), nil, issues, nil)
	if issues[0].BlockedByHuman {
		t.Error("nil client should leave BlockedByHuman = false")
	}
}

// stubDepLister returns canned dep lists per candidate ID. Used to
// pin markBlockedByHuman's dep-scan behaviour without needing a
// real bd binary or workspace.
type stubDepLister struct {
	byID map[string][]beads.Issue
	err  error
}

func (s *stubDepLister) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byID[id], nil
}

func TestMarkBlockedByHuman_FlagsRowsWithHumanLabeledBlocker(t *testing.T) {
	// Three agent-inbox candidates, each with a different dep
	// composition. The dep-scan loop must:
	//   - flag the candidate whose blocker carries `human`
	//   - leave the candidate with a non-human blocker untouched
	//   - leave the candidate whose blockers are mixed flagged
	//     (any human blocker triggers it, not all-must-be-human)
	issues := []beads.Issue{
		{ID: "would-you-kindly-aaa", Labels: []string{"src:agent"}, DependencyCount: 1}, // blocker = human → flag
		{ID: "would-you-kindly-bbb", Labels: []string{"src:agent"}, DependencyCount: 1}, // blocker = non-human → no flag
		{ID: "would-you-kindly-ccc", Labels: []string{"src:agent"}, DependencyCount: 2}, // mixed → flag
	}
	stub := &stubDepLister{
		byID: map[string][]beads.Issue{
			"would-you-kindly-aaa": {
				{ID: "would-you-kindly-xxx", Labels: []string{"human"}},
			},
			"would-you-kindly-bbb": {
				{ID: "would-you-kindly-yyy", Labels: []string{"src:agent"}},
			},
			"would-you-kindly-ccc": {
				{ID: "would-you-kindly-zzz", Labels: []string{"src:human"}},
				{ID: "would-you-kindly-www", Labels: []string{"human"}},
			},
		},
	}
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if !issues[0].BlockedByHuman {
		t.Errorf("aaa: blocker has `human` label → should be flagged")
	}
	if issues[1].BlockedByHuman {
		t.Errorf("bbb: blocker has no `human` label → should NOT be flagged")
	}
	if !issues[2].BlockedByHuman {
		t.Errorf("ccc: at least one blocker has `human` → should be flagged")
	}
}

func TestMarkBlockedByHuman_SkipsNonCandidates(t *testing.T) {
	// Rows that aren't agent-inbox candidates (no src:agent, OR
	// human-flagged, OR no deps) must not trigger a ListDeps call.
	// We assert via a stub that explodes if invoked.
	issues := []beads.Issue{
		{ID: "human-row", Labels: []string{"src:agent", "human"}, DependencyCount: 1},
		{ID: "no-src-row", Labels: []string{"src:human"}, DependencyCount: 1},
		{ID: "no-deps-row", Labels: []string{"src:agent"}, DependencyCount: 0},
	}
	calls := 0
	stub := stubDepListerFunc(func(_ context.Context, id string) ([]beads.Issue, error) {
		calls++
		t.Errorf("ListDeps called for non-candidate %q", id)
		return nil, nil
	})
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if calls != 0 {
		t.Errorf("expected zero ListDeps calls; got %d", calls)
	}
}

type stubDepListerFunc func(ctx context.Context, id string) ([]beads.Issue, error)

func (f stubDepListerFunc) ListDeps(ctx context.Context, id string) ([]beads.Issue, error) {
	return f(ctx, id)
}

func TestNewMultiBDSource_SharesOneDepSemAcrossSubs(t *testing.T) {
	// Regression for the per-workspace-vs-global concurrency cap.
	// NewMultiBDSource must allocate ONE semaphore and thread it
	// into every sub's BDSource.DepSem — otherwise the global
	// bd-subprocess count scales with registry size instead of
	// staying bounded by markBlockedByHumanConcurrency.
	c1 := &beads.Client{}
	c2 := &beads.Client{}
	c3 := &beads.Client{}
	m, err := NewMultiBDSource([]*beads.Client{c1, c2, c3}, []string{"alpha", "beta", "gamma"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.subs) != 3 {
		t.Fatalf("expected 3 subs; got %d", len(m.subs))
	}
	var firstSem chan struct{}
	for i, sub := range m.subs {
		bds, ok := sub.src.(*BDSource)
		if !ok {
			t.Fatalf("sub %d: expected *BDSource, got %T", i, sub.src)
		}
		if bds.DepSem == nil {
			t.Errorf("sub %d: DepSem is nil; multi-source should always populate it", i)
			continue
		}
		if i == 0 {
			firstSem = bds.DepSem
			if cap(firstSem) != markBlockedByHumanConcurrency {
				t.Errorf("DepSem capacity = %d, want %d", cap(firstSem), markBlockedByHumanConcurrency)
			}
		} else if bds.DepSem != firstSem {
			t.Errorf("sub %d: DepSem is a different channel from sub 0; semaphore must be shared so the global concurrent-subprocess cap is enforced", i)
		}
	}
}

func TestFetchConcurrencyFromEnv(t *testing.T) {
	cases := []struct {
		set  string
		want int
	}{
		{"", defaultFetchConcurrency},
		{"8", 8},
		{"1", 1},
		{"0", defaultFetchConcurrency},    // must be >=1
		{"-3", defaultFetchConcurrency},   // must be >=1
		{"lots", defaultFetchConcurrency}, // unparseable
	}
	for _, c := range cases {
		t.Run(c.set, func(t *testing.T) {
			t.Setenv("WYK_FETCH_CONCURRENCY", c.set)
			if got := fetchConcurrencyFromEnv(); got != c.want {
				t.Errorf("fetchConcurrencyFromEnv() with %q = %d, want %d", c.set, got, c.want)
			}
		})
	}
}
