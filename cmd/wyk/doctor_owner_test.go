package main

import (
	"strings"
	"testing"

	"github.com/raylytics/would-you-kindly/internal/beads"
)

// TestUnbadgedIssueIDs checks the owner-badge filter: an issue is
// "unowned" (TUI owner column blank) iff it carries neither the `human`
// label nor `src:agent` — mirroring responsibilityBadgeFor's blank case.
func TestUnbadgedIssueIDs(t *testing.T) {
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}},          // AGENT badge
		{ID: "a-2", Labels: []string{"human"}},              // HUMAN badge
		{ID: "a-3"},                                         // no labels → blank → flagged
		{ID: "a-4", Labels: []string{"priority:hi"}},        // unrelated label → blank → flagged
		{ID: "a-5", Labels: []string{"human", "src:agent"}}, // HUMAN badge
	}
	got := unbadgedIssueIDs(issues)
	want := []string{"a-3", "a-4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("unbadgedIssueIDs = %v, want %v", got, want)
	}
	if len(unbadgedIssueIDs(nil)) != 0 {
		t.Error("nil issues should yield none")
	}
}

func TestSummarizeIDs(t *testing.T) {
	if got := summarizeIDs([]string{"a", "b"}, 10); got != "a, b" {
		t.Errorf("under cap = %q, want %q", got, "a, b")
	}
	if got := summarizeIDs([]string{"a", "b", "c", "d"}, 2); got != "a, b (+2 more)" {
		t.Errorf("over cap = %q, want %q", got, "a, b (+2 more)")
	}
}
