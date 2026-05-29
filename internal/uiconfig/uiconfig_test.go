package uiconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/xdg", "wyk", "ui.json")
	if p != want {
		t.Errorf("DefaultPath = %q, want %q", p, want)
	}
}

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", c.Version, CurrentVersion)
	}
	if len(c.HiddenColumns) != 0 {
		t.Errorf("HiddenColumns should be empty; got %v", c.HiddenColumns)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "ui.json")
	want := Config{Version: CurrentVersion, HiddenColumns: []string{"branch", "type"}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != want.Version {
		t.Errorf("version = %d, want %d", got.Version, want.Version)
	}
	if len(got.HiddenColumns) != 2 || got.HiddenColumns[0] != "branch" || got.HiddenColumns[1] != "type" {
		t.Errorf("HiddenColumns = %v, want %v", got.HiddenColumns, want.HiddenColumns)
	}
}

func TestLoad_VersionMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected version-mismatch error")
	}
	// main relies on this being a distinguishable sentinel so it
	// can disable persistence (vs. repairing a corrupt-but-current
	// file). A free-text error would force startup to treat all
	// errors the same and either repair the future schema (bad) or
	// never repair anything (also bad).
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("expected ErrUnsupportedVersion sentinel; got %v", err)
	}
}

func TestLoad_CorruptJSONIsNotVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("parse errors should NOT carry ErrUnsupportedVersion; got %v", err)
	}
}

func TestHiddenSet(t *testing.T) {
	c := Config{HiddenColumns: []string{"a", "b"}}
	s := c.HiddenSet()
	if !s["a"] || !s["b"] || s["c"] {
		t.Errorf("HiddenSet = %v, expected a,b only", s)
	}
}
