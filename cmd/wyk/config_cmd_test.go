package main

import (
	"os"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// withSilencedStderr redirects os.Stderr to /dev/null for the duration
// of the test so expected error-path messages don't clutter the log.
func withSilencedStderr(t *testing.T) {
	t.Helper()
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	t.Cleanup(func() {
		os.Stderr = old
		_ = devnull.Close()
	})
}

func TestRunConfig_SetPersistsAndValidates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if code := runConfig([]string{"set", "default_scope", "cwd"}); code != 0 {
		t.Fatalf("set exit %d, want 0", code)
	}
	// Persisted to config.json.
	path, _ := wykconfig.DefaultPath()
	cfg, err := wykconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultScope != wykconfig.ScopeCwd {
		t.Fatalf("default_scope = %q, want cwd", cfg.DefaultScope)
	}
}

func TestRunConfig_SetRejectsInvalidValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withSilencedStderr(t)
	if code := runConfig([]string{"set", "default_scope", "bogus"}); code != 64 {
		t.Fatalf("set bogus exit %d, want 64", code)
	}
}

func TestRunConfig_SetRejectsUnknownKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withSilencedStderr(t)
	if code := runConfig([]string{"set", "nope", "x"}); code != 64 {
		t.Fatalf("set unknown-key exit %d, want 64", code)
	}
}

func TestRunConfig_GetUnknownKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withSilencedStderr(t)
	if code := runConfig([]string{"get", "nope"}); code != 64 {
		t.Fatalf("get unknown-key exit %d, want 64", code)
	}
}

func TestRunConfig_GetReturnsEffectiveDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Unset → get reports the effective default (all), exit 0.
	if code := runConfig([]string{"get", "default_scope"}); code != 0 {
		t.Fatalf("get exit %d, want 0", code)
	}
}

func TestRunConfig_ListAndUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code := runConfig([]string{"list"}); code != 0 {
		t.Fatalf("list exit %d, want 0", code)
	}
	withSilencedStderr(t)
	if code := runConfig(nil); code != 64 {
		t.Fatalf("no-args exit %d, want 64", code)
	}
	if code := runConfig([]string{"frobnicate"}); code != 64 {
		t.Fatalf("unknown-sub exit %d, want 64", code)
	}
}
