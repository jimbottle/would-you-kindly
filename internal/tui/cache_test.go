package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raylytics/would-you-kindly/internal/beads"
)

func TestCacheDefaultPath_HonorsXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	got, err := CacheDefaultPath()
	if err != nil {
		t.Fatalf("CacheDefaultPath: %v", err)
	}
	want := "/tmp/xdg-cache/wyk/last-fetch.json"
	if got != want {
		t.Errorf("CacheDefaultPath = %q, want %q", got, want)
	}
}

func TestCacheDefaultPath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/fake-home")
	got, err := CacheDefaultPath()
	if err != nil {
		t.Fatalf("CacheDefaultPath: %v", err)
	}
	want := "/tmp/fake-home/.cache/wyk/last-fetch.json"
	if got != want {
		t.Errorf("CacheDefaultPath = %q, want %q", got, want)
	}
}

func TestSaveLoadCache_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-fetch.json")
	issues := []beads.Issue{
		{ID: "wyk-1", Title: "first"},
		{ID: "wyk-2", Title: "second"},
	}
	now := time.Now().Truncate(time.Second)
	if err := SaveCache(path, Cache{Preset: "all", SavedAt: now, Issues: issues}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got.Preset != "all" {
		t.Errorf("Preset = %q, want %q", got.Preset, "all")
	}
	if len(got.Issues) != 2 || got.Issues[0].ID != "wyk-1" {
		t.Errorf("Issues = %+v, want 2 entries starting with wyk-1", got.Issues)
	}
	if !got.SavedAt.Equal(now) {
		t.Errorf("SavedAt = %v, want %v", got.SavedAt, now)
	}
}

func TestLoadCache_MissingFileReturnsZeroNoError(t *testing.T) {
	// First-run users (no cache file yet) must not see an error.
	// The TUI's startup path treats nil err as "skip the seed" via
	// the empty-issues check.
	got, err := LoadCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Errorf("missing-file LoadCache error: %v", err)
	}
	if len(got.Issues) != 0 {
		t.Errorf("missing-file LoadCache issues = %d, want 0", len(got.Issues))
	}
}

func TestLoadCache_ExpiredTTLReturnsError(t *testing.T) {
	// A cache older than cacheTTL is worse than no cache (the user
	// assumes it's fresh). The TUI's caller treats a non-nil err
	// as "skip the seed" so this path keeps the load best-effort.
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.json")
	oldTime := time.Now().Add(-cacheTTL - time.Hour)
	if err := SaveCache(path, Cache{Preset: "all", SavedAt: oldTime, Issues: []beads.Issue{{ID: "x"}}}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, err := LoadCache(path); err == nil {
		t.Error("expected error for expired cache; got nil")
	} else if !strings.Contains(err.Error(), "TTL") {
		t.Errorf("expected TTL error; got %v", err)
	}
}

func TestLoadCache_UnsupportedVersionReturnsError(t *testing.T) {
	// A future wyk's cache must not be interpreted as the current
	// schema. Errors out cleanly so the caller falls back to a
	// cold start without corrupting the on-disk data.
	dir := t.TempDir()
	path := filepath.Join(dir, "future.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"preset":"all","saved_at":"2026-05-31T00:00:00Z","issues":[]}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadCache(path); err == nil {
		t.Error("expected error for unsupported version; got nil")
	}
}

func TestSaveCache_CapsAtCacheMaxIssues(t *testing.T) {
	// A pathological registry (thousands of issues) shouldn't grow
	// the cache file unboundedly. The save bound keeps disk usage
	// predictable; the user's next live fetch fills in the rest.
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.json")
	big := make([]beads.Issue, cacheMaxIssues+10)
	for i := range big {
		big[i] = beads.Issue{ID: "x", Title: "y"}
	}
	if err := SaveCache(path, Cache{Preset: "all", SavedAt: time.Now(), Issues: big}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if len(got.Issues) != cacheMaxIssues {
		t.Errorf("Issues = %d, want %d (cap)", len(got.Issues), cacheMaxIssues)
	}
}
