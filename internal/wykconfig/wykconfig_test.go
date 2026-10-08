package wykconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		Version:            CurrentVersion,
		DefaultScope:       ScopeCwd,
		DisableUpdateCheck: true,
		CompactJSON:        true,
		SlimJSON:           true,
		Color:              ColorNever,
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestValidateColor(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{ColorAuto, false},
		{ColorNever, false},
		{"always", true},
		{"AUTO", true},
		{"off", true},
	} {
		err := ValidateColor(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateColor(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestSaveStampsVersionWhenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{DefaultScope: ScopeAll}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", got.Version, CurrentVersion)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	want := Config{Version: CurrentVersion}
	if got != want {
		t.Fatalf("missing file: got %+v, want %+v", got, want)
	}
}

func TestLoadUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Load: err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestLoadVersionZeroTreatedAsCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"default_scope":"cwd"}`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != CurrentVersion || got.DefaultScope != ScopeCwd {
		t.Fatalf("got %+v, want version %d + scope cwd", got, CurrentVersion)
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: want parse error, got nil")
	}
	if errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal("corrupt JSON must not be reported as ErrUnsupportedVersion")
	}
	// The message must name the remedy, not just the file: settings
	// are re-settable, so pointing at `wyk config set` turns a JSON
	// parse error into an actionable one.
	if !strings.Contains(err.Error(), "wyk config set") {
		t.Errorf("parse error should name the remedy; got %q", err)
	}
}

func TestValidateScope(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{ScopeAll, false},
		{ScopeCwd, false},
		{"ALL", true},
		{"bogus", true},
		{"cwd ", true},
	} {
		err := ValidateScope(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateScope(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestDefaultPathXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join("/tmp/xdg-test", "wyk", "config.json")
	if got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestHooksRoundTripAndOmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// No hooks: the key must not appear at all, so an older wyk reading
	// the file sees exactly what it wrote.
	if err := Save(path, Config{DefaultScope: ScopeCwd}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "hooks") {
		t.Fatalf("hooks key written for a config without hooks:\n%s", b)
	}
	// With a hook: command, timeout, and events survive a round trip.
	want := Config{Hooks: HooksConfig{Handoff: &HandoffHook{
		Command: "node ~/bin/hook.mjs", TimeoutSeconds: 30, Events: []string{"handoff", "close"},
	}}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := got.Hooks.Handoff
	if h == nil || h.Command != want.Hooks.Handoff.Command || h.TimeoutSeconds != 30 || len(h.Events) != 2 || h.Events[1] != "close" {
		t.Fatalf("round-trip hook = %+v, want %+v", h, want.Hooks.Handoff)
	}
	// Hand-edited file with only a command decodes with zero defaults.
	if err := os.WriteFile(path, []byte(`{"version":1,"hooks":{"handoff":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil {
		t.Fatalf("Load minimal: %v", err)
	}
	if got.Hooks.Handoff == nil || got.Hooks.Handoff.Command != "x" || got.Hooks.Handoff.TimeoutSeconds != 0 || got.Hooks.Handoff.Events != nil {
		t.Fatalf("minimal hook = %+v", got.Hooks.Handoff)
	}
}

func TestValidateHandoffEventsAndTimeout(t *testing.T) {
	if err := ValidateHandoffEvents(nil); err != nil {
		t.Errorf("nil events: %v", err)
	}
	if err := ValidateHandoffEvents([]string{"handoff", "ping"}); err != nil {
		t.Errorf("valid subset: %v", err)
	}
	err := ValidateHandoffEvents([]string{"handoff", "reopen"})
	if !errors.Is(err, ErrInvalidValue) {
		t.Errorf("unknown event: err = %v, want ErrInvalidValue", err)
	}
	if err := ValidateHandoffTimeout(0); err != nil {
		t.Errorf("zero timeout: %v", err)
	}
	if err := ValidateHandoffTimeout(-1); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("negative timeout: err = %v, want ErrInvalidValue", err)
	}
}
