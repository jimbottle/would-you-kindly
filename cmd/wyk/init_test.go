package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
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

func TestInit_ChainPreservesForeignHook(t *testing.T) {
	// -chain is the safer alternative to -force: it preserves the
	// existing hook at .pre-wyk and writes a wrapper that runs both.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# roborev-style hook\necho roborev ran\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("expected 0 with -chain; got %d", code)
	}
	// Original was moved to .pre-wyk
	preserved, err := os.ReadFile(preWykPath)
	if err != nil {
		t.Fatalf("preserved hook missing: %v", err)
	}
	if string(preserved) != string(foreign) {
		t.Errorf(".pre-wyk content mismatch; got:\n%s", preserved)
	}
	// New hook is the chained wrapper
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "post-commit.pre-wyk") {
		t.Errorf("hook should reference the preserved .pre-wyk; got:\n%s", body)
	}
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("hook should still exec wyk; got:\n%s", body)
	}
}

func TestInit_ChainRefusesWhenPreWykAlreadyExists(t *testing.T) {
	// Guard against silently clobbering a previously-preserved hook.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# fresh foreign hook\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preWykPath, []byte("#!/bin/sh\n# previously preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("expected 64 (refuse) when .pre-wyk already exists; got %d", code)
	}
}

func TestHookScripts_BothContainMarker(t *testing.T) {
	// Re-install detection looks for hookMarker in the existing
	// hook's body to decide "this is a wyk hook, skip" vs "this is
	// foreign, refuse/-chain/-force". If a future hook variant
	// (e.g. one that adds a pre-commit dimension) forgets the
	// marker, detection silently breaks — a foreign-hook refusal
	// could fire against wyk's own previously-installed hook. Lock
	// the invariant.
	if !strings.Contains(postCommitHook, hookMarker) {
		t.Errorf("postCommitHook is missing %q — re-install detection will break for this variant", hookMarker)
	}
	if !strings.Contains(chainedPostCommitHook, hookMarker) {
		t.Errorf("chainedPostCommitHook is missing %q — re-install detection will break for this variant", hookMarker)
	}
}

func TestInit_ChainAndForceMutuallyExclusive(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-chain", "-force", "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("expected 64 when -chain and -force are both set; got %d", code)
	}
}

func TestInit_ChainDryRunReflectsRuntimeRefusal(t *testing.T) {
	// Symmetry with TestInit_ChainRefusesWhenPreWykAlreadyExists:
	// when .pre-wyk is already in place, the real -chain run refuses
	// (exit 64). The dry-run shouldn't claim "would chain" — that's
	// false advertising. Should still exit 0 (it's a dry-run) but
	// print the would-refuse message.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# fresh foreign hook\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preWykPath, []byte("#!/bin/sh\n# previously preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-chain", "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("-chain -dry-run should exit 0 even when .pre-wyk exists; got %d", code)
	}
	// Nothing should have been written.
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "fresh foreign hook") {
		t.Errorf("dry-run modified the foreign hook; got:\n%s", body)
	}
}

func TestInit_UninstallRemovesPlainHook(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook should exist before uninstall: %v", err)
	}

	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d, want 0", code)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("hook should be removed; stat err = %v", err)
	}
}

func TestInit_UninstallRestoresPreWyk(t *testing.T) {
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Foreign hook + -chain install: leaves the foreign script at .pre-wyk.
	foreign := []byte("#!/bin/sh\n# foreign tool\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("chain install exit %d", code)
	}

	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d, want 0", code)
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("post-commit missing after uninstall: %v", err)
	}
	if string(body) != string(foreign) {
		t.Errorf("uninstall should restore the foreign hook verbatim; got:\n%s", body)
	}
	preWyk := hookPath + ".pre-wyk"
	if _, err := os.Stat(preWyk); !os.IsNotExist(err) {
		t.Errorf(".pre-wyk should be gone after restore; stat err = %v", err)
	}
}

func TestInit_UninstallRefusesForeignHook(t *testing.T) {
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# foreign\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-uninstall"); code != 64 {
		t.Errorf("uninstall on foreign hook exit %d, want 64", code)
	}
	body, _ := os.ReadFile(hookPath)
	if string(body) != string(foreign) {
		t.Errorf("foreign hook should be left intact; got:\n%s", body)
	}
}

