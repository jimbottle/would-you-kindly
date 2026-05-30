package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestEmitDashboardTable_ColumnsAndTotals(t *testing.T) {
	rows := []dashboardRow{
		{Name: "alpha", Open: 5, Human: 1, ClosedInWindow: 2},
		{Name: "beta", Open: 3, Human: 0, ClosedInWindow: 1},
		{Name: "gamma", Err: "permission denied"},
	}
	var buf bytes.Buffer
	emitDashboardTable(&buf, rows, 7)
	out := buf.String()

	// Per-repo lines.
	for _, want := range []string{"alpha", "beta", "gamma", "permission denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output; got\n%s", want, out)
		}
	}
	// TOTAL row sums the non-errored rows: open 8, human 1, closed 3.
	for _, want := range []string{"TOTAL", "8", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in totals row; got\n%s", want, out)
		}
	}
	// Header includes the window from the days flag.
	if !strings.Contains(out, "CLOSED ↓ 7d") {
		t.Errorf("header should include window days; got\n%s", out)
	}
}

func TestTallyIssues_ClassifiesAllBranches(t *testing.T) {
	cutoff := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	// Boundary: inside window (closed at cutoff+1s) vs. outside
	// (closed at cutoff-1s). Inside should count, outside should
	// not — pins the After-not-AtOrAfter contract.
	inside := cutoff.Add(time.Second)
	outside := cutoff.Add(-time.Second)
	issues := []beads.Issue{
		// open non-human → open++, human stays put.
		{Status: "open"},
		// open human → both open and human++.
		{Status: "open", Labels: []string{"human"}},
		// closed inside window → closedInWindow++.
		{Status: "closed", ClosedAt: inside},
		// closed outside window → no counter.
		{Status: "closed", ClosedAt: outside},
		// closed inside window but human-labeled — closed paths
		// never increment human regardless of label.
		{Status: "closed", ClosedAt: inside, Labels: []string{"human"}},
		// closed with zero ClosedAt — shouldn't happen, but if it
		// does we drop silently rather than counting.
		{Status: "closed"},
	}
	open, human, closedInWindow := tallyIssues(issues, cutoff)
	if open != 2 {
		t.Errorf("open = %d, want 2", open)
	}
	if human != 1 {
		t.Errorf("human = %d, want 1 (closed rows must not contribute)", human)
	}
	if closedInWindow != 2 {
		t.Errorf("closedInWindow = %d, want 2 (inside-window closed counts; zero-ClosedAt drops)", closedInWindow)
	}
}

func TestTallyIssues_WithPriorityFilter(t *testing.T) {
	// Confirm the wiring: filterByMaxPriority is what
	// collectDashboard applies before calling tallyIssues, so the
	// open/human/closed-in-window counts reflect the in-priority
	// set. Test the composition end-to-end on a fixture so the
	// behavior is locked even if collectDashboard rearranges
	// internally.
	now := time.Now()
	cutoff := now.Add(-7 * 24 * time.Hour)
	all := []beads.Issue{
		{ID: "p0-open", Priority: 0, Status: "open"},
		{ID: "p1-open-human", Priority: 1, Status: "open", Labels: []string{"human"}},
		{ID: "p2-open", Priority: 2, Status: "open"},
		{ID: "p3-open", Priority: 3, Status: "open"},
		{ID: "p1-closed-recent", Priority: 1, Status: "closed", ClosedAt: now.Add(-1 * time.Hour)},
		{ID: "p3-closed-recent", Priority: 3, Status: "closed", ClosedAt: now.Add(-1 * time.Hour)},
	}

	filtered := filterByMaxPriority(append([]beads.Issue(nil), all...), 1)
	open, human, closed := tallyIssues(filtered, cutoff)
	// P0 + P1 only: 2 open (p0-open + p1-open-human), 1 human,
	// 1 closed-in-window (p1-closed-recent).
	if open != 2 || human != 1 || closed != 1 {
		t.Errorf("max=1 counts open=%d human=%d closed=%d; want 2/1/1", open, human, closed)
	}
}

func TestEmitDashboardJSON_RoundTrips(t *testing.T) {
	rows := []dashboardRow{{Name: "alpha", Open: 5, Human: 1, ClosedInWindow: 2}}
	cutoff := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	emitDashboardJSON(&buf, rows, 7, cutoff)

	var got struct {
		WindowDays   int            `json:"window_days"`
		WindowCutoff time.Time      `json:"window_cutoff"`
		Repos        []dashboardRow `json:"repos"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if got.WindowDays != 7 {
		t.Errorf("WindowDays = %d, want 7", got.WindowDays)
	}
	if !got.WindowCutoff.Equal(cutoff) {
		t.Errorf("WindowCutoff = %v, want %v", got.WindowCutoff, cutoff)
	}
	if len(got.Repos) != 1 || got.Repos[0].Name != "alpha" {
		t.Errorf("Repos = %v, want [alpha]", got.Repos)
	}
}
