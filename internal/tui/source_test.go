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

	// deps, when set, backs ListDeps/ListDependents (keyed by the
	// parent issue id). nil means every lookup returns no edges.
	deps map[string][]beads.Issue

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
// multi-repo routing tests. Edges come from the optional deps map
// (keyed by parent id); the zero value has none, so every issue
// resolves to an empty set — enough to keep the
// MultiBDSource.ListDeps routing test honest without canned data.
func (f *fakeRepoSource) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	return f.deps[id], nil
}

// ListDependents is the reverse-direction twin of ListDeps; same
// deps-map lookup so fakeRepoSource keeps satisfying DepLister /
// fullSource.
func (f *fakeRepoSource) ListDependents(_ context.Context, id string) ([]beads.Issue, error) {
	return f.deps[id], nil
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
	// A sub that returns rows belonging to ANOTHER REGISTERED
	// workspace (the cross-workspace leak symptom — bd serving
	// another workspace's data when this one's .beads is broken).
	// Those rows are dropped and surface as a FetchError so the user
	// sees the mis-attribution rather than silently consuming bad
	// data; this sub's own rows survive and get decorated.
	//
	// Note the leaked IDs are `good-*`: the guard's signal is "some
	// OTHER sub claims this ID", not "the ID lacks my name". An ID no
	// sub claims is this workspace's own bd prefix — see
	// TestMultiBDSource_KeepsRowsWhoseBDPrefixIsNotTheRegistryName.
	clean := &fakeRepoSource{issues: []beads.Issue{
		{ID: "good-1", Title: "ok-1"},
		{ID: "good-2", Title: "ok-2"},
	}}
	leaky := &fakeRepoSource{issues: []beads.Issue{
		{ID: "good-x", Title: "leaked from the `good` workspace"},
		{ID: "good-y", Title: "also leaked"},
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
	if !strings.Contains(errs[0].Err.Error(), "did not carry this workspace's") {
		t.Errorf("fetch error message = %q, want it to name the prefix mismatch", errs[0].Err.Error())
	}
	if !strings.Contains(errs[0].Err.Error(), `"leaky-"`) {
		t.Errorf("fetch error message should name the expected prefix; got %q", errs[0].Err.Error())
	}
	if !strings.Contains(errs[0].Err.Error(), "2 issue(s)") {
		t.Errorf("fetch error message should count the dropped rows; got %q", errs[0].Err.Error())
	}
}

func TestMultiBDSource_KeepsRowsWhoseBDPrefixIsNotTheRegistryName(t *testing.T) {
	// A workspace's bd issue prefix is chosen at `bd init` and is
	// often NOT its directory name — which is where the registry name
	// comes from. Requiring the ID to start with the registry name
	// emptied every such workspace and reported it as a failed repo,
	// e.g. a repo registered as `louisville-open-data-expenditure-bot`
	// (its folder) whose issues are all `louisville-open-data-*` lost
	// every row while bd was serving exactly the right data
	// (would-you-kindly-qp14).
	shortPrefix := &fakeRepoSource{issues: []beads.Issue{
		{ID: "louisville-open-data-7zo", Title: "real row"},
		{ID: "louisville-open-data-715", Title: "another real row"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"louisville-open-data-expenditure-bot", "main", shortPrefix},
	)
	issues, errs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("FetchWithSubErrors: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected both rows to survive; got %d (%+v)", len(issues), idsOf(issues))
	}
	if len(errs) != 0 {
		t.Errorf("a workspace using its own bd prefix is not an error; got %+v", errs)
	}
	for _, i := range issues {
		if i.Repo != "louisville-open-data-expenditure-bot" {
			t.Errorf("%s decorated with Repo=%q, want the registry name", i.ID, i.Repo)
		}
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

func TestMultiBDSource_ListDepsStampsRepo(t *testing.T) {
	// Regression: dep-list rows came back with a blank Repo, but
	// Detail and every Mutator method route on Repo via repoForIssue
	// — so drilling into a dependency in the detail view failed
	// enrichment and surfaced the "has no Repo set" programmer error.
	// Rows must route by their own ID prefix (a dep edge can cross
	// repos); an unclaimed prefix falls back to the queried sub.
	a := &fakeRepoSource{deps: map[string][]beads.Issue{
		"alpha-1": {
			{ID: "alpha-2", Title: "same-repo dep"},
			{ID: "beta-9", Title: "cross-repo dep"},
			{ID: "zzz-1", Title: "foreign-prefix dep"},
		},
	}}
	b := &fakeRepoSource{}
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

	for _, list := range []func(context.Context, string) ([]beads.Issue, error){
		m.ListDeps, m.ListDependents,
	} {
		deps, err := list(context.Background(), "alpha-1")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"alpha-2": "alpha", "beta-9": "beta", "zzz-1": "alpha"}
		if len(deps) != len(want) {
			t.Fatalf("expected %d rows, got %+v", len(want), deps)
		}
		for _, d := range deps {
			if d.Repo != want[d.ID] {
				t.Errorf("dep %s: Repo=%q, want %q", d.ID, d.Repo, want[d.ID])
			}
		}
		// The stamped rows must now route: a Mutator write on the
		// same-repo dep should reach alpha without the "no Repo set"
		// error a blank Repo used to produce.
		if err := m.AddLabel(context.Background(), deps[0], "human"); err != nil {
			t.Errorf("AddLabel on stamped dep row: %v", err)
		}
	}
	if len(a.added) != 2 {
		t.Errorf("alpha should have received both AddLabel calls; got %+v", a.added)
	}
	// The sub-source's own rows must be untouched: stamping works on a
	// copy. This also keeps the ListDependents leg above honest — the
	// fake serves the same slice to both listers, so an in-place stamp
	// during the ListDeps leg would leave the second leg pre-stamped
	// and its assertions vacuous.
	for _, d := range a.deps["alpha-1"] {
		if d.Repo != "" {
			t.Errorf("sub-source row %s was mutated in place (Repo=%q); stampRepos must copy", d.ID, d.Repo)
		}
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

// stubDepLister returns canned dep edges per candidate ID, plus the
// blocker issues those edges point at. Used to pin
// markBlockedByHuman's behaviour without needing a real bd binary or
// workspace. batchCalls/lookupCalls count subprocess-equivalents so a
// test can assert the fan-out stays batched.
// Counters are atomic because markBlockedByHuman's unattributable
// fallback calls ListDeps from several goroutines at once; plain ++
// here is a data race that `make check`'s -race gate catches only
// intermittently (roborev #4034). byID is written once at
// construction and only read afterwards, so it needs no lock.
type stubDepLister struct {
	byID      map[string][]beads.Issue // candidate ID → its blocker issues
	err       error                    // returned by ListDepsBatch/ListByIDs
	singleErr error                    // returned by the per-issue ListDeps fallback
	batchIDs  []string                 // ids passed to the last ListDepsBatch
	batch     atomic.Int32
	lookups   atomic.Int32
	singles   atomic.Int32
	onBatch   func() // fired inside ListDepsBatch, to simulate mid-flight events
}

func (s *stubDepLister) ListDepsBatch(_ context.Context, ids []string) (map[string][]beads.Dependency, error) {
	s.batch.Add(1)
	s.batchIDs = append([]string(nil), ids...)
	if s.onBatch != nil {
		s.onBatch()
	}
	if s.err != nil {
		return nil, s.err
	}
	out := map[string][]beads.Dependency{}
	for _, id := range ids {
		for _, b := range s.byID[id] {
			out[id] = append(out[id], beads.Dependency{IssueID: id, DependsOnID: b.ID, Type: "blocks"})
		}
	}
	return out, nil
}

func (s *stubDepLister) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	s.singles.Add(1)
	if s.singleErr != nil {
		return nil, s.singleErr
	}
	return s.byID[id], nil
}

func (s *stubDepLister) ListByIDs(_ context.Context, ids []string) ([]beads.Issue, error) {
	s.lookups.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []beads.Issue
	for _, blockers := range s.byID {
		for _, b := range blockers {
			if want[b.ID] {
				out = append(out, b)
			}
		}
	}
	return out, nil
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
	// The whole point of the rewrite: ONE dep call for all three
	// candidates, not one per row (would-you-kindly-3frr).
	if stub.batch.Load() != 1 {
		t.Errorf("ListDepsBatch called %d times, want exactly 1 for the whole slice", stub.batch.Load())
	}
	if len(stub.batchIDs) != 3 {
		t.Errorf("batch asked about %d ids, want all 3 candidates in one call", len(stub.batchIDs))
	}
	// None of the blockers are in the fetched slice, so exactly one
	// label-resolution round-trip is expected — never one per blocker.
	if stub.lookups.Load() != 1 {
		t.Errorf("ListByIDs called %d times, want exactly 1", stub.lookups.Load())
	}
}

func TestMarkBlockedByHuman_ResolvesBlockersAlreadyInTheFetchedSet(t *testing.T) {
	// When the blocker is already on screen, its labels are in hand
	// and no second bd call is needed at all.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1},
		{ID: "a-blocker", Labels: []string{"human", "src:agent"}},
	}
	stub := &stubDepLister{byID: map[string][]beads.Issue{
		"a-1": {{ID: "a-blocker"}}, // no labels here — they come from the slice
	}}
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if !issues[0].BlockedByHuman {
		t.Error("a-1's blocker is a human row in the same fetch → should be flagged")
	}
	if stub.lookups.Load() != 0 {
		t.Errorf("ListByIDs called %d times; a blocker already in the slice needs no lookup", stub.lookups.Load())
	}
}

func TestMarkBlockedByHuman_DepErrorLosesOnlyTheBadge(t *testing.T) {
	// Best-effort contract: a failing bd call must not panic or
	// corrupt the fetch, just leave the badge off.
	issues := []beads.Issue{{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1}}
	stub := &stubDepLister{err: errors.New("bd: timed out"), byID: map[string][]beads.Issue{
		"a-1": {{ID: "x", Labels: []string{"human"}}},
	}}
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if issues[0].BlockedByHuman {
		t.Error("a failed dep lookup must leave BlockedByHuman false, not guess")
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
	stub := &explodingDepLister{t: t}
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if stub.calls != 0 {
		t.Errorf("expected zero bd calls; got %d", stub.calls)
	}
}

// explodingDepLister fails the test if markBlockedByHuman shells out
// at all — used to prove the no-candidate path costs nothing.
type explodingDepLister struct {
	t     *testing.T
	calls int
}

func (e *explodingDepLister) ListDepsBatch(_ context.Context, ids []string) (map[string][]beads.Dependency, error) {
	e.calls++
	e.t.Errorf("ListDepsBatch called for non-candidates %v", ids)
	return nil, nil
}

func (e *explodingDepLister) ListByIDs(_ context.Context, ids []string) ([]beads.Issue, error) {
	e.calls++
	e.t.Errorf("ListByIDs called for non-candidates %v", ids)
	return nil, nil
}

func (e *explodingDepLister) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	e.calls++
	e.t.Errorf("ListDeps called for non-candidate %q", id)
	return nil, nil
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

func TestMarkBlockedByHuman_UsesEmbeddedEdgesWithoutAnyBDCall(t *testing.T) {
	// bd embeds each issue's edge set in `bd list`/`bd ready`, so the
	// default and ready presets already carry what this scan needs —
	// asking bd again re-fetches data we were handed (roborev #4031).
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1,
			Dependencies: []beads.Dependency{{IssueID: "a-1", DependsOnID: "a-blocker", Type: "blocks"}}},
		{ID: "a-blocker", Labels: []string{"human", "src:agent"}},
	}
	stub := &explodingDepLister{t: t} // any bd call fails the test
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if !issues[0].BlockedByHuman {
		t.Error("embedded edge to a human row should flag a-1")
	}
	if stub.calls != 0 {
		t.Errorf("made %d bd calls; embedded edges + an in-slice blocker need zero", stub.calls)
	}
}

func TestMarkBlockedByHuman_FallsBackWhenTheBatchIsUnattributable(t *testing.T) {
	// bd answers a multi-id `dep list` in the single-issue shape when
	// any id fails to resolve, which is unattributable. Losing every
	// badge in the workspace there would be worse than the per-issue
	// fan-out this replaced, so fall back to it (roborev #4031).
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1},
		{ID: "a-2", Labels: []string{"src:agent"}, DependencyCount: 1},
	}
	stub := &stubDepLister{
		err: beads.ErrUnattributableDeps, // ListDepsBatch fails this way
		byID: map[string][]beads.Issue{
			"a-1": {{ID: "x", Labels: []string{"human"}}},
			"a-2": {{ID: "y", Labels: []string{"src:agent"}}},
		},
	}
	markBlockedByHuman(context.Background(), stub, issues, nil)
	if !issues[0].BlockedByHuman {
		t.Error("a-1's human blocker should survive the fallback")
	}
	if issues[1].BlockedByHuman {
		t.Error("a-2 has no human blocker and must stay unflagged")
	}
	if stub.singles.Load() != 2 {
		t.Errorf("per-issue fallback ran %d times, want one per candidate", stub.singles.Load())
	}
}

