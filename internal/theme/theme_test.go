package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile_ParsesPartialOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.json")
	body := []byte(`{"human_badge_bg":"#ff0000","status_open":"42"}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.HumanBadgeBG != "#ff0000" {
		t.Errorf("HumanBadgeBG=%q, want #ff0000", got.HumanBadgeBG)
	}
	if got.StatusOpen != "42" {
		t.Errorf("StatusOpen=%q, want 42", got.StatusOpen)
	}
	if got.AgentBadgeBG != "" {
		t.Errorf("AgentBadgeBG=%q, want empty (default fall-through)", got.AgentBadgeBG)
	}
}

func TestLoadDefault_MissingFileNoError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := LoadDefault()
	if err != nil {
		t.Errorf("LoadDefault on missing file should be nil-error; got %v", err)
	}
	if (got != Theme{}) {
		t.Errorf("expected zero Theme; got %+v", got)
	}
}

func TestLoadDefault_FindsFileInXDG(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := filepath.Join(cfg, "wyk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"cursor":"99"}`)
	if err := os.WriteFile(filepath.Join(dir, "theme.json"), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if got.Cursor != "99" {
		t.Errorf("Cursor=%q, want 99", got.Cursor)
	}
}

func TestLoadFile_MalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Error("expected error for malformed JSON; got nil")
	}
}
