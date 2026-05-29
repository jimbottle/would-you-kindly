package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

func TestRunDoctorFix_InstallsMissingSkipsExistingForeign(t *testing.T) {
	// Three registered repos in a tempdir-rooted registry: one with
	// no hook (the fix target), one with wyk's hook (skip), one
	// with a foreign hook (skip with notice).
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	missing := gitInit(t)
	wykd := gitInit(t)
	foreign := gitInit(t)
	// Plant a wyk-marked hook in wykd. hookMarker is the substring
	// the doctor recognises as wyk's; the rest is arbitrary content.
	wykHook := filepath.Join(wykd, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(wykHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(wykHook, []byte("#!/bin/sh\n# "+hookMarker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write wyk hook: %v", err)
	}
	// Plant a foreign hook in foreign.
	forHook := filepath.Join(foreign, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(forHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(forHook, []byte("#!/bin/sh\n# something else\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "missing", Path: missing},
		{Name: "wykd", Path: wykd},
		{Name: "foreign", Path: foreign},
	}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	// Stub the install seam so this test doesn't actually shell
	// out to runInit (which would chdir + try `bd init` + spawn
	// the real `bd` binary). Record which dirs the fix attempted
	// to install into.
	var installed []string
	prev := installHookIn
	installHookIn = func(dir string, _ ...string) int {
		installed = append(installed, dir)
		return 0
	}
	defer func() { installHookIn = prev }()

	if code := runDoctorFix(false); code != 0 {
		t.Errorf("runDoctorFix exit %d, want 0", code)
	}
	if len(installed) != 1 || installed[0] != missing {
		t.Errorf("installHookIn called for %v, want [%q] (missing only)", installed, missing)
	}
	// wyk-marked hook untouched.
	if body, _ := os.ReadFile(wykHook); !strings.Contains(string(body), hookMarker) {
		t.Errorf("wyk hook was modified; got:\n%s", body)
	}
	// Foreign hook untouched.
	if body, _ := os.ReadFile(forHook); strings.Contains(string(body), hookMarker) {
		t.Errorf("foreign hook was re-chained without consent; got:\n%s", body)
	}
}

func TestRunDoctorFix_DryRunSkipsWrites(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	missing := gitInit(t)
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "missing", Path: missing}}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	called := false
	prev := installHookIn
	installHookIn = func(_ string, _ ...string) int { called = true; return 0 }
	defer func() { installHookIn = prev }()

	if code := runDoctorFix(true); code != 0 {
		t.Errorf("dry-run exit %d, want 0", code)
	}
	if called {
		t.Error("dry-run must not call installHookIn")
	}
}

func TestRunDoctorFix_NoRegistryReturns2(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// No registry file at all.
	stderr := captureStderr(t, func() {
		if code := runDoctorFix(false); code != 2 {
			t.Errorf("no-registry exit %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "no repos registered") {
		t.Errorf("expected 'no repos registered' message; got %q", stderr)
	}
}

func TestCheckStatus_MarshalJSON(t *testing.T) {
	cases := []struct {
		s    checkStatus
		want string
	}{
		{statusPass, `"pass"`},
		{statusWarn, `"warn"`},
		{statusFail, `"fail"`},
	}
	for _, tc := range cases {
		b, err := tc.s.MarshalJSON()
		if err != nil {
			t.Fatalf("%v: %v", tc.s, err)
		}
		if string(b) != tc.want {
			t.Errorf("status %v marshalled to %q, want %q", tc.s, b, tc.want)
		}
	}
}

func TestCheck_MarshalJSONShape(t *testing.T) {
	c := check{name: "n", status: statusWarn, detail: "d"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["name"] != "n" || got["status"] != "warn" || got["detail"] != "d" {
		t.Errorf("JSON shape drift: %s", b)
	}
}

func TestEmitDoctorJSON_VerdictReflectsHasFail(t *testing.T) {
	tmp, err := os.CreateTemp("", "doctor-json-*.json")
	if err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	checks := []check{
		{name: "ok", status: statusPass},
		{name: "broken", status: statusFail, detail: "details"},
	}
	emitDoctorJSON(tmp, checks, true)
	_ = tmp.Sync()
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out doctorJSONOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Verdict != "fail" {
		t.Errorf("verdict=%q, want fail", out.Verdict)
	}
	if len(out.Checks) != 2 {
		t.Errorf("checks count=%d, want 2", len(out.Checks))
	}
}

func TestCheckStatus_String(t *testing.T) {
	cases := []struct {
		s    checkStatus
		want string
	}{
		{statusPass, "PASS"},
		{statusWarn, "WARN"},
		{statusFail, "FAIL"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("checkStatus(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// docRepo creates a fake registered repo under a tempdir, optionally
// with a git dir, a .beads dir, and a post-commit hook of the given
// body. The Repo.Name is derived from the directory's basename, the
// path is the directory's absolute resolved path.
func docRepo(t *testing.T, withGit, withBeads bool, hookBody string) registry.Repo {
	t.Helper()
	dir := t.TempDir()
	if withGit {
		// git init produces a .git directory + .git/hooks
		cmd := exec.Command("git", "init", "--quiet", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
	if withBeads {
		if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if hookBody != "" {
		hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// EvalSymlinks for the same reason the registry does it — macOS
	// /var → /private/var would otherwise produce two distinct paths.
	resolved, _ := filepath.EvalSymlinks(dir)
	return registry.Repo{Name: filepath.Base(dir), Path: resolved}
}

func TestCheckEditor_WarnsOnFallbackPassesOnSet(t *testing.T) {
	// Unset → fallback "vi", WARN if it resolves (most systems
	// have vi).
	t.Run("fallback when EDITOR unset", func(t *testing.T) {
		t.Setenv("EDITOR", "")
		got := checkEditor()
		// The status depends on whether vi is installed; we only
		// pin the surfaced editor and the fallback note.
		if !strings.Contains(got.detail, "vi") {
			t.Errorf("expected vi in detail; got %+v", got)
		}
		if got.status == statusPass && !strings.Contains(got.detail, "fallback") {
			t.Errorf("WARN-style detail should mention fallback; got %+v", got)
		}
	})

	t.Run("pass when EDITOR set and resolves", func(t *testing.T) {
		// Point EDITOR at a binary we know is on PATH on every
		// reasonable test host.
		t.Setenv("EDITOR", "true")
		got := checkEditor()
		if got.status != statusPass {
			t.Errorf("EDITOR=true should PASS; got %+v", got)
		}
		if !strings.Contains(got.detail, "true") {
			t.Errorf("detail should name the resolved binary; got %+v", got)
		}
	})

	t.Run("fail when EDITOR set but missing", func(t *testing.T) {
		t.Setenv("EDITOR", "this-binary-cannot-exist-12345")
		got := checkEditor()
		if got.status != statusFail {
			t.Errorf("missing binary should FAIL; got %+v", got)
		}
	})
}

func TestCheckActor_PrefersBeadsActor(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "the-actor")
	got := checkActor()
	if got.status != statusPass || !strings.Contains(got.detail, "the-actor") {
		t.Errorf("BEADS_ACTOR should win; got %+v", got)
	}
}

func TestCheckActor_FallsBackToUser(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "")
	// Force git config to fail / return empty by pointing HOME
	// at a tempdir (git falls back to global config which won't
	// exist for this user). This also makes the test independent
	// of the developer's machine.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USER", "ev")
	got := checkActor()
	// May either land on git (if a system-wide config exists) or
	// on $USER; the contract we pin is "not WARN".
	if got.status == statusWarn {
		t.Errorf("with $USER set, actor should resolve; got WARN: %+v", got)
	}
}

func TestCheckXDGPaths_PassesWhenFilePresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wyk := filepath.Join(dir, "wyk")
	if err := os.MkdirAll(wyk, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed only the registry file. The other two should still
	// land as WARN (not yet created) — pin both branches at
	// once.
	if err := os.WriteFile(filepath.Join(wyk, "repos.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := checkXDGPaths()
	if len(got) != 3 {
		t.Fatalf("expected 3 path checks; got %d", len(got))
	}
	// First entry is repos.json → PASS.
	if got[0].status != statusPass {
		t.Errorf("repos.json should PASS when present; got %+v", got[0])
	}
	// ui.json / filters.json → WARN (not yet created).
	for i := 1; i < 3; i++ {
		if got[i].status != statusWarn {
			t.Errorf("%s should WARN when absent; got %+v", got[i].name, got[i])
		}
	}
}

func TestCheckRepo_MissingGitFails(t *testing.T) {
	r := docRepo(t, false, false, "") // no .git
	checks := checkRepo(r)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check (the .git fail short-circuits); got %d", len(checks))
	}
	if checks[0].status != statusFail {
		t.Errorf("expected FAIL for missing .git; got %s", checks[0].status)
	}
}

func TestCheckRepo_MissingBeadsFailsButContinues(t *testing.T) {
	r := docRepo(t, true, false, "") // .git but no .beads, no hook
	checks := checkRepo(r)
	// Expect: .beads FAIL + post-commit-missing WARN.
	gotFail := false
	gotHookWarn := false
	for _, c := range checks {
		if strings.Contains(c.name, ".beads") && c.status == statusFail {
			gotFail = true
		}
		if strings.Contains(c.name, "post-commit") && c.status == statusWarn {
			gotHookWarn = true
		}
	}
	if !gotFail {
		t.Errorf("expected a .beads FAIL check among %+v", checks)
	}
	if !gotHookWarn {
		t.Errorf("expected a post-commit WARN check among %+v", checks)
	}
}

func TestCheckRepo_WykHookPlainPasses(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init`. line\nexec wyk hook post-commit\n")
	checks := checkRepo(r)
	foundPlain := false
	for _, c := range checks {
		if strings.Contains(c.name, "post-commit hook (wyk)") && c.status == statusPass {
			foundPlain = true
		}
	}
	if !foundPlain {
		t.Errorf("expected a PASS for the plain wyk hook among %+v", checks)
	}
}

func TestCheckRepo_ForeignHookWarns(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# roborev or some other tool\necho ok\n")
	checks := checkRepo(r)
	foundForeign := false
	for _, c := range checks {
		if strings.Contains(c.name, "foreign") && c.status == statusWarn {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Errorf("expected a WARN for the foreign hook among %+v", checks)
	}
}

func TestCheckRepo_ChainedHookMissingPreWykFails(t *testing.T) {
	// Chained marker present in hook body but no .pre-wyk file → FAIL
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init -chain`.\nexec wyk hook post-commit\n")
	checks := checkRepo(r)
	foundFail := false
	for _, c := range checks {
		if strings.Contains(c.name, ".pre-wyk") && c.status == statusFail {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected a FAIL for missing .pre-wyk on chained hook among %+v", checks)
	}
}

func TestCheckRepo_ChainedHookWithPreWykPasses(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init -chain`.\nexec wyk hook post-commit\n")
	// Create the .pre-wyk file.
	preWyk := filepath.Join(r.Path, ".git", "hooks", "post-commit.pre-wyk")
	if err := os.WriteFile(preWyk, []byte("#!/bin/sh\n# preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	checks := checkRepo(r)
	foundChained := false
	for _, c := range checks {
		if strings.Contains(c.name, "chained") && c.status == statusPass {
			foundChained = true
		}
	}
	if !foundChained {
		t.Errorf("expected a PASS for chained hook with .pre-wyk among %+v", checks)
	}
}

// TestCheckRepo_GitlinkSubdirHookResolves regresses would-you-kindly-2m9:
// pre-fix doctor read `<r.Path>/.git/hooks/post-commit` directly,
// which errored "not a directory" when `.git` was a *file*
// containing `gitdir: <path>` (the layout `git worktree add` and
// submodules create). The fix routes through `git rev-parse` so the
// hook in the parent's resolved git dir is found and classified
// normally.
func TestCheckRepo_GitlinkSubdirHookResolves(t *testing.T) {
	parent := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Plant a wyk hook in the parent's resolved git dir.
	hookPath := filepath.Join(parent, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# Installed by `wyk init`.\nexec wyk hook post-commit\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Build the gitlink subdir: <parent>/sub/.git is a FILE with
	// `gitdir: <parent>/.git`. Must also have a .beads/ so the
	// hook check runs (it follows the .beads PASS branch).
	sub := filepath.Join(parent, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+filepath.Join(parent, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(sub)
	r := registry.Repo{Name: "sub", Path: resolved}
	checks := checkRepo(r)
	// Pre-fix this produced a FAIL with "open .../sub/.git/hooks/post-commit: not a directory".
	// Post-fix: should classify as the plain wyk hook (PASS).
	foundPlain := false
	for _, c := range checks {
		if c.status == statusFail {
			t.Errorf("did not expect FAIL on gitlink subdir; got %q: %s", c.name, c.detail)
		}
		if strings.Contains(c.name, "post-commit hook (wyk)") && c.status == statusPass {
			foundPlain = true
		}
	}
	if !foundPlain {
		t.Errorf("expected gitlink subdir's hook to be classified as plain wyk PASS; got %+v", checks)
	}
}
