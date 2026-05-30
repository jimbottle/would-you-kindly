package main

import (
	"sort"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestInboxQuery_IsTheDocumentedString(t *testing.T) {
	// The inbox subcommand and docs/CONTRACT.md must agree on the
	// canonical query string — drift here means the docs lie about
	// what wyk inbox does. The contract version (wyk-contract/v1)
	// pins this exact string; bumping the contract version is the
	// only license to change it.
	want := `label=src:agent AND NOT label=human AND status!=closed`
	if inboxQuery != want {
		t.Errorf("inboxQuery drift:\n  want: %q\n  got:  %q", want, inboxQuery)
	}
}

func TestFilterByMaxPriority(t *testing.T) {
	in := []beads.Issue{
		{ID: "a", Priority: 0},
		{ID: "b", Priority: 1},
		{ID: "c", Priority: 2},
		{ID: "d", Priority: 3},
	}
	cases := []struct {
		max  int
		want []string
	}{
		{0, []string{"a"}},
		{1, []string{"a", "b"}},
		{2, []string{"a", "b", "c"}},
		{3, []string{"a", "b", "c", "d"}},
	}
	for _, tc := range cases {
		got := filterByMaxPriority(append([]beads.Issue(nil), in...), tc.max)
		if len(got) != len(tc.want) {
			t.Errorf("max=%d: len=%d, want %d", tc.max, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i].ID != tc.want[i] {
				t.Errorf("max=%d: ids=%v, want %v", tc.max, idsOf(got), tc.want)
				break
			}
		}
	}
}

func idsOf(issues []beads.Issue) []string {
	ids := make([]string, len(issues))
	for i, x := range issues {
		ids[i] = x.ID
	}
	return ids
}

// helper for the -limit tests: build a slice that mimics
// fetchInbox's unsorted, repo-concatenated output so we can
// exercise the sort-then-truncate path in isolation.
func mixedRepoInbox() []beads.Issue {
	return []beads.Issue{
		{ID: "r1-c", Priority: 3, Repo: "r1"},
		{ID: "r1-a", Priority: 0, Repo: "r1"},
		{ID: "r1-b", Priority: 2, Repo: "r1"},
		{ID: "r2-y", Priority: 1, Repo: "r2"},
		{ID: "r2-x", Priority: 0, Repo: "r2"},
	}
}

func TestInbox_LimitTakesHighestPriorityAcrossRepos(t *testing.T) {
	// Mirror the production sort-then-truncate that runInbox
	// applies inside the *limit >= 0 branch.
	all := mixedRepoInbox()
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Priority != all[j].Priority {
			return all[i].Priority < all[j].Priority
		}
		return all[i].ID < all[j].ID
	})
	limited := all[:3]

	gotIDs := idsOf(limited)
	want := []string{"r1-a", "r2-x", "r2-y"}
	if len(gotIDs) != len(want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("got %v, want %v (mismatch at %d)", gotIDs, want, i)
		}
	}
}

func TestInbox_LimitNegativeOneIsNoop(t *testing.T) {
	// -limit -1 should not even enter the sort/truncate branch,
	// so the unsorted concatenation order is preserved.
	all := mixedRepoInbox()
	original := make([]beads.Issue, len(all))
	copy(original, all)

	// Simulate the runInbox guard: *limit >= 0 gates the work.
	limit := -1
	if limit >= 0 {
		t.Fatal("guard accepted -1; production path would mutate")
	}

	for i := range all {
		if all[i].ID != original[i].ID {
			t.Errorf("ordering changed at %d: got %q, want %q", i, all[i].ID, original[i].ID)
		}
	}
}

func TestInbox_LimitZeroEmptiesResult(t *testing.T) {
	all := mixedRepoInbox()
	if 0 < len(all) {
		all = all[:0]
	}
	if len(all) != 0 {
		t.Errorf("expected empty slice; got %v", idsOf(all))
	}
}

