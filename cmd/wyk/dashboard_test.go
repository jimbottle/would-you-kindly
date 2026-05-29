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
