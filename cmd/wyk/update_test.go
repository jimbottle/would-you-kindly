package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		"checked_at": "2026-05-28T00:00:00Z",
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
		"checked_at": "2026-05-28T00:00:00Z",
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
		"checked_at": "2026-05-28T00:00:00Z",
		"latest":     map[string]string{"tag_name": "v0.9.9"},
	}
	b, _ := json.Marshal(entry)
	_ = os.WriteFile(path, b, 0o644)

	if got := readUpdateNudge("wyk v0.3.0"); got == "" {
		t.Errorf("legacy cache shape should still produce a nudge; got empty")
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
