package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a fresh git repo in a tempdir and returns its root.
// Used by the init tests to exercise findGitDir + write paths.
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// runInitIn runs runInit with the process cwd set to dir; mirrors how
// the real binary discovers .git via `git rev-parse --git-dir`.
func runInitIn(t *testing.T, dir string, args ...string) int {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return runInit(args)
}

func TestInit_InstallsExecutableHook(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("runInit exit %d, want 0", code)
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	st, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook not executable; mode = %v", st.Mode())
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("hook body missing the exec line:\n%s", body)
	}
	if !strings.Contains(string(body), hookMarker) {
		t.Errorf("hook body missing the marker; future reinstall detection won't work:\n%s", body)
	}
}

func TestInit_IdempotentReinstallNoForce(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("first install exit %d", code)
	}
	// Second run without -force should succeed (idempotent) since
	// the existing hook carries our marker.
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("idempotent reinstall exit %d, want 0", code)
	}
}

func TestInit_RefusesToOverwriteForeignHook(t *testing.T) {
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# some other tool's hook\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Without -force: refuse with usage exit code 64.
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("expected 64 when overwriting foreign hook without -force; got %d", code)
	}

	// With -force: replace.
	if code := runInitIn(t, dir, "-force", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("expected 0 with -force; got %d", code)
	}
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("after -force, hook should be the wyk one; got:\n%s", body)
	}
}

func TestInit_DryRunDoesNotWrite(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("dry-run exit %d, want 0", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("dry-run should not write the hook; stat err = %v", err)
	}
}

func TestInit_DryRunAgainstForeignHookReturnsZero(t *testing.T) {
	// -dry-run is observation-only. Even when a foreign hook would
	// cause the real run to refuse (exit 64), the dry-run must
	// preview and exit 0 so scripts like `wyk init -dry-run || …`
	// don't have to special-case the refusal code.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# some other tool's hook\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("-dry-run against foreign hook should exit 0; got %d", code)
	}
	// And: it must not have written.
	body, _ := os.ReadFile(hookPath)
	if string(body) != string(foreign) {
		t.Errorf("-dry-run modified the foreign hook; got:\n%s", body)
	}
}

func TestInit_OutsideRepoFailsCleanly(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 2 {
		t.Errorf("expected exit 2 outside a repo; got %d", code)
	}
}
