package main

import (
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestUnassignedIssueIDs(t *testing.T) {
	issues := []beads.Issue{
		{ID: "a-1", Assignee: "jimbottle"},
		{ID: "a-2"},                // empty
		{ID: "a-3", Assignee: " "}, // whitespace-only counts as unassigned
		{ID: "a-4", Assignee: "someone"},
	}
	got := unassignedIssueIDs(issues)
	want := []string{"a-2", "a-3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("unassignedIssueIDs = %v, want %v", got, want)
	}
	if len(unassignedIssueIDs(nil)) != 0 {
		t.Error("nil issues should yield no unassigned IDs")
	}
}

func TestSummarizeIDs(t *testing.T) {
	if got := summarizeIDs([]string{"a", "b"}, 10); got != "a, b" {
		t.Errorf("under cap = %q, want %q", got, "a, b")
	}
	got := summarizeIDs([]string{"a", "b", "c", "d"}, 2)
	if got != "a, b (+2 more)" {
		t.Errorf("over cap = %q, want %q", got, "a, b (+2 more)")
	}
}
