package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
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
