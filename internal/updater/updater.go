// Package updater detects when a newer wyk release is available
// and exposes a cached snapshot the TUI / doctor / update subcommand
// can read without re-hitting GitHub on every call.
//
// The check is best-effort: a missing network, a rate-limit, or a
// malformed response all return an error that callers can treat as
// "no nudge to show" rather than a hard failure. The cache keeps
// the live request rate at ~1/day per user, far below GitHub's
// anonymous-API rate limit.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// owner/repo are constants so the updater can be vendored or
// distributed without callers supplying them. If the project moves
// they get updated here and shipped in the next release.
const (
	githubOwner = "jimbottle"
	githubRepo  = "would-you-kindly"
)

// CacheTTL is how long a snapshot stays fresh before LatestCached
// triggers a refetch. 24h gives a good "noticed the same day"
// signal without hammering the GitHub API.
const CacheTTL = 24 * time.Hour

// Release is the slice of a GitHub release the updater cares
// about. The JSON field names match GitHub's response so a single
// decode populates this directly.
type Release struct {
	TagName     string    `json:"tag_name"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
}

// cacheEntry is the on-disk snapshot. CheckedAt drives the TTL;
// Latest is the most-recent release the last live fetch saw.
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    Release   `json:"latest"`
}

// LatestLive fetches the most recent release from the GitHub API,
// including prereleases. Returns the absolute newest (sorted by
// publish time) — distinct from /releases/latest, which skips
// prereleases and is the exact behaviour that bit users hitting
// the "@latest pulls v0.2.3" trap.
//
// Times out after 5s so a TUI startup doesn't stall on a slow
// network. Errors don't include the underlying response body
// because the caller treats any error as "skip the nudge" — no
// need to spam logs.
func LatestLive(ctx context.Context, client *http.Client) (Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=5", githubOwner, githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain so the connection can be reused; ignore the body
		// content — we won't surface it to the user.
		_, _ = io.Copy(io.Discard, resp.Body)
		return Release{}, fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}
	var rels []Release
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return Release{}, err
	}
	if len(rels) == 0 {
		return Release{}, errors.New("no releases published")
	}
	// GitHub returns newest-first; first entry is the absolute
	// most recent regardless of prerelease flag. Caller decides
	// whether to honour prereleases via the channel option in
	// the update command.
	return rels[0], nil
}

// CachePath returns the on-disk location for the update-check
// snapshot. Honours $XDG_CACHE_HOME and falls back to
// $HOME/.cache/wyk/update.json. The directory is not created here;
// Save handles that.
func CachePath() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "update.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".cache", "wyk", "update.json"), nil
}

// LatestCached returns the most recent release wyk knows about,
// re-fetching live when the cache is missing, malformed, or older
// than CacheTTL. The returned bool is true when the data came from
// a live fetch (vs. cache hit) — useful for telemetry but ignored
// by most callers.
//
// Failures fall through silently: a network error returns the
// stale cache if present, or the empty Release if not. The caller
// distinguishes "no update available" from "couldn't check" by
// inspecting the returned Release.TagName.
func LatestCached(ctx context.Context, client *http.Client) (Release, bool, error) {
	path, perr := CachePath()
	if perr == nil {
		if entry, err := readCache(path); err == nil && time.Since(entry.CheckedAt) < CacheTTL {
			return entry.Latest, false, nil
		}
	}
	rel, err := LatestLive(ctx, client)
	if err != nil {
		// Fall back to stale cache if we have one.
		if path != "" {
			if entry, cerr := readCache(path); cerr == nil {
				return entry.Latest, false, nil
			}
		}
		return Release{}, false, err
	}
	if path != "" {
		_ = writeCache(path, cacheEntry{CheckedAt: time.Now(), Latest: rel})
	}
	return rel, true, nil
}

func readCache(path string) (cacheEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(b, &entry); err != nil {
		return cacheEntry{}, err
	}
	return entry, nil
}

func writeCache(path string, entry cacheEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// IsNewer reports whether latestTag represents a strictly newer
// release than currentVer (the runtime/debug.ReadBuildInfo
// Main.Version). Comparison uses golang.org/x/mod/semver which
// handles prerelease ordering correctly (v0.2.3 < v0.3.0-alpha <
// v0.3.0-beta < v0.3.0).
//
// Special cases:
//   - currentVer "(devel)" or "" → always considered older (so
//     dev builds get a nudge to the latest tag for discoverability)
//   - currentVer is a pseudoversion (v0.2.4-0.YYYYMMDD-SHA) →
//     compared via semver, which puts pseudoversions BEFORE the
//     corresponding tagged version
func IsNewer(currentVer, latestTag string) bool {
	if currentVer == "" || currentVer == "(devel)" {
		return true
	}
	// semver requires a leading v on both sides; tolerate either.
	c := ensureV(currentVer)
	l := ensureV(latestTag)
	if !semver.IsValid(c) || !semver.IsValid(l) {
		return false
	}
	return semver.Compare(c, l) < 0
}

func ensureV(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if s[0] != 'v' {
		return "v" + s
	}
	return s
}

// InstallCommand returns the go-install invocation that pulls the
// given release tag. Useful for both the update subcommand and the
// nudge banner so the two stay in sync.
func InstallCommand(tag string) string {
	return fmt.Sprintf("go install github.com/%s/%s/cmd/wyk@%s", githubOwner, githubRepo, tag)
}
