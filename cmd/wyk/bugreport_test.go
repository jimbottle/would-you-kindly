package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBugreportEnv_Allowlist pins that the environment capture is a strict
// allowlist: WYK_*/XDG_* and the named vars are included, and an unrelated
// (potentially secret-bearing) var is never dumped.
func TestBugreportEnv_Allowlist(t *testing.T) {
	t.Setenv("WYK_DEFAULT_SCOPE", "cwd")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("SECRET_TOKEN", "do-not-leak")
	got := strings.Join(bugreportEnv(), "\n")
	for _, want := range []string{"WYK_DEFAULT_SCOPE=cwd", "NO_COLOR=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("bugreportEnv missing allowlisted %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SECRET_TOKEN") || strings.Contains(got, "do-not-leak") {
		t.Errorf("bugreportEnv leaked a non-allowlisted var; got:\n%s", got)
	}
}

// TestWriteBugreport_SectionsConfigAndLogTail pins the report shape: the
// headers, version, the dumped config.json, and the tail of the crash log.
func TestWriteBugreport_SectionsConfigAndLogTail(t *testing.T) {
	cfgHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Seed a config.json so the config section dumps real content.
	cfgPath := filepath.Join(cfgHome, "wyk", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"version":1,"compact_json":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed a crash log so the tail section has content.
	crashPath := filepath.Join(stateHome, "wyk", "crash.log")
	if err := os.MkdirAll(filepath.Dir(crashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crashPath, []byte("line-a\nline-b\nCRASH-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	writeBugreport(&b, 2)
	got := b.String()

	for _, want := range []string{
		"# wyk bug report", "version:",
		"## environment", "## doctor",
		"## config.json", "compact_json",
		"## crash log", "CRASH-MARKER",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q; full report:\n%s", want, got)
		}
	}
	// -tail 2 should have dropped the first crash-log line.
	if strings.Contains(got, "line-a") {
		t.Errorf("tail=2 should have trimmed the oldest crash line; got:\n%s", got)
	}
}

// TestRunBugreport_WritesFile pins the -o path.
func TestRunBugreport_WritesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out := filepath.Join(t.TempDir(), "report.txt")
	if code := runBugreport([]string{"-o", out, "-tail", "0"}); code != 0 {
		t.Fatalf("runBugreport exit %d, want 0", code)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("report file not written: %v", err)
	}
	if !strings.Contains(string(b), "# wyk bug report") {
		t.Fatalf("report file missing header:\n%s", b)
	}
}