func TestMultiBDSource_UsesBDsRealPrefixForTheLeakGuard(t *testing.T) {
	// With the workspace's true bd prefix in hand the guard is exact
	// in BOTH directions — rows that aren't ours are dropped whoever
	// they belong to, registered or not. That matters beyond display:
	// repoForIssue routes WRITES by Issue.Repo, so a leaked row shown
	// under the wrong repo would send a close to the wrong workspace
	// (roborev #4031).
	src := &fakeRepoSource{issues: []beads.Issue{
		{ID: "short-1", Title: "ours"},
		{ID: "unregistered-elsewhere-1", Title: "leaked from a workspace wyk doesn't know"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"folder-name-differs", "main", src},
	)
	// bd reports the real prefix, which is neither the folder name nor
	// any registered name.
	m.subs[0].prefixFn = func(context.Context) string { return "short" }

	issues, errs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("FetchWithSubErrors: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "short-1" {
		t.Fatalf("expected only the workspace's own row; got %+v", idsOf(issues))
	}
	if len(errs) != 1 {
		t.Fatalf("the unregistered leak should surface as a fetch error; got %+v", errs)
	}
	if !strings.Contains(errs[0].Err.Error(), `"short-"`) {
		t.Errorf("error should name bd's real prefix; got %q", errs[0].Err.Error())
	}
}

func TestMultiBDSource_NestedPrefixesStillResolveToTheLongestOwner(t *testing.T) {
	// Two workspaces whose bd prefixes nest. A row belonging to the
	// CHILD that leaks into the parent's fetch must be dropped: a bare
	// HasPrefix(id, "wyk-") keeps it, stamps Repo=wyk, and a later
	// close then dispatches against the wrong workspace — the exact
	// wrong-workspace write the exact-prefix guard exists to stop
	// (roborev #4033).
	parent := &fakeRepoSource{issues: []beads.Issue{
		{ID: "wyk-1", Title: "parent's own row"},
		{ID: "wyk-web-1", Title: "leaked from the nested child"},
	}}
	child := &fakeRepoSource{issues: []beads.Issue{
		{ID: "wyk-web-2", Title: "child's own row"},
	}}
	m := newMultiForTest(t,
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"wyk", "main", parent},
		struct {
			name   string
			branch string
			src    *fakeRepoSource
		}{"wyk-web", "main", child},
	)
	m.subs[0].prefixFn = func(context.Context) string { return "wyk" }
	m.subs[1].prefixFn = func(context.Context) string { return "wyk-web" }

	issues, errs, err := m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("FetchWithSubErrors: %v", err)
	}
	for _, i := range issues {
		if i.ID == "wyk-web-1" && i.Repo == "wyk" {
			t.Errorf("child's row leaked into the parent as Repo=%q — a close would hit the wrong workspace", i.Repo)
		}
	}
	if len(issues) != 2 {
		t.Errorf("expected wyk-1 and wyk-web-2 only; got %v", idsOf(issues))
	}
	if len(errs) != 1 || errs[0].Repo != "wyk" {
		t.Errorf("the leak should surface as a fetch error on wyk; got %+v", errs)
	}
}

