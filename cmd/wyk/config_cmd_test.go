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

func TestRunConfig_SetBoolKeyPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code := runConfig([]string{"set", "disable_update_check", "true"}); code != 0 {
		t.Fatalf("set bool exit %d, want 0", code)
	}
	cfg := loadConfigBestEffort()
	if !cfg.DisableUpdateCheck {
		t.Fatal("disable_update_check not persisted")
	}
}

func TestRunConfig_SetBoolRejectsNonBool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withSilencedStderr(t)
	if code := runConfig([]string{"set", "compact_json", "yes-please"}); code != 64 {
		t.Fatalf("set non-bool exit %d, want 64", code)
	}
}

func TestRunConfig_SetColorValidatesEnum(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code := runConfig([]string{"set", "color", "never"}); code != 0 {
		t.Fatalf("set color never exit %d, want 0", code)
	}
	withSilencedStderr(t)
	if code := runConfig([]string{"set", "color", "rainbow"}); code != 64 {
		t.Fatalf("set color rainbow exit %d, want 64", code)
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

func TestRunConfig_HandoffHookKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withSilencedStderr(t)
	path, _ := wykconfig.DefaultPath()

	if code := runConfig([]string{"set", "hooks.handoff.command", "node ~/bin/hook.mjs"}); code != 0 {
		t.Fatalf("set command exit %d, want 0", code)
	}
	if code := runConfig([]string{"set", "hooks.handoff.timeout_seconds", "30"}); code != 0 {
		t.Fatalf("set timeout exit %d, want 0", code)
	}
	if code := runConfig([]string{"set", "hooks.handoff.events", "handoff, close"}); code != 0 {
		t.Fatalf("set events exit %d, want 0", code)
	}
	cfg, err := wykconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := cfg.Hooks.Handoff
	if h == nil || h.Command != "node ~/bin/hook.mjs" || h.TimeoutSeconds != 30 || len(h.Events) != 2 || h.Events[1] != "close" {
		t.Fatalf("persisted hook = %+v", h)
	}

	// Validation maps to the usage exit code and leaves the file alone.
	for _, bad := range [][]string{
		{"set", "hooks.handoff.timeout_seconds", "soon"},
		{"set", "hooks.handoff.timeout_seconds", "-5"},
		{"set", "hooks.handoff.events", "handoff,reopen"},
	} {
		if code := runConfig(bad); code != 64 {
			t.Errorf("%v: exit %d, want 64", bad, code)
		}
	}
	cfg, _ = wykconfig.Load(path)
	if cfg.Hooks.Handoff.TimeoutSeconds != 30 || len(cfg.Hooks.Handoff.Events) != 2 {
		t.Fatalf("rejected values leaked into the file: %+v", cfg.Hooks.Handoff)
	}

	// Clearing the command disables the hook and drops the block.
	if code := runConfig([]string{"set", "hooks.handoff.command", ""}); code != 0 {
		t.Fatalf("clear command exit %d, want 0", code)
	}
	cfg, _ = wykconfig.Load(path)
	if cfg.Hooks.Handoff != nil {
		t.Fatalf("hook block survived clearing the command: %+v", cfg.Hooks.Handoff)
	}
}
