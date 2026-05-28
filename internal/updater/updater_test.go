package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		{"v0.3.0", "v0.2.3", false},        // newer current beats older latest
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
	rel, err := LatestLive(t.Context(), hijack)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.4.0-alpha" {
		t.Errorf("first release should be returned; got %q", rel.TagName)
	}
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
	rel, fresh, err := LatestCached(t.Context(), noNet)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Error("fresh==true means a live fetch happened; cache was supposed to win")
	}
	if rel.TagName != "v0.9.9-cached" {
		t.Errorf("got %q, want the cached tag", rel.TagName)
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
	}
	b, _ := json.Marshal(stale)
	_ = os.WriteFile(path, b, 0o644)
	fail := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &netErr{}
	})}
	rel, _, err := LatestCached(t.Context(), fail)
	if err != nil {
		t.Errorf("expected silent fall-back to stale cache, got error: %v", err)
	}
	if rel.TagName != "v0.1.1-stale" {
		t.Errorf("got %q, want the stale-cache fallback", rel.TagName)
	}
}

// --- helpers ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type netErr struct{}

func (netErr) Error() string   { return "simulated network outage" }
func (netErr) Timeout() bool   { return false }
func (netErr) Temporary() bool { return true }
