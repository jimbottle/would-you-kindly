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

// TestRunBugreport_UsageError: a trailing positional is a usage error (64).
func TestRunBugreport_UsageError(t *testing.T) {
	if code := runBugreport([]string{"extra-arg"}); code != 64 {
		t.Fatalf("runBugreport with a positional exit %d, want 64", code)
	}
}

// TestRunBugreport_WriteFailure: an unwritable -o target exits 1.
func TestRunBugreport_WriteFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A path whose parent is a regular file can't be created.
	notADir := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(notADir, "report.txt")
	if code := runBugreport([]string{"-tail", "0", "-o", bad}); code != 1 {
		t.Fatalf("runBugreport to an unwritable -o exit %d, want 1", code)
	}
}

// TestWriteBugreport_DebugLogSection: with WYK_DEBUG set, the report
// includes the debug-log tail section (exercises the debugLogPath()!="" branch).
func TestWriteBugreport_DebugLogSection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WYK_DEBUG", "1")
	var b strings.Builder
	writeBugreport(&b, 10)
	if !strings.Contains(b.String(), "## debug log") {
		t.Fatalf("expected a debug-log section when WYK_DEBUG is set; got:\n%s", b.String())
	}
}

// TestWriteBugreport_DoctorMultilineIndented: multi-line doctor details
// (the always-present handoff stanza) must be indented, not flush-left, so
// the report's structure holds (roborev on w5bf.3).
func TestWriteBugreport_DoctorMultilineIndented(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var b strings.Builder
	writeBugreport(&b, 0)
	// The handoff-convention check carries a multi-line detail; every one
	// of its continuation lines should be indented under the doctor section.
	inDoctor := false
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, "## ") {
			inDoctor = strings.Contains(line, "doctor")
			continue
		}
		if inDoctor && line != "" && !strings.HasPrefix(line, "  ") {
			t.Fatalf("doctor section line is flush-left (not indented): %q", line)
		}
	}
}
