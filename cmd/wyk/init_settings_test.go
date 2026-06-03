package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsHasGuard re-reads the file and reports whether the guard hook
// is registered under PreToolUse.
func settingsHasGuard(t *testing.T, dir string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, b)
	}
	return claudeSettingsHasHook(root, claudeSettingsHook)
}

func TestSeedClaudeSettings_CreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	action, err := seedClaudeSettings(dir, false)
	if err != nil {
		t.Fatalf("seedClaudeSettings: %v", err)
	}
	if !strings.Contains(action, "registered") {
		t.Errorf("action = %q, want it to mention 'registered'", action)
	}
	if !settingsHasGuard(t, dir) {
		t.Error("guard hook not present after seeding a fresh settings.json")
	}
}

func TestSeedClaudeSettings_MergesAndPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mirror what bd init writes: SessionStart/PreCompact running bd prime.
	existing := `{
  "hooks": {
    "SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "bd prime"}]}],
    "PreCompact":   [{"matcher": "", "hooks": [{"type": "command", "command": "bd prime"}]}]
  }
}`
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := seedClaudeSettings(dir, false); err != nil {
		t.Fatalf("seedClaudeSettings: %v", err)
	}
	if !settingsHasGuard(t, dir) {
		t.Error("guard hook not added")
	}
	// The pre-existing bd prime hooks must survive the merge.
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "bd prime") || !strings.Contains(string(b), "SessionStart") {
		t.Errorf("merge dropped the existing bd hooks:\n%s", b)
	}
}

func TestSeedClaudeSettings_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := seedClaudeSettings(dir, false); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	action, err := seedClaudeSettings(dir, false)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if !strings.Contains(action, "already") {
		t.Errorf("second seed action = %q, want 'already'", action)
	}
	// Exactly one PreToolUse entry — no duplicate.
	b, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if n := strings.Count(string(b), claudeSettingsHook); n != 1 {
		t.Errorf("guard command appears %d times, want 1:\n%s", n, b)
	}
}

func TestSeedClaudeSettings_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	action, err := seedClaudeSettings(dir, true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(action, "would register") {
		t.Errorf("action = %q, want 'would register'", action)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("dry-run created settings.json; stat err = %v", err)
	}
}

func TestSeedClaudeSettings_RefusesMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := seedClaudeSettings(dir, false); err == nil {
		t.Error("expected an error on malformed settings.json, got nil")
	}
	// The malformed file must be left untouched, not overwritten.
	b, _ := os.ReadFile(path)
	if string(b) != "{ not json" {
		t.Errorf("malformed file was modified:\n%s", b)
	}
}
