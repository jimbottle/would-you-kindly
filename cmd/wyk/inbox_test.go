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