func TestMemoPrefix_RetriesTransientButSettlesPermanent(t *testing.T) {
	// A cold-start timeout is transient — the fetch beside it retries
	// for the same reason. Memoizing it would silently downgrade the
	// workspace to the weaker name-based guard for the life of the
	// process (roborev #4033).
	t.Run("transient failure retries", func(t *testing.T) {
		calls := 0
		fn := memoPrefix(func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", fmt.Errorf("bd config get: %w", beads.ErrTimedOut)
			}
			return "resolved", nil
		})
		if got := fn(context.Background()); got != "" {
			t.Errorf("first (timed-out) probe = %q, want empty", got)
		}
		if got := fn(context.Background()); got != "resolved" {
			t.Errorf("retry = %q, want the resolved prefix", got)
		}
		if calls != 2 {
			t.Errorf("probe ran %d times, want a retry after the timeout", calls)
		}
	})
	t.Run("transient failures are capped", func(t *testing.T) {
		// Retrying forever means a chronically-slow workspace burns a
		// full 10s fetch slot on every refresh, ahead of its own cache
		// fast path, starving the other subs (roborev #4034).
		calls := 0
		fn := memoPrefix(func(context.Context) (string, error) {
			calls++
			return "", fmt.Errorf("bd config get: %w", beads.ErrTimedOut)
		})
		for i := 0; i < maxTransientPrefixProbes+5; i++ {
			fn(context.Background())
		}
		if calls != maxTransientPrefixProbes {
			t.Errorf("probe ran %d times, want it to settle after %d timeouts", calls, maxTransientPrefixProbes)
		}
	})
	t.Run("a canceled refresh does not burn an attempt", func(t *testing.T) {
		// Cancellation says nothing about the workspace, so it must
		// not count toward the cap.
		calls := 0
		fn := memoPrefix(func(context.Context) (string, error) {
			calls++
			return "", errors.New("canceled")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for i := 0; i < maxTransientPrefixProbes+2; i++ {
			fn(ctx)
		}
		if calls != maxTransientPrefixProbes+2 {
			t.Errorf("probe ran %d times; a canceled ctx must not consume the retry budget", calls)
		}
	})
	t.Run("permanent failure settles", func(t *testing.T) {
		calls := 0
		fn := memoPrefix(func(context.Context) (string, error) {
			calls++
			return "", errors.New("unknown command")
		})
		fn(context.Background())
		fn(context.Background())
		if calls != 1 {
			t.Errorf("probe ran %d times; a permanent failure must not re-ask every refresh", calls)
		}
	})
	t.Run("success is memoized", func(t *testing.T) {
		calls := 0
		fn := memoPrefix(func(context.Context) (string, error) {
			calls++
			return "p", nil
		})
		fn(context.Background())
		fn(context.Background())
		if calls != 1 || fn(context.Background()) != "p" {
			t.Errorf("probe ran %d times, want exactly 1", calls)
		}
	})
}

func TestMarkBlockedByHuman_FallbackStopsOnCancel(t *testing.T) {
	// A canceled fetch must stop dispatching per-issue fallbacks
	// rather than walking every candidate (roborev #4033).
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, DependencyCount: 1},
		{ID: "a-2", Labels: []string{"src:agent"}, DependencyCount: 1},
		{ID: "a-3", Labels: []string{"src:agent"}, DependencyCount: 1},
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel DURING the batch call, not before: a pre-canceled context
	// returns at the semaphore and never reaches the fallback loop, so
	// the test would pass without exercising the break at all.
	stub := &stubDepLister{
		err:     beads.ErrUnattributableDeps,
		byID:    map[string][]beads.Issue{},
		onBatch: cancel,
	}
	defer cancel()
	markBlockedByHuman(ctx, stub, issues, nil)
	if stub.batch.Load() != 1 {
		t.Fatalf("batch ran %d times; the test must reach the fallback path", stub.batch.Load())
	}
	if stub.singles.Load() != 0 {
		t.Errorf("dispatched %d per-issue fallbacks after cancellation, want 0", stub.singles.Load())
	}
}

