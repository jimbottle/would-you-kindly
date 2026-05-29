package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/updater"
)

func TestExtractCurrentTag_CoversEveryVersionStringShape(t *testing.T) {
	// versionString produces several forms depending on how the
	// binary was built (tagged install, pseudoversion, source-tree
	// build, build-info missing). extractCurrentTag must pull a
	// usable token out of each — getting it wrong silently breaks
	// the nudge ("already on " comparison) for that user.
	cases := []struct {
		in   string
		want string
	}{
		{"wyk v0.3.0", "v0.3.0"},
		{"wyk v0.3.0-alpha (commit abc123)", "v0.3.0-alpha"},
		{"wyk v0.3.1 (commit abc123-dirty)", "v0.3.1"},
		{"wyk (devel) (commit abc123)", "(devel)"},
		{"wyk (unknown — build info missing)", "(unknown"},
		{"wyk v0.2.4-0.20260528195020-92ea3db7f8f3 (commit 92ea3db)", "v0.2.4-0.20260528195020-92ea3db7f8f3"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := extractCurrentTag(c.in); got != c.want {
				t.Errorf("extractCurrentTag(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestReadUpdateNudge_ProducesBannerWhenNewer(t *testing.T) {
	// Plant a cache entry advertising a newer version than the
	// current. readUpdateNudge should produce a non-empty banner
	// string mentioning the new tag.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := updater.CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	entry := map[string]any{
		"checked_at": time.Now().Format(time.RFC3339),
		"latest":     map[string]string{"tag_name": "v0.9.9"},
		"releases":   []map[string]string{{"tag_name": "v0.9.9"}},
	}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o644)

	got := readUpdateNudge("wyk v0.3.0")
	if got == "" {
		t.Fatal("expected a non-empty nudge banner when cache advertises newer; got empty")
	}
	if !strings.Contains(got, "v0.9.9") {
		t.Errorf("nudge should name the newer tag; got %q", got)
	}
	if !strings.Contains(got, "wyk update") {
		t.Errorf("nudge should tell the user the resolve command; got %q", got)
	}
}

func TestReadUpdateNudge_EmptyWhenUpToDate(t *testing.T) {
	// Cache advertises the same version as the binary → no nudge.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := updater.CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	entry := map[string]any{
		"checked_at": time.Now().Format(time.RFC3339),
		"releases":   []map[string]string{{"tag_name": "v0.3.0"}},
	}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o644)

	if got := readUpdateNudge("wyk v0.3.0"); got != "" {
		t.Errorf("up-to-date binary should produce no nudge; got %q", got)
	}
}

func TestReadUpdateNudge_EmptyWhenCacheMissing(t *testing.T) {
	// No cache file → silent (no nudge). The user just hasn't
	// done a background check yet.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	if got := readUpdateNudge("wyk v0.3.0"); got != "" {
		t.Errorf("missing cache should produce no nudge; got %q", got)
	}
}

func TestReadUpdateNudge_LegacyCacheBackCompat(t *testing.T) {
	// A cache written by v0.3.0 carries only `latest`; the new
	// reader must still produce a nudge from it.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	path, _ := updater.CachePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	entry := map[string]any{
		"checked_at": time.Now().Format(time.RFC3339),
		"latest":     map[string]string{"tag_name": "v0.9.9"},
	}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o644)

	if got := readUpdateNudge("wyk v0.3.0"); got == "" {
		t.Errorf("legacy cache shape should still produce a nudge; got empty")
	}
}

// captureStdout swaps os.Stdout for a pipe, runs fn, then returns
// what fn wrote. Used to assert the printed install command across
// runUpdate's channel-dispatch branches without refactoring the
// function to take an io.Writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// stubLiveFetcher swaps liveFetcher for the duration of a test
// and points XDG_CACHE_HOME at a per-test tempdir so PersistLatest
// doesn't trample the user's real cache.
func stubLiveFetcher(t *testing.T, rels []updater.Release) func() {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) { return rels, nil }
	return func() { liveFetcher = prev }
}

