package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsNewer_SemverOrdering(t *testing.T) {
	// Pin a handful of the orderings we rely on for the nudge:
	// devel < any tag; pseudoversion < its corresponding tag;
	// prerelease < final; stale tag < newest tag. Each case is a
	// concrete pair from this project's actual release history.
	cases := []struct {
		current string
		latest  string
		want    bool
	}{
		{"(devel)", "v0.3.0-alpha", true},
		{"", "v0.3.0-alpha", true},
		{"v0.2.3", "v0.3.0-alpha", true},
		{"v0.2.4-0.20260528195020-92ea3db7f8f3", "v0.3.0-alpha", true},
		{"v0.3.0-alpha", "v0.3.0-beta", true},
		{"v0.3.0-alpha", "v0.3.0", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.3.0", "v0.3.0-alpha", false}, // final beats prerelease
		{"v0.3.0", "v0.2.3", false},       // newer current beats older latest
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestIsNewer_GarbageInputsReturnFalse(t *testing.T) {
	// Pre-tag pseudoversions where the tag is unparsable, or
	// hand-typed nonsense, must NOT cause the updater to claim
	// "newer" — the nudge would mislead the user. Return false
	// instead.
	cases := []struct {
		current, latest string
	}{
		{"notaversion", "v0.3.0"},
		{"v0.3.0", "alsonotone"},
		{"foo bar", "baz qux"},
	}
	for _, c := range cases {
		if IsNewer(c.current, c.latest) {
			t.Errorf("IsNewer(%q, %q) = true; want false for unparseable input", c.current, c.latest)
		}
	}
}

func TestInstallCommand_FormsTheExpectedGoInstall(t *testing.T) {
	got := InstallCommand("v0.3.0-alpha")
	want := "go install github.com/jimbottle/would-you-kindly/cmd/wyk@v0.3.0-alpha"
	if got != want {
		t.Errorf("InstallCommand = %q, want %q", got, want)
	}
}

func TestLatestLive_ReturnsFirstEntry(t *testing.T) {
	// Spin up a stub server emitting a /releases array in
	// GitHub's shape with two entries; LatestLive should return
	// the first (newest, per the API's ordering).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Release{
			{TagName: "v0.4.0-alpha", Prerelease: true, PublishedAt: time.Now()},
			{TagName: "v0.3.0", Prerelease: false, PublishedAt: time.Now().Add(-24 * time.Hour)},
		})
	}))
	defer srv.Close()
	// Override the URL by swapping the host via a custom client
	// that rewrites api.github.com → the test server.
	hijack := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = "http"
			req.URL.Host = srv.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	rels, err := LatestLive(t.Context(), hijack)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("expected both releases in the page; got %d", len(rels))
	}
	if rels[0].TagName != "v0.4.0-alpha" {
		t.Errorf("newest entry should come first; got %q", rels[0].TagName)
	}
	if rels[1].TagName != "v0.3.0" {
		t.Errorf("second entry should be the stable; got %q", rels[1].TagName)
	}
}

func TestPickStable_ReturnsNewestNonPrerelease(t *testing.T) {
	// The stable-channel branch of `wyk update` lives or dies by
	// PickStable. Three cases worth pinning: newest-is-prerelease
	// falls through to the next non-prerelease; newest-is-stable
	// returns it directly; all-prereleases returns the zero
	// Release so callers can surface "no stable version known".
	cases := []struct {
		name string
		in   []Release
		want string
	}{
		{
			name: "newest-is-prerelease-falls-through",
			in: []Release{
				{TagName: "v0.4.0-alpha", Prerelease: true},
				{TagName: "v0.3.0", Prerelease: false},
				{TagName: "v0.2.3", Prerelease: false},
			},
			want: "v0.3.0",
		},
		{
			name: "newest-is-stable-returns-it",
			in: []Release{
				{TagName: "v0.3.0", Prerelease: false},
				{TagName: "v0.2.3", Prerelease: false},
			},
			want: "v0.3.0",
		},
		{
			name: "all-prereleases-returns-empty",
			in: []Release{
				{TagName: "v0.4.0-alpha", Prerelease: true},
				{TagName: "v0.4.0-beta1", Prerelease: true},
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PickStable(c.in).TagName; got != c.want {
				t.Errorf("PickStable(%v).TagName = %q, want %q", tagNames(c.in), got, c.want)
			}
		})
	}
}

func tagNames(rels []Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.TagName
	}
	return out
}

func TestLatestCached_UsesCacheWithinTTL(t *testing.T) {
	// Plant a fresh cache entry; LatestCached must return it
	// without hitting the live endpoint at all.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, err := CachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := cacheEntry{
		CheckedAt: time.Now().Add(-1 * time.Hour), // 1h old, well within TTL
		Latest:    Release{TagName: "v0.9.9-cached"},
		Releases:  []Release{{TagName: "v0.9.9-cached"}},
	}
	b, _ := json.Marshal(entry)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// Refuse to hit a real network: if the client gets used at
	// all, the test fails fast.
	noNet := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("LatestCached must NOT hit the network when cache is fresh")
		return nil, nil
	})}
	rels, fresh, err := LatestCached(t.Context(), noNet)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Error("fresh==true means a live fetch happened; cache was supposed to win")
	}
	if len(rels) != 1 || rels[0].TagName != "v0.9.9-cached" {
		t.Errorf("got %v, want a single-element slice with the cached tag", tagNames(rels))
	}
}

