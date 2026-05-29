package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// stubActivityClient is a minimal activityClient impl that lets
// the collectActivity test drive happy / errored / empty branches
// per repo without a real bd binary.
type stubActivityClient struct {
	issues []beads.Issue
	err    error
}

func (s *stubActivityClient) ListAll(_ context.Context) ([]beads.Issue, error) {
	return s.issues, s.err
}

func TestCollectActivity_FiltersAndSorts(t *testing.T) {
	cutoff := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	in := cutoff.Add(time.Hour)        // inside window
	older := cutoff.Add(-time.Hour)    // outside window
	newer := cutoff.Add(2 * time.Hour) // newest inside window
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "alpha", Path: "/tmp/a"},
		{Name: "beta", Path: "/tmp/b"},
		{Name: "broken", Path: "/tmp/c"},
	}}
	stubs := map[string]*stubActivityClient{
		"/tmp/a": {issues: []beads.Issue{
			{ID: "a-1", Title: "rotate", Status: "open", UpdatedAt: in},
			{ID: "a-2", Title: "old", Status: "closed", UpdatedAt: older},
		}},
		"/tmp/b": {issues: []beads.Issue{
			{ID: "b-1", Title: "deploy", Status: "open", UpdatedAt: newer},
		}},
		"/tmp/c": {err: errors.New("boom")},
	}
	mk := func(dir string) activityClient { return stubs[dir] }

	events, hadError := collectActivity(reg, cutoff, mk)
	if !hadError {
		t.Errorf("hadError should be true when any sub failed")
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 in-window events; got %d", len(events))
	}
	// Sorted newest-first: b-1 (newer) before a-1 (in).
	if events[0].ID != "b-1" || events[1].ID != "a-1" {
		t.Errorf("events should be sorted newest-first; got %v then %v", events[0].ID, events[1].ID)
	}
}

func TestEmitActivityTable_EmptyWindow(t *testing.T) {
	var buf bytes.Buffer
	emitActivityTable(&buf, nil, time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(buf.String(), "(nothing touched in the window)") {
		t.Errorf("empty window should print a placeholder; got %q", buf.String())
	}
}

func TestEmitActivityJSON_EmptyEventsRendersArrayNotNull(t *testing.T) {
	// Downstream tools iterating `events` shouldn't have to
	// special-case `null` — pin the empty-window shape as [].
	var buf bytes.Buffer
	cutoff := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	emitActivityJSON(&buf, []activityEvent{}, cutoff)

	if !strings.Contains(buf.String(), `"events": []`) {
		t.Errorf("empty events should encode as []; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"cutoff"`) {
		t.Errorf("output should include the cutoff field; got %q", buf.String())
	}
}

func TestCollectActivity_SkipsZeroUpdatedAt(t *testing.T) {
	cutoff := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "alpha", Path: "/tmp/a"}}}
	stubs := map[string]*stubActivityClient{
		"/tmp/a": {issues: []beads.Issue{
			// zero UpdatedAt — must be skipped even though
			// time.Time{}.After(cutoff) is false anyway, the
			// !IsZero() guard pins the contract.
			{ID: "a-1", Title: "no timestamp"},
			{ID: "a-2", Title: "real", Status: "open",
				UpdatedAt: cutoff.Add(time.Hour)},
		}},
	}
	mk := func(dir string) activityClient { return stubs[dir] }

	events, _ := collectActivity(reg, cutoff, mk)
	if len(events) != 1 || events[0].ID != "a-2" {
		t.Errorf("zero-UpdatedAt row should be skipped; got %+v", events)
	}
}

func TestRunActivity_RejectsBadArgs(t *testing.T) {
	// Send stderr to /dev/null so the validation messages don't
	// leak into the test runner's output.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"non-zero since=0", []string{"-since", "0"}, 64},
		{"positional argument", []string{"extra"}, 64},
		{"unknown flag", []string{"-frobnicate"}, 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runActivity(tc.args); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEmitActivityTable_RendersRowsInOrder(t *testing.T) {
	events := []activityEvent{
		{Repo: "alpha", ID: "a-1", Title: "rotate", Status: "open",
			UpdatedAt: time.Date(2026, 5, 29, 14, 0, 0, 0, time.UTC)},
		{Repo: "beta", ID: "b-9", Title: "deploy", Status: "closed",
			UpdatedAt: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)},
	}
	var buf bytes.Buffer
	emitActivityTable(&buf, events, time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC))
	out := buf.String()
	// First row newer → appears before second.
	aIdx := strings.Index(out, "a-1")
	bIdx := strings.Index(out, "b-9")
	if aIdx < 0 || bIdx < 0 || aIdx > bIdx {
		t.Errorf("rows should render in given (sorted) order; got %q", out)
	}
	// Header includes the cutoff.
	if !strings.Contains(out, "since 2026-05-29 00:00") {
		t.Errorf("header should include the cutoff; got %q", out)
	}
}
