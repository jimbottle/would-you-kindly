package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// fakeRepoSource implements both Source and Mutator so it can stand
// in for a single-repo BDSource inside MultiBDSource. Each instance
// records the writes routed to it so the multi-source test can
// assert routing.
type fakeRepoSource struct {
	issues    []beads.Issue
	fetchErr  error
	closed    []string
	added     []labelOp
	removed   []labelOp
	notes     []labelOp
}

func (f *fakeRepoSource) Fetch(_ context.Context, _ filter.Preset) ([]beads.Issue, error) {
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
func (f *fakeRepoSource) Create(_ context.Context, _, title string) (string, error) {
	// Stub returns a fake ID derived from the title so tests can
	// assert which sub got routed to without wiring a real bd.
	return "new-" + title, nil
}
func (f *fakeRepoSource) Detail(_ context.Context, i beads.Issue) (beads.Issue, error) {
	// Stub echoes the input back with a fixed Notes field so tests
	// can verify the Detail call reached the right sub.
	i.Notes = "stub notes from " + i.Repo
	return i, nil
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
	decorateIssues(issues, "alpha", func() string { return "main" }, true)
	for _, i := range issues {
		if i.Repo != "alpha" || i.Branch != "main" {
			t.Errorf("issue %s: Repo=%q Branch=%q, want alpha/main", i.ID, i.Repo, i.Branch)
		}
		if !i.WykHooked {
			t.Errorf("issue %s: WykHooked=false, want true", i.ID)
		}
	}
}

func TestWykHookInstalled(t *testing.T) {
	// Detection runs `git rev-parse --git-path hooks/post-commit`
	// so each case needs a real git repo. Pins the substring the
	// TUI greps for ("wyk hook post-commit") and the three failure
	// modes (foreign hook, missing hook file, non-git directory),
	// plus a gitlink regression where `.git` is a file pointing
	// into a parent repo's git dir — the exact layout that broke
	// the literal-`.git`-dir read this function originally used.
	gitInit := func(t *testing.T, dir string) {
		t.Helper()
		if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
			t.Skipf("git init failed (git not on PATH?): %v", err)
		}
	}
	writeHook := func(t *testing.T, hookPath, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"plain-wyk", "#!/bin/sh\nexec wyk hook post-commit\n", true},
		{"chained-wyk", "#!/bin/sh\n./.git/hooks/post-commit.pre-wyk\nexec wyk hook post-commit\n", true},
		{"foreign", "#!/bin/sh\nexec roborev hook post-commit\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			writeHook(t, filepath.Join(dir, ".git", "hooks", "post-commit"), tc.body)
			if got := wykHookInstalled(dir); got != tc.want {
				t.Errorf("wykHookInstalled(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
	t.Run("missing-hook-file", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		if got := wykHookInstalled(dir); got {
			t.Errorf("wykHookInstalled with no hook file = true, want false")
		}
	})
	t.Run("not-a-git-repo", func(t *testing.T) {
		// rev-parse errors out cleanly when dir isn't a git repo
		// at all; we treat that as "not installed".
		if got := wykHookInstalled(t.TempDir()); got {
			t.Errorf("wykHookInstalled on non-git dir = true, want false")
		}
	})
	t.Run("empty-dir-string", func(t *testing.T) {
		if got := wykHookInstalled(""); got {
			t.Errorf("wykHookInstalled(\"\") = true, want false")
		}
	})
	t.Run("gitlink-subdir", func(t *testing.T) {
		// Subdir whose `.git` is a file containing `gitdir: <path>`
		// — the layout `git worktree add` and submodules create.
		// The hook lives under the resolved git dir, not under
		// `<subdir>/.git/hooks/`. Pre-fix this returned false; the
		// new rev-parse-based resolver should find it.
		parent := t.TempDir()
		gitInit(t, parent)
		sub := filepath.Join(parent, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		// gitdir: must point at the parent's .git directory.
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+filepath.Join(parent, ".git")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeHook(t, filepath.Join(parent, ".git", "hooks", "post-commit"), "#!/bin/sh\nexec wyk hook post-commit\n")
		if got := wykHookInstalled(sub); !got {
			t.Errorf("wykHookInstalled on gitlink subdir = false, want true (hook in parent's git dir was missed)")
		}
	})
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
	decorateIssues(issues, "", branchFn, false)
	if calls != 0 {
		t.Errorf("branchFn should not be called when name is empty; got %d calls", calls)
	}
	if issues[0].Repo != "preset" || issues[0].Branch != "preset-branch" {
		t.Errorf("decorateIssues with empty name overwrote existing fields: %+v", issues[0])
	}
}

func TestMultiBDSource_FetchUnionsAndDecorates(t *testing.T) {
	a := &fakeRepoSource{issues: []beads.Issue{
		{ID: "a-1", Title: "in alpha"},
		{ID: "a-2", Title: "also alpha"},
	}}
	b := &fakeRepoSource{issues: []beads.Issue{
		{ID: "b-9", Title: "in beta"},
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
		case "a-1", "a-2":
			if i.Repo != "alpha" || i.Branch != "main" {
				t.Errorf("%s decorated as repo=%q branch=%q, want alpha/main",
					i.ID, i.Repo, i.Branch)
			}
		case "b-9":
			if i.Repo != "beta" || i.Branch != "feat/x" {
				t.Errorf("%s decorated as repo=%q branch=%q, want beta/feat/x",
					i.ID, i.Repo, i.Branch)
			}
		}
	}
}

func TestMultiBDSource_PartialFailureKeepsGood(t *testing.T) {
	good := &fakeRepoSource{issues: []beads.Issue{{ID: "ok-1", Title: "ok"}}}
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
	if len(got) != 1 || got[0].ID != "ok-1" {
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
			out[i] = FetchError{Repo: n, Err: errors.New("x")}
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

func TestRenderFetchErrorBanner_TruncatesToWidth(t *testing.T) {
	// Three long-named repos with the full retry tail will exceed
	// a narrow terminal; the banner must cap at width with an
	// ellipsis rather than wrap. The +N-more collapse is by COUNT
	// (n > 3), so width-based truncation is the only guard for the
	// wide-names-but-few-of-them case. Measured in runes — the
	// same semantic trunc uses (rune-aware, so multi-byte names
	// can't be split mid-codepoint).
	errs := []FetchError{
		{Repo: "long-name-repository-one", Err: errors.New("x")},
		{Repo: "long-name-repository-two", Err: errors.New("x")},
		{Repo: "long-name-repository-three", Err: errors.New("x")},
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