func TestRunUpdate_ChannelStablePicksTheStable(t *testing.T) {
	// Stub the live fetch: prerelease at [0], stable beneath. The
	// stable-channel dispatch must pick the stable, NOT abort with
	// "latest is a prerelease".
	restore := stubLiveFetcher(t, []updater.Release{
		{TagName: "v0.9.9-alpha", Prerelease: true},
		{TagName: "v0.9.0", Prerelease: false},
	})
	defer restore()

	out := captureStdout(t, func() {
		if code := runUpdate([]string{"-channel", "stable", "-dry-run"}); code != 0 {
			t.Errorf("runUpdate dry-run exit %d, want 0", code)
		}
	})
	if !strings.Contains(out, "v0.9.0") {
		t.Errorf("stable channel should print install for v0.9.0; got:\n%s", out)
	}
	if strings.Contains(out, "v0.9.9-alpha") {
		t.Errorf("stable channel must NOT advertise the prerelease; got:\n%s", out)
	}
}

func TestRunUpdate_ChannelAnyPicksThePrerelease(t *testing.T) {
	// `-channel any` is the default. With a prerelease at [0],
	// it should choose THAT (the absolute newest).
	restore := stubLiveFetcher(t, []updater.Release{
		{TagName: "v0.9.9-alpha", Prerelease: true},
		{TagName: "v0.9.0", Prerelease: false},
	})
	defer restore()

	out := captureStdout(t, func() {
		if code := runUpdate([]string{"-channel", "any", "-dry-run"}); code != 0 {
			t.Errorf("runUpdate dry-run exit %d, want 0", code)
		}
	})
	if !strings.Contains(out, "v0.9.9-alpha") {
		t.Errorf("any-channel should print install for the newest entry (prerelease); got:\n%s", out)
	}
}

func TestRunUpdate_ChannelStableAllPrereleasesExitsCleanly(t *testing.T) {
	// All entries in the page are prereleases — `-channel stable`
	// has nothing to install. Must exit 0 (not an error) with a
	// stderr line pointing the user at `-channel any`.
	restore := stubLiveFetcher(t, []updater.Release{
		{TagName: "v0.9.9-alpha", Prerelease: true},
		{TagName: "v0.9.9-beta1", Prerelease: true},
	})
	defer restore()

	if code := runUpdate([]string{"-channel", "stable", "-dry-run"}); code != 0 {
		t.Errorf("all-prereleases stable channel should exit 0 (informational), not an error; got %d", code)
	}
}

func TestRunUpdate_LiveResultIsPersistedForNextNudge(t *testing.T) {
	// Regression for cqq: runUpdate must write the fresh fetch
	// back to the cache so the next TUI nudge reflects what the
	// user just saw — without this, the user could install v0.3.2
	// via wyk update, then open the TUI and still see a "↑ wyk
	// v0.3.2 available" nudge against their old cached snapshot.
	restore := stubLiveFetcher(t, []updater.Release{
		{TagName: "v9.9.9", Prerelease: false},
	})
	defer restore()

	if code := runUpdate([]string{"-dry-run"}); code != 0 {
		t.Fatalf("runUpdate dry-run exit %d, want 0", code)
	}

	// Cache should now reflect the live result.
	rels, _, err := updater.LatestCached(t.Context(), nil)
	if err != nil {
		t.Fatalf("LatestCached after runUpdate: %v", err)
	}
	if len(rels) == 0 || rels[0].TagName != "v9.9.9" {
		t.Errorf("expected cache to contain v9.9.9 after runUpdate; got %+v", rels)
	}
}

func TestRunUpdate_RejectsUnknownChannel(t *testing.T) {
	// -channel typos (`stabel`, `prerelase`) are usage errors;
	// silently falling through to "any" would hand prereleases to
	// a user who specifically asked for stable.
	if code := runUpdate([]string{"-channel", "stabel", "-dry-run"}); code != 64 {
		t.Errorf("unknown -channel value should exit 64; got %d", code)
	}
	if code := runUpdate([]string{"-channel", "prerelease", "-dry-run"}); code != 64 {
		t.Errorf("unknown -channel value should exit 64; got %d", code)
	}
}

