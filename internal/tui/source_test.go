package tui

import (
	"context"
	"errors"
	"testing"

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

// newMultiForTest builds a MultiBDSource directly from fake subs so
// tests don't have to wire up real bd.Clients. The branchFn is a
// constant so assertions can pin the branch column too.
func newMultiForTest(t *testing.T, subs ...struct {
	name   string
	branch string
	src    *fakeRepoSource
}) *MultiBDSource {
	t.Helper()
	m := &MultiBDSource{idToRepo: map[string]string{}}
	for _, s := range subs {
		b := s.branch
		m.subs = append(m.subs, subRepo{
			name:     s.name,
			src:      s.src,
			branchFn: func() string { return b },
		})
	}
	return m
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

func TestMultiBDSource_WriteToUnknownIDErrors(t *testing.T) {
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