// concurrencyProbe is a depLister that records the PEAK number of
// simultaneous bd calls, so a test can assert the shared budget
// actually bounds them.
type concurrencyProbe struct {
	cur, max atomic.Int32
	blockers []beads.Issue
}

func (p *concurrencyProbe) enter() func() {
	n := p.cur.Add(1)
	for {
		old := p.max.Load()
		if n <= old || p.max.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond) // hold the slot long enough to overlap
	return func() { p.cur.Add(-1) }
}

func (p *concurrencyProbe) ListDepsBatch(context.Context, []string) (map[string][]beads.Dependency, error) {
	defer p.enter()()
	return nil, beads.ErrUnattributableDeps
}
func (p *concurrencyProbe) ListByIDs(context.Context, []string) ([]beads.Issue, error) {
	defer p.enter()()
	return nil, nil
}
func (p *concurrencyProbe) ListDeps(context.Context, string) ([]beads.Issue, error) {
	defer p.enter()()
	return p.blockers, nil
}

func TestMarkBlockedByHuman_FallbackHonoursTheSharedBudget(t *testing.T) {
	// The fallback's width must be drawn from the SHARED semaphore.
	// A per-invocation cap would let each of the N token-holders spawn
	// its own extra subprocesses, multiplying the global bd bound that
	// MultiBDSource's shared DepSem exists to enforce instead of
	// capping it (roborev #4034).
	const budget = 2
	issues := make([]beads.Issue, 12)
	for i := range issues {
		issues[i] = beads.Issue{
			ID:              fmt.Sprintf("a-%d", i),
			Labels:          []string{"src:agent"},
			DependencyCount: 1,
		}
	}
	probe := &concurrencyProbe{}
	sem := make(chan struct{}, budget)
	markBlockedByHuman(context.Background(), probe, issues, sem)
	if got := probe.max.Load(); got > budget {
		t.Errorf("peak %d concurrent bd calls exceeds the shared budget of %d", got, budget)
	}
	if probe.max.Load() == 0 {
		t.Error("test made no bd calls at all — it is not exercising the fallback")
	}
}

func TestMultiBDSource_CloseManyGroupsByRepoAndRoutesFailures(t *testing.T) {
	a := &fakeRepoSource{issues: []beads.Issue{{ID: "a-1"}, {ID: "a-2"}}}
	b := &fakeRepoSource{issues: []beads.Issue{{ID: "b-1"}}}
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
	failed := m.CloseMany(context.Background(), []beads.Issue{
		{ID: "a-1", Repo: "alpha"},
		{ID: "b-1", Repo: "beta"},
		{ID: "a-2", Repo: "alpha"},
		{ID: "x-1", Repo: "nowhere"},
	})
	// fakeRepoSource has no CloseMany, so the per-repo fallback closes
	// each issue on its own sub — grouped, never cross-routed.
	if got := strings.Join(a.closed, ","); got != "a-1,a-2" {
		t.Errorf("alpha closed %q, want a-1,a-2", got)
	}
	if got := strings.Join(b.closed, ","); got != "b-1" {
		t.Errorf("beta closed %q, want b-1", got)
	}
	if len(failed) != 1 || failed[0].Issue.ID != "x-1" {
		t.Fatalf("the unregistered repo's issue must come back as a failure; got %+v", failed)
	}
}