func TestRunUpdate_LiveFailureFallsBackToCacheWithWarning(t *testing.T) {
	// Network outage: liveFetcher errors. With a planted cache,
	// runUpdate must use the cached snapshot, print the stderr
	// warning, and proceed normally (not exit 1).
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return nil, errors.New("simulated network outage")
	}
	defer func() { liveFetcher = prev }()

	// Seed the cache with a release strictly newer than the test
	// binary's reported "(devel)" so the fallback has work to
	// advertise.
	if err := updater.PersistLatest([]updater.Release{
		{TagName: "v8.8.8", Prerelease: false},
	}, ""); err != nil {
		t.Fatal(err)
	}

	oldErr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr
	defer func() { os.Stderr = oldErr }()

	out := captureStdout(t, func() {
		if code := runUpdate([]string{"-dry-run"}); code != 0 {
			t.Errorf("fallback path should exit 0 (cached info available); got %d", code)
		}
	})
	wErr.Close()
	stderrOut, _ := io.ReadAll(rErr)

	if !strings.Contains(out, "v8.8.8") {
		t.Errorf("expected stdout to mention the cached version; got:\n%s", out)
	}
	if !strings.Contains(string(stderrOut), "live check failed") {
		t.Errorf("expected stderr to warn about the fallback; got:\n%s", stderrOut)
	}
}

func TestRunUpdate_LiveFailureNoCacheExitsOne(t *testing.T) {
	// Genuine first-run failure: liveFetcher errors AND no cache
	// exists yet. There's nothing to fall back to — exit 1 with
	// a clear stderr message.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return nil, errors.New("no network")
	}
	defer func() { liveFetcher = prev }()

	if code := runUpdate([]string{"-dry-run"}); code != 1 {
		t.Errorf("live failure with no cache should exit 1; got %d", code)
	}
}

func TestReadUpdateNudge_StableChannelFiltersOutPrereleases(t *testing.T) {
	// User who pinned -channel stable previously: cache holds
	// both a prerelease at [0] and a stable at [1]. The nudge
	// must advertise the stable, not the prerelease.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := updater.PersistLatest([]updater.Release{
		{TagName: "v9.9.9-alpha", Prerelease: true},
		{TagName: "v9.9.0", Prerelease: false},
	}, "stable"); err != nil {
		t.Fatal(err)
	}
	nudge := readUpdateNudge("wyk v0.3.0")
	if !strings.Contains(nudge, "v9.9.0") {
		t.Errorf("stable-channel nudge should advertise the stable; got %q", nudge)
	}
	if strings.Contains(nudge, "v9.9.9-alpha") {
		t.Errorf("stable-channel nudge MUST NOT advertise the prerelease; got %q", nudge)
	}
}

func TestReadUpdateNudge_AnyChannelAdvertisesAbsoluteNewest(t *testing.T) {
	// Default ("any") and explicit "any" both surface the
	// absolute newest, prerelease or not.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := updater.PersistLatest([]updater.Release{
		{TagName: "v9.9.9-alpha", Prerelease: true},
		{TagName: "v9.9.0", Prerelease: false},
	}, "any"); err != nil {
		t.Fatal(err)
	}
	nudge := readUpdateNudge("wyk v0.3.0")
	if !strings.Contains(nudge, "v9.9.9-alpha") {
		t.Errorf("any-channel nudge should advertise the absolute newest; got %q", nudge)
	}
}

func TestPersistLatest_PreservesChannelWhenEmptyArg(t *testing.T) {
	// Background update check should NOT clobber a previously-set
	// channel preference. First call sets it; second call with
	// channel="" leaves it intact.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := updater.PersistLatest([]updater.Release{{TagName: "v1.0.0"}}, "stable"); err != nil {
		t.Fatal(err)
	}
	if got := updater.CachedChannel(); got != "stable" {
		t.Fatalf("setup: expected channel=stable; got %q", got)
	}
	// Simulate a background check: pass channel="".
	if err := updater.PersistLatest([]updater.Release{{TagName: "v1.0.1"}}, ""); err != nil {
		t.Fatal(err)
	}
	if got := updater.CachedChannel(); got != "stable" {
		t.Errorf("background check should preserve stored channel; got %q", got)
	}
}
