package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/raylytics/would-you-kindly/internal/beads"
)

// cacheSchemaVersion is the on-disk format the loader requires.
// Bump when the JSON shape changes incompatibly so an older wyk
// reading a newer cache returns a clean "unsupported version"
// signal instead of best-effort-parsing into garbage.
const cacheSchemaVersion = 1

// cacheTTL is the oldest a saved fetch may be before LoadCache
// treats it as unusable. Stale-but-recent data beats a blank
// "loading…" screen; stale-by-weeks data is more confusing than
// helpful (the user assumes it's current).
const cacheTTL = 7 * 24 * time.Hour

// cacheMaxIssues bounds the persisted snapshot size. A pathological
// registry (thousands of open issues) shouldn't blow up the cache
// file unboundedly. The truncation keeps the first N issues in the
// order the fetch delivered them — no sort happens here; the live
// fetch that lands shortly after warm-start fills in the rest.
const cacheMaxIssues = 5000

// Cache is the on-disk snapshot of the last successful Fetch. It
// lets a fresh `wyk` paint rows on the first frame (sourced from
// disk) while the live fetch runs in parallel — eliminating the
// "loading…" beat for the user's most-frequent interaction.
type Cache struct {
	Version int           `json:"version"`
	Preset  string        `json:"preset"`
	SavedAt time.Time     `json:"saved_at"`
	Issues  []beads.Issue `json:"issues"`
}

// CacheDefaultPath returns the canonical cache-file location:
// $XDG_CACHE_HOME/wyk/last-fetch.json, falling back to
// ~/.cache/wyk/last-fetch.json. XDG_CACHE_HOME (not _CONFIG_) is
// the convention for ephemeral data the user can delete to force
// a cold start.
func CacheDefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "last-fetch.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "wyk", "last-fetch.json"), nil
}

// LoadCache reads the cache file. Returns a usable Cache (zero-
// valued Issues is fine — caller checks len) and a nil error on a
// missing file: a first-run user shouldn't see an error banner
// just because they've never opened wyk before. Other classes of
// failure (corrupt JSON, unsupported version, expired TTL) return
// a non-nil error so the caller can log + skip the seed, but
// never block startup.
func LoadCache(path string) (Cache, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Cache{}, nil
	}
	if err != nil {
		return Cache{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return Cache{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Version != cacheSchemaVersion {
		return Cache{}, fmt.Errorf("%s: cache schema version %d not supported (this wyk understands %d)",
			path, c.Version, cacheSchemaVersion)
	}
	if age := time.Since(c.SavedAt); age > cacheTTL {
		return Cache{}, fmt.Errorf("%s: cache is %s old (TTL %s); ignoring", path, age.Round(time.Hour), cacheTTL)
	}
	return c, nil
}

// saveCacheCmd returns a tea.Cmd that persists the snapshot off
// the Bubble Tea event loop. Inline persistence from Update would
// block input/render on every successful fetchedMsg for the
// duration of an fsync — exactly the latency the warm-start
// feature exists to eliminate. The command emits no message; the
// save is fire-and-forget. A failure is silently dropped — the
// next successful fetch will retry, and a permanently-broken
// cache directory just degrades to a cold start.
func saveCacheCmd(path, preset string, issues []beads.Issue) tea.Cmd {
	return func() tea.Msg {
		_ = SaveCache(path, Cache{
			Preset:  preset,
			SavedAt: time.Now(),
			Issues:  issues,
		})
		return nil
	}
}

// SaveCache atomically writes the cache via write-tmp-then-rename.
// Bounds the snapshot at cacheMaxIssues to keep the file size in
// check. Errors are returned so the caller can log them, but the
// save is best-effort — a failure here must never abort a TUI
// fetch.
func SaveCache(path string, c Cache) error {
	c.Version = cacheSchemaVersion
	if c.SavedAt.IsZero() {
		c.SavedAt = time.Now()
	}
	if len(c.Issues) > cacheMaxIssues {
		c.Issues = c.Issues[:cacheMaxIssues]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-fetch.json.*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}