func TestLatestCached_LegacySingleLatestCacheBackCompat(t *testing.T) {
	// Caches written by v0.3.0 only carry the `latest` field
	// (single Release). New code must still surface that as a
	// one-element slice so the upgrade path is smooth — without
	// this, every user's first run after upgrading to v0.3.1
	// would silently re-fetch instead of using the existing
	// cache.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	legacy := struct {
		CheckedAt time.Time `json:"checked_at"`
		Latest    Release   `json:"latest"`
	}{
		CheckedAt: time.Now().Add(-2 * time.Hour),
		Latest:    Release{TagName: "v0.3.0", Prerelease: false},
	}
	b, _ := json.Marshal(legacy)
	_ = os.WriteFile(path, b, 0o644)
	rels, _, err := LatestCached(t.Context(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("legacy-format cache should still satisfy the read; no live fetch expected")
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].TagName != "v0.3.0" {
		t.Errorf("legacy cache should produce a one-element slice with the latest tag; got %v", tagNames(rels))
	}
}

func TestLatestCached_FallsBackToStaleOnNetworkError(t *testing.T) {
	// Cache is older than TTL; LatestCached should attempt the
	// live fetch, fail, and fall back to the stale cache rather
	// than erroring out (silent-degradation policy).
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	stale := cacheEntry{
		CheckedAt: time.Now().Add(-72 * time.Hour), // way past TTL
		Latest:    Release{TagName: "v0.1.1-stale"},
		Releases:  []Release{{TagName: "v0.1.1-stale"}},
	}
	b, _ := json.Marshal(stale)
	_ = os.WriteFile(path, b, 0o644)
	fail := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &netErr{}
	})}
	rels, _, err := LatestCached(t.Context(), fail)
	if err != nil {
		t.Errorf("expected silent fall-back to stale cache, got error: %v", err)
	}
	if len(rels) != 1 || rels[0].TagName != "v0.1.1-stale" {
		t.Errorf("got %v, want the stale-cache fallback", tagNames(rels))
	}
}

func TestWriteCache_IsAtomic_NoPartialFileLeaksToReader(t *testing.T) {
	// Write a cache snapshot; then read it. The write must use
	// temp+rename so a reader concurrent with the write can never
	// observe a half-written file — we can't easily race the
	// goroutines deterministically, but we CAN assert no
	// `.update.*.json.tmp` file is left behind and the final
	// file decodes as the full entry.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := CachePath()
	entry := cacheEntry{
		CheckedAt: time.Now(),
		Latest:    Release{TagName: "v9.9.9"},
		Releases:  []Release{{TagName: "v9.9.9"}, {TagName: "v9.8.0"}},
	}
	if err := writeCache(path, entry); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	// Re-read and confirm it's the full entry.
	got, err := readCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Releases) != 2 || got.Latest.TagName != "v9.9.9" {
		t.Errorf("round-tripped entry corrupted; got %+v", got)
	}
	// No leftover temp files in the cache dir — rename-into-place
	// is the contract.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".update.") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// --- helpers ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type netErr struct{}

func (netErr) Error() string   { return "simulated network outage" }
func (netErr) Timeout() bool   { return false }
func (netErr) Temporary() bool { return true }

func TestCachedOnly_DoesNotRefetchOnStale(t *testing.T) {
	// CachedOnly is the pure-read variant — even when the on-disk
	// snapshot is older than CacheTTL, it returns what's there
	// rather than re-fetching live. Callers on the error path
	// rely on this so a network outage doesn't trigger a second
	// HTTP attempt inside the same handler.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	stale := cacheEntry{
		CheckedAt: time.Now().Add(-72 * time.Hour), // well past TTL
		Releases:  []Release{{TagName: "v0.0.1-stale"}},
	}
	b, _ := json.Marshal(stale)
	_ = os.WriteFile(path, b, 0o644)

	rels, err := CachedOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].TagName != "v0.0.1-stale" {
		t.Errorf("CachedOnly should return the on-disk snapshot verbatim; got %v", tagNames(rels))
	}
}

func TestCachedOnly_MissingCacheReturnsEmptyNotError(t *testing.T) {
	// First-run case: no cache file yet. CachedOnly should
	// return (nil, nil) so the caller can treat "no info" as
	// the soft-fail signal rather than an error.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	rels, err := CachedOnly()
	if err != nil {
		t.Errorf("missing cache should not be an error; got %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("missing cache should produce empty slice; got %v", tagNames(rels))
	}
}