func TestInit_UninstallMissingHookIsNoop(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Errorf("uninstall on missing hook should exit 0; got %d", code)
	}
}

func TestInit_UninstallDryRunDoesNotWrite(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	statBefore, _ := os.Stat(hookPath)
	if code := runInitIn(t, dir, "-uninstall", "-dry-run"); code != 0 {
		t.Fatalf("uninstall dry-run exit %d, want 0", code)
	}
	statAfter, err := os.Stat(hookPath)
	if err != nil {
		t.Errorf("dry-run should not remove the hook: %v", err)
	}
	if statBefore.ModTime() != statAfter.ModTime() {
		t.Errorf("dry-run mutated the hook (mod time changed)")
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

func TestRunFixForeignHooks_ChainsForeignSkipsWykAndMissing(t *testing.T) {
	// Three registered repos: one with a foreign hook (chain target),
	// one with wyk's hook (skip), one with no hook (missing — left
	// alone; doctor -fix handles that case). The stubbed chain seam
	// records which repos the fix actually touched so we don't have
	// to invoke real bd.
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	foreign := gitInit(t)
	wykd := gitInit(t)
	missing := gitInit(t)

	forHook := filepath.Join(foreign, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(forHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(forHook, []byte("#!/bin/sh\n# roborev hook\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}
	wykHook := filepath.Join(wykd, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(wykHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(wykHook, []byte("#!/bin/sh\n# "+hookMarker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write wyk hook: %v", err)
	}

	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "foreign", Path: foreign},
		{Name: "wykd", Path: wykd},
		{Name: "missing", Path: missing},
	}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	var chained []string
	prev := chainHookIntoRepo
	chainHookIntoRepo = func(dir string) int {
		chained = append(chained, dir)
		return 0
	}
	defer func() { chainHookIntoRepo = prev }()

	if code := runFixForeignHooks(false); code != 0 {
		t.Errorf("runFixForeignHooks exit %d, want 0", code)
	}
	if len(chained) != 1 || chained[0] != foreign {
		t.Errorf("chainHookIntoRepo called for %v, want [%q] (foreign only)", chained, foreign)
	}
	// wyk hook untouched.
	if body, _ := os.ReadFile(wykHook); !strings.Contains(string(body), hookMarker) {
		t.Errorf("wyk hook was modified; got:\n%s", body)
	}
}

func TestRunFixForeignHooks_DryRunSkipsWrites(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	foreign := gitInit(t)
	forHook := filepath.Join(foreign, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(forHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(forHook, []byte("#!/bin/sh\n# foreign\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "foreign", Path: foreign}}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	called := false
	prev := chainHookIntoRepo
	chainHookIntoRepo = func(_ string) int { called = true; return 0 }
	defer func() { chainHookIntoRepo = prev }()

	if code := runFixForeignHooks(true); code != 0 {
		t.Errorf("dry-run exit %d, want 0", code)
	}
	if called {
		t.Error("dry-run must not call chainHookIntoRepo")
	}
}

func TestRunFixForeignHooks_NoRegistryReturns2(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	stderr := captureStderr(t, func() {
		if code := runFixForeignHooks(false); code != 2 {
			t.Errorf("missing-registry exit %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "no repos registered") && !strings.Contains(stderr, "registry") {
		t.Errorf("stderr should mention the missing registry; got %q", stderr)
	}
}

func TestInit_FixForeignHooksFlagGuards(t *testing.T) {
	// -fix-foreign-hooks is a registry-wide alternate mode; combined
	// with per-repo install flags it must error out with exit 64
	// rather than silently ignore them.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()
	for _, args := range [][]string{
		{"-fix-foreign-hooks", "-chain"},
		{"-fix-foreign-hooks", "-force"},
		{"-fix-foreign-hooks", "-scan", "/tmp"},
		{"-fix-foreign-hooks", "-uninstall"},
	} {
		if code := runInit(args); code != 64 {
			t.Errorf("runInit %v exit %d, want 64", args, code)
		}
	}
}
