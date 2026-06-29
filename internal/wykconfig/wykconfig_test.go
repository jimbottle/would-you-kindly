package wykconfig

import (
	"errors"
	"os"
	"path/filepath"
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
