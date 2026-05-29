package main

import (
	"os"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

func TestStatsSubs_RepoNameSelectsOneRegisteredEntry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "alpha", Path: "/tmp/a"},
		{Name: "beta", Path: "/tmp/b"},
	}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	subs, code := statsSubs("", "beta")
	if code != 0 {
		t.Errorf("statsSubs exit %d, want 0", code)
	}
	if len(subs) != 1 || subs[0].name != "beta" {
		t.Errorf("subs=%v, want [beta]", subs)
	}
}

func TestStatsSubs_MissingRepoNameExits1(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "alpha", Path: "/tmp/a"}}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Swallow the stderr error message so the test log stays
	// clean — filterRegistryByName writes to stderr in the
	// no-match branch.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()

	_, code := statsSubs("", "ghost")
	if code != 1 {
		t.Errorf("missing-name exit %d, want 1", code)
	}
}

func TestRunStats_CAndRepoMutuallyExclusive(t *testing.T) {
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()
	if code := runStats([]string{"-C", "/tmp/a", "-repo", "alpha"}); code != 64 {
		t.Errorf("-C+-repo exit %d, want 64", code)
	}
}

func TestComputeStats_BasicAggregations(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	all := []beads.Issue{
		// Currently open + human-flagged from an agent.
		{ID: "a-1", Status: "open", Labels: []string{"human", "src:agent"},
			CreatedAt: now.Add(-2 * day)},
		// Open + human, self-filed.
		{ID: "a-2", Status: "open", Labels: []string{"human", "src:human"},
			CreatedAt: now.Add(-1 * day)},
		// Inbox: src:agent without human, not closed.
		{ID: "a-3", Status: "open", Labels: []string{"src:agent"},
			CreatedAt: now.Add(-3 * day)},
		// Closed within 7d: a human-flagged from agent (counts toward time-to-close).
		{ID: "c-1", Status: "closed", Labels: []string{"human", "src:agent"},
			CreatedAt: now.Add(-3 * day),
			ClosedAt:  now.Add(-1 * day)},
		// Closed within 7d: non-human.
		{ID: "c-2", Status: "closed", Labels: nil,
			CreatedAt: now.Add(-5 * day),
			ClosedAt:  now.Add(-4 * day)},
		// Closed > 30d ago.
		{ID: "c-3", Status: "closed", Labels: nil,
			CreatedAt: now.Add(-100 * day),
			ClosedAt:  now.Add(-50 * day)},
		// In-progress: counts in by_status only.
		{ID: "w-1", Status: "in_progress", Labels: nil,
			CreatedAt: now.Add(-1 * day)},
	}

	s := computeStats(all, now)

	if s.TotalIssues != 7 {
		t.Errorf("TotalIssues = %d, want 7", s.TotalIssues)
	}
	if s.ByStatus["open"] != 3 || s.ByStatus["closed"] != 3 || s.ByStatus["in_progress"] != 1 {
		t.Errorf("ByStatus mismatch: %+v", s.ByStatus)
	}
	if s.HumanFlagged != 2 {
		t.Errorf("HumanFlagged = %d, want 2 (open issues with human label)", s.HumanFlagged)
	}
	if s.HumanFromAgent != 1 || s.HumanFromHuman != 1 {
		t.Errorf("HumanFromAgent=%d HumanFromHuman=%d; want 1/1", s.HumanFromAgent, s.HumanFromHuman)
	}
	if s.InboxCount != 1 {
		t.Errorf("InboxCount = %d, want 1 (a-3: src:agent without human, not closed)", s.InboxCount)
	}
	if s.ClosedLast7d != 2 {
		t.Errorf("ClosedLast7d = %d, want 2", s.ClosedLast7d)
	}
	if s.ClosedLast30d != 2 {
		t.Errorf("ClosedLast30d = %d, want 2", s.ClosedLast30d)
	}
	if s.TimeToCloseHuman == nil {
		t.Fatal("TimeToCloseHuman should not be nil — c-1 was a closed human-flagged issue")
	}
	if s.TimeToCloseHuman.Samples != 1 {
		t.Errorf("TimeToCloseHuman.Samples = %d, want 1", s.TimeToCloseHuman.Samples)
	}
	// c-1 was open 2 days, so median ≈ 2 * day.
	if want := 2 * day; s.TimeToCloseHuman.Median != want {
		t.Errorf("TimeToCloseHuman.Median = %v, want %v", s.TimeToCloseHuman.Median, want)
	}
}

func TestComputeStats_EmptyInputYieldsZeros(t *testing.T) {
	s := computeStats(nil, time.Now())
	if s.TotalIssues != 0 {
		t.Errorf("TotalIssues = %d, want 0", s.TotalIssues)
	}
	if s.TimeToCloseHuman != nil {
		t.Errorf("TimeToCloseHuman should be nil for empty input; got %+v", s.TimeToCloseHuman)
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
		{49 * time.Hour, "2d 1h"},
	}
	for _, c := range cases {
		if got := humanizeDuration(c.d); got != c.want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
