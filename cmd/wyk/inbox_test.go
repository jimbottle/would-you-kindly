package main

import (
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

// mixedRepoInbox returns a slice mimicking fetchInbox's
// unsorted, repo-concatenated output so the limitByPriority
// tests can exercise the production sort+truncate against a
// realistic shape.
func mixedRepoInbox() []beads.Issue {
	return []beads.Issue{
		{ID: "r1-c", Priority: 3, Repo: "r1"},
		{ID: "r1-a", Priority: 0, Repo: "r1"},
		{ID: "r1-b", Priority: 2, Repo: "r1"},
		{ID: "r2-y", Priority: 1, Repo: "r2"},
		{ID: "r2-x", Priority: 0, Repo: "r2"},
	}
}

func TestLimitByPriority(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  []string // expected order; nil = same as input
	}{
		{"top-3 by priority across repos", 3, []string{"r1-a", "r2-x", "r2-y"}},
		{"top-1 picks lowest priority + lowest ID tiebreak", 1, []string{"r1-a"}},
		{"limit zero empties the result", 0, []string{}},
		{"limit -1 returns input unchanged (no sort)", -1, nil},
		{"limit at len returns input unchanged (no sort)", 5, nil},
		{"limit > len returns input unchanged (no sort)", 99, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := mixedRepoInbox()
			got := limitByPriority(in, tc.limit)
			gotIDs := idsOf(got)
			var want []string
			if tc.want == nil {
				want = idsOf(mixedRepoInbox())
			} else {
				want = tc.want
			}
			if len(gotIDs) != len(want) {
				t.Fatalf("len=%d, want %d (got %v, want %v)", len(gotIDs), len(want), gotIDs, want)
			}
			for i := range want {
				if gotIDs[i] != want[i] {
					t.Errorf("position %d: got %q, want %q (full got=%v, want=%v)", i, gotIDs[i], want[i], gotIDs, want)
				}
			}
		})
	}
}

