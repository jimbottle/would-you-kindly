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
// Releases holds the page the last live fetch saw (newest-first),
// so callers can pick the right one for a given channel without
// re-hitting the network. Latest is preserved as a backwards-
// compatible convenience for older readers — same as Releases[0].
// Channel records the user's last-used `wyk update -channel` value
// so the TUI/doctor nudge can advertise a release the user will
// actually install (e.g. don't nudge toward a prerelease for a
// user who pinned -channel stable).
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    Release   `json:"latest"`
	Releases  []Release `json:"releases"`
	Channel   string    `json:"channel,omitempty"`
}

// LatestLive fetches the most recent releases from the GitHub API,
// including prereleases. Returns the page newest-first (per the
// API's ordering) so callers can pick by channel: Releases[0] is
// the absolute newest including prereleases; PickStable picks the
// newest with Prerelease == false. Five entries is enough headroom
// for any realistic prerelease-burst (e.g. -alpha → -beta → -rc1 →
// -rc2 → final) without paginating.
//
// Times out after 5s so a TUI startup doesn't stall on a slow
// network. Errors don't include the underlying response body
// because the caller treats any error as "skip the nudge" — no
// need to spam logs.
func LatestLive(ctx context.Context, client *http.Client) ([]Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=5", githubOwner, githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain so the connection can be reused; ignore the body
		// content — we won't surface it to the user.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}
	var rels []Release
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, errors.New("no releases published")
	}
	return rels, nil
}

// PickStable returns the newest release in the slice whose
// Prerelease flag is false, or the zero value if the slice
// contains only prereleases. Callers in the "stable" channel use
// this to fall through to the most recent final release rather
// than aborting on a prerelease-at-HEAD scenario.
func PickStable(rels []Release) Release {
	for _, r := range rels {
		if !r.Prerelease {
			return r
		}
	}
	return Release{}
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

// CachedOnly returns whatever the cache file currently holds,
// without re-fetching live on miss/stale. Use this on the error
// path of a live-fetch caller so a genuine network outage doesn't
// trigger a second HTTP attempt inside the same handler. Returns
// an empty slice when no cache exists or it can't be decoded.
func CachedOnly() ([]Release, error) {
	path, err := CachePath()
	if err != nil {
		return nil, err
	}
	entry, err := readCache(path)
	if err != nil {
		// Distinguish "no cache yet" (not an error for callers
		// using us as a fallback) from a corrupted file.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entryReleases(entry), nil
}

// PersistLatest writes a fresh release-page snapshot to the cache
// so subsequent reads (TUI nudge, doctor stanza, next LatestCached
// within the TTL) see the up-to-date list. Used by callers that
// did their own live fetch and want to share the result. Empty
// slices are silently ignored; cache-path errors are returned but
// callers typically swallow them — this is best-effort
// enrichment, not load-bearing for the caller's flow.
//
// channel records the user's preference so the TUI nudge can
// advertise the right release on the next paint. Empty string
// preserves whatever value the previous cache entry held (so the
// background update-check path doesn't clobber a preference set
// by an earlier explicit `wyk update -channel ...` call).
func PersistLatest(rels []Release, channel string) error {
	if len(rels) == 0 {
		return nil
	}
	path, err := CachePath()
	if err != nil {
		return err
	}
	entry := cacheEntry{
		CheckedAt: time.Now(),
		Latest:    rels[0],
		Releases:  rels,
		Channel:   channel,
	}
	if channel == "" {
		// Preserve the previously-saved preference instead of
		// blanking it on a background check.
		if prev, perr := readCache(path); perr == nil {
			entry.Channel = prev.Channel
		}
	}
	return writeCache(path, entry)
}

// CachedChannel returns the user's last-saved channel preference,
// or "any" when no cache exists / no preference recorded.
func CachedChannel() string {
	path, err := CachePath()
	if err != nil {
		return "any"
	}
	entry, err := readCache(path)
	if err != nil {
		return "any"
	}
	if entry.Channel == "" {
		return "any"
	}
	return entry.Channel
}

// LatestCached returns the page of recent releases wyk knows
// about (newest-first), re-fetching live when the cache is
// missing, malformed, or older than CacheTTL. The returned bool
// is true when the data came from a live fetch (vs. cache hit) —
// useful for telemetry but ignored by most callers.
//
// Failures fall through silently: a network error returns the
// stale cache page if present, or nil if not. The caller
// distinguishes "no update available" from "couldn't check" by
// inspecting whether the returned slice is non-empty.
func LatestCached(ctx context.Context, client *http.Client) ([]Release, bool, error) {
	path, perr := CachePath()
	if perr == nil {
		if entry, err := readCache(path); err == nil && time.Since(entry.CheckedAt) < CacheTTL {
			return entryReleases(entry), false, nil
		}
	}
	rels, err := LatestLive(ctx, client)
	if err != nil {
		// Fall back to stale cache if we have one.
		if path != "" {
			if entry, cerr := readCache(path); cerr == nil {
				return entryReleases(entry), false, nil
			}
		}
		return nil, false, err
	}
	if path != "" {
		entry := cacheEntry{CheckedAt: time.Now(), Releases: rels}
		if len(rels) > 0 {
			entry.Latest = rels[0]
		}
		_ = writeCache(path, entry)
	}
	return rels, true, nil
}

// entryReleases prefers Releases (the new schema) but falls back
// to wrapping Latest in a single-element slice so caches written
// by older wyk versions still resolve. Newer wyk writes both, so
// fresh caches always populate Releases.
func entryReleases(e cacheEntry) []Release {
	if len(e.Releases) > 0 {
		return e.Releases
	}
	if e.Latest.TagName != "" {
		return []Release{e.Latest}
	}
	return nil
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

// writeCache writes the snapshot atomically (temp file + rename)
// so a concurrent reader in another wyk process — either the TUI
// reading the nudge or `wyk doctor` consulting it — can never
// observe a half-written file. Matches the pattern used by
// internal/registry.Save. Returns the first error encountered;
// callers ignore failures (cache is best-effort).
func writeCache(path string, entry cacheEntry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".update.*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
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
