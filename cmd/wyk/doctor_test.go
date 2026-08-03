package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/skills"
)

func TestClassifyBDVersion(t *testing.T) {
	cases := []struct {
		name         string
		out          string
		wantStatus   checkStatus
		wantInDetail string
	}{
		{"current passes", "bd version 1.0.4 (ce242a879: main@ce242a879678)", statusPass, "within the supported range"},
		{"exact minimum passes", "bd version 1.0.0", statusPass, "supported range"},
		{"too old fails", "bd version 0.9.9 (abc)", statusFail, "older than the minimum"},
		{"newer major warns", "bd version 2.0.0 (xyz)", statusWarn, "newer than the latest tested major"},
		{"unparseable warns", "bd: command not found", statusWarn, "couldn't parse a version"},
		{"empty warns", "", statusWarn, "couldn't parse a version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, detail := classifyBDVersion(tc.out)
			if st != tc.wantStatus {
				t.Errorf("status = %v, want %v (detail: %q)", st, tc.wantStatus, detail)
			}
			if !strings.Contains(detail, tc.wantInDetail) {
				t.Errorf("detail %q does not contain %q", detail, tc.wantInDetail)
			}
		})
	}
}

func TestRunDoctorFix_InstallsMissingSkipsExistingForeign(t *testing.T) {
	// Three registered repos in a tempdir-rooted registry: one with
	// no hook (the fix target), one with wyk's hook (skip), one
	// with a foreign hook (skip with notice).
	// Stand outside any bd workspace. runDoctorFix registers the cwd's
	// workspace, and the test binary runs in cmd/wyk — whose repo root IS
	// a bd workspace (.beads/ is tracked). Without this the repo under
	// test is appended to reg.Repos and the hook loop processes it, so the
	// assertions below depend on whether the developer's checkout happens
	// to have a post-commit hook. A clean clone (i.e. CI) has none, so the
	// install branch fires and the counts are off by one (roborev #3041).
	t.Chdir(t.TempDir())
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	withTempHome(t) // isolate the skills install from the real ~/.claude

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
	// Stand outside any bd workspace. runDoctorFix registers the cwd's
	// workspace, and the test binary runs in cmd/wyk — whose repo root IS
	// a bd workspace (.beads/ is tracked). Without this the repo under
	// test is appended to reg.Repos and the hook loop processes it, so the
	// assertions below depend on whether the developer's checkout happens
	// to have a post-commit hook. A clean clone (i.e. CI) has none, so the
	// install branch fires and the counts are off by one (roborev #3041).
	t.Chdir(t.TempDir())
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	withTempHome(t) // isolate skills install
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

func TestRunDoctorFix_PartialFailureExits1(t *testing.T) {
	// Two repos missing hooks; the stubbed installer fails the
	// first one and succeeds on the second. Exit 1 reflects the
	// aggregated failure; the second install still ran (no
	// short-circuit on first error).
	// Stand outside any bd workspace. runDoctorFix registers the cwd's
	// workspace, and the test binary runs in cmd/wyk — whose repo root IS
	// a bd workspace (.beads/ is tracked). Without this the repo under
	// test is appended to reg.Repos and the hook loop processes it, so the
	// assertions below depend on whether the developer's checkout happens
	// to have a post-commit hook. A clean clone (i.e. CI) has none, so the
	// install branch fires and the counts are off by one (roborev #3041).
	t.Chdir(t.TempDir())
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	withTempHome(t) // isolate skills install
	a := gitInit(t)
	b := gitInit(t)
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "a", Path: a},
		{Name: "b", Path: b},
	}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	var attempted []string
	prev := installHookIn
	installHookIn = func(dir string, _ ...string) int {
		attempted = append(attempted, dir)
		if dir == a {
			return 1 // fail the first
		}
		return 0
	}
	defer func() { installHookIn = prev }()

	// Swallow stderr so the failure message doesn't clutter the test log.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()

	if code := runDoctorFix(false); code != 1 {
		t.Errorf("partial-failure exit %d, want 1", code)
	}
	if len(attempted) != 2 {
		t.Errorf("installHookIn called %d times, want 2 (no short-circuit on first error)", len(attempted))
	}
}

func TestRunDoctor_FlagCombinationGuards(t *testing.T) {
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()
	if code := runDoctor([]string{"-json", "-fix"}); code != 64 {
		t.Errorf("-json+-fix exit %d, want 64", code)
	}
	if code := runDoctor([]string{"-dry-run"}); code != 64 {
		t.Errorf("-dry-run without -fix exit %d, want 64", code)
	}
}

func TestRunDoctorFix_NoRegistryReturns2(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// Stand outside any bd workspace: -fix registers the cwd's workspace
	// when there is one, which would itself be fixable work and defeat the
	// "nothing left to fix" case under test. Without this the test passes
	// or fails depending on where the source tree lives.
	t.Chdir(t.TempDir())
	// Pre-install the user skills so the skills-fix step is a no-op;
	// then with no registry there's genuinely nothing left to fix → 2.
	dir := withTempHome(t)
	if _, err := installMissingSkills(dir, false); err != nil {
		t.Fatal(err)
	}
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

// TestRunDoctorFix_NoRegistryInstallsMissingSkillsReturns0 exercises the
// skills-install wiring inside runDoctorFix directly: with no repos
// registered but the user skills missing, -fix should install them and
// return 0 (the "we fixed the skills even though there were no repos"
// branch), writing the skills to disk.
func TestRunDoctorFix_NoRegistryInstallsMissingSkillsReturns0(t *testing.T) {
	// Stand outside any bd workspace. runDoctorFix registers the cwd's
	// workspace, and the test binary runs in cmd/wyk — whose repo root IS
	// a bd workspace (.beads/ is tracked). Without this the repo under
	// test is appended to reg.Repos and the hook loop processes it, so the
	// assertions below depend on whether the developer's checkout happens
	// to have a post-commit hook. A clean clone (i.e. CI) has none, so the
	// install branch fires and the counts are off by one (roborev #3041).
	t.Chdir(t.TempDir())
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := withTempHome(t) // fresh temp HOME → skills start missing
	// No registry file at all, so the only fixable work is the skills.
	if code := runDoctorFix(false); code != 0 {
		t.Errorf("no-registry-but-skills-missing exit %d, want 0", code)
	}
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if st, _ := skillStateAt(s, dir); st != skillCurrent {
			t.Errorf("skill %q state after -fix = %v, want current (should have been installed)", s.Name, st)
		}
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

// checkNamed returns the first check whose name contains sub, or a zero
// check if none match — a small helper for asserting on one row out of
// checkRepo's slice.
func checkNamed(checks []check, sub string) (check, bool) {
	for _, c := range checks {
		if strings.Contains(c.name, sub) {
			return c, true
		}
	}
	return check{}, false
}

func TestCheckRepo_CLAUDEMDWykAware(t *testing.T) {
	// PASS when CLAUDE.md carries the wyk conventions marker; WARN when
	// the file exists without it; WARN when the file is absent. Pins the
	// three-way branch so an inverted Contains or swapped status is caught.
	cases := []struct {
		name    string
		content *string // nil → no CLAUDE.md file
		want    checkStatus
	}{
		{"with marker", strPtr("# Doc\n\n" + wykConventionsBeginMarker + "\n...\n" + wykConventionsEndMarker + "\n"), statusPass},
		{"without marker", strPtr("# Doc\n\nno wyk block here\n"), statusWarn},
		{"no file", nil, statusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := docRepo(t, true, true, "") // git + beads so checkRepo reaches the CLAUDE.md branch
			if tc.content != nil {
				if err := os.WriteFile(filepath.Join(r.Path, "CLAUDE.md"), []byte(*tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			c, ok := checkNamed(checkRepo(r), "CLAUDE.md wyk-aware")
			if !ok {
				t.Fatalf("no 'CLAUDE.md wyk-aware' check emitted for %+v", checkRepo(r))
			}
			if c.status != tc.want {
				t.Errorf("status = %s, want %s (detail: %s)", c.status, tc.want, c.detail)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

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
	// Seed only the registry file. The others (ui.json, filters.json,
	// config.json) should still land as WARN (not yet created) — pin
	// both branches at once.
	if err := os.WriteFile(filepath.Join(wyk, "repos.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := checkXDGPaths()
	if len(got) != 4 {
		t.Fatalf("expected 4 path checks; got %d", len(got))
	}
	// First entry is repos.json → PASS.
	if got[0].status != statusPass {
		t.Errorf("repos.json should PASS when present; got %+v", got[0])
	}
	// ui.json / filters.json / config.json → WARN (not yet created).
	for i := 1; i < len(got); i++ {
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

func TestDoltRemoteCheck(t *testing.T) {
	// Error -> skip (no row).
	if _, ok := doltRemoteCheck("repo x", nil, os.ErrClosed); ok {
		t.Error("bd dolt error should skip the check")
	}
	// A URL present -> PASS.
	c, ok := doltRemoteCheck("repo x", []byte("origin   git+ssh://git@github.com/o/r.git\n"), nil)
	if !ok || c.status != statusPass {
		t.Errorf("remote present should PASS; got ok=%v status=%v", ok, c.status)
	}
	// Empty / no-remote message -> WARN.
	for _, out := range [][]byte{[]byte(""), []byte("No remotes configured\n"), []byte("   \n")} {
		c, ok := doltRemoteCheck("repo x", out, nil)
		if !ok || c.status != statusWarn {
			t.Errorf("no remote (%q) should WARN; got ok=%v status=%v", out, ok, c.status)
		}
	}
}

func TestClassifyGuardHook(t *testing.T) {
	const good = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"wyk hook bd-create-guard"}]}]}}`
	const noHook = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"something else"}]}]}}`

	// Present -> PASS, with the trust caveat.
	c := classifyGuardHook("repo x", "/p", []byte(good), nil)
	if c.status != statusPass {
		t.Errorf("present hook should PASS; got %v", c.status)
	}
	if !strings.Contains(c.detail, "/hooks") || !strings.Contains(strings.ToLower(c.detail), "trust") {
		t.Errorf("PASS detail must caveat that Claude Code must trust/run it; got %q", c.detail)
	}
	// Missing file -> WARN.
	if c := classifyGuardHook("repo x", "/p", nil, os.ErrNotExist); c.status != statusWarn {
		t.Errorf("missing settings.json should WARN; got %v", c.status)
	}
	// Invalid JSON -> WARN.
	if c := classifyGuardHook("repo x", "/p", []byte("{not json"), nil); c.status != statusWarn || !strings.Contains(c.detail, "valid JSON") {
		t.Errorf("invalid JSON should WARN about JSON; got %v %q", c.status, c.detail)
	}
	// Valid but hook absent -> WARN.
	if c := classifyGuardHook("repo x", "/p", []byte(noHook), nil); c.status != statusWarn || !strings.Contains(c.detail, "missing") {
		t.Errorf("hook-absent should WARN as missing; got %v %q", c.status, c.detail)
	}
}

func TestClaudeBlockSalienceNote(t *testing.T) {
	long := strings.Repeat("filler\n", 300)
	block := wykConventionsBeginPrefix + " v:1 -->\n"
	// Block near the bottom of a long file -> note.
	if note := claudeBlockSalienceNote([]byte(long + block)); note == "" {
		t.Error("buried block in a long file should produce a salience note")
	}
	// Block near the top of a long file -> no note.
	if note := claudeBlockSalienceNote([]byte(block + long)); note != "" {
		t.Errorf("top-placed block should not nag; got %q", note)
	}
	// Short file -> no note even if at the bottom.
	if note := claudeBlockSalienceNote([]byte("a\nb\n" + block)); note != "" {
		t.Errorf("short file should not nag; got %q", note)
	}
	// No block -> empty.
	if note := claudeBlockSalienceNote([]byte(long)); note != "" {
		t.Errorf("no block -> empty; got %q", note)
	}
}

func TestCheckContractHygiene(t *testing.T) {
	mk := func(id string, labels []string, desc, assignee string) beads.Issue {
		return beads.Issue{ID: id, Labels: labels, Description: desc, Assignee: assignee}
	}
	manyOrphans := func() []beads.Issue {
		var out []beads.Issue
		for _, id := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
			out = append(out, mk(id, []string{"src:agent"}, "", ""))
		}
		return out
	}
	tests := []struct {
		name      string
		issues    []beads.Issue
		want      checkStatus
		inDetail  []string
		notDetail []string
	}{
		{
			name: "clean queue passes",
			issues: []beads.Issue{
				mk("a", []string{"human", "src:agent"}, "do the thing", ""), // human: runbook + provenance
				mk("b", []string{"src:agent"}, "", "alice"),                 // agent task, assigned
				mk("c", []string{"agent-handoff"}, "", ""),                  // another agent's, skipped
			},
			want: statusPass,
		},
		{
			name:     "human task with whitespace-only runbook warns",
			issues:   []beads.Issue{mk("h1", []string{"human", "src:agent"}, "   \n", "")},
			want:     statusWarn,
			inDetail: []string{"empty runbook", "h1"},
		},
		{
			// A human-flagged task with no src: — the filer is unknown, so
			// the hint must offer BOTH options and must NOT hand out the
			// unconditional `bd label add … src:agent` backfill command
			// (that would mis-stamp a genuinely human-filed task and pull it
			// into the agent inbox on bounce-back). roborev on w5bf voef.
			name:      "human task missing provenance warns without the src:agent backfill",
			issues:    []beads.Issue{mk("h2", []string{"human"}, "a runbook", "")},
			want:      statusWarn,
			inDetail:  []string{"provenance", "h2", "as appropriate"},
			notDetail: []string{"bd label add"},
		},
		{
			name:      "agent task without assignee is an orphan",
			issues:    []beads.Issue{mk("o1", []string{"src:agent"}, "", "")},
			want:      statusWarn,
			inDetail:  []string{"no assignee", "o1"},
			notDetail: []string{"runbook", "provenance"},
		},
		{
			// A wyk-filed issue (carries session:) with no src: label — the
			// wyk-create under-labeling bug (would-you-kindly-voef). Assigned
			// so only the provenance clause fires, not orphan.
			name:   "wyk-filed agent task missing provenance warns with the src:agent backfill",
			issues: []beads.Issue{mk("s1", []string{"session:abc123"}, "", "alice")},
			want:   statusWarn,
			// The wyk-filed case is provably src:agent, so it — and only it —
			// gets the exact `bd label add … src:agent` backfill command.
			inDetail:  []string{"provenance", "s1", "bd label add", "src:agent"},
			notDetail: []string{"no assignee"},
		},
		{
			// A legacy/hand-filed issue with neither session: nor src: is
			// "unknown source" per CONTRACT.md — NOT a violation.
			name:   "legacy issue without session or src is not flagged",
			issues: []beads.Issue{mk("legacy", []string{}, "", "alice")},
			want:   statusPass,
		},
		{
			name:   "agent-handoff is never flagged",
			issues: []beads.Issue{mk("ah", []string{"agent-handoff"}, "", "")},
			want:   statusPass,
		},
		{
			name:     "long offender lists are capped",
			issues:   manyOrphans(),
			want:     statusWarn,
			inDetail: []string{"+2 more"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkContractHygiene("repo x", tc.issues)
			if got.status != tc.want {
				t.Fatalf("status = %v, want %v (detail: %q)", got.status, tc.want, got.detail)
			}
			for _, s := range tc.inDetail {
				if !strings.Contains(got.detail, s) {
					t.Errorf("detail %q missing %q", got.detail, s)
				}
			}
			for _, s := range tc.notDetail {
				if strings.Contains(got.detail, s) {
					t.Errorf("detail %q should not contain %q", got.detail, s)
				}
			}
		})
	}
}

// checkCwdRegistered is the diagnostic half of would-you-kindly-afo3: an
// unregistered workspace loses handoffs silently, so doctor must say so
// loudly (FAIL, not WARN) rather than leaving the user to discover it
// after a P0 goes unseen.
func TestCheckCwdRegistered_FailsOnUnregisteredWorkspace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)
	t.Chdir(root)

	c, ok := checkCwdRegistered()
	if !ok {
		t.Fatal("no check reported for a bd workspace")
	}
	if c.status != statusFail {
		t.Errorf("status = %v, want FAIL", c.status)
	}
	if !strings.Contains(c.detail, "wyk registry add") {
		t.Errorf("detail %q does not name the remedy", c.detail)
	}
}

func TestCheckCwdRegistered_PassesOnRegisteredWorkspace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)
	t.Chdir(root)
	if code := runRegistry([]string{"add"}); code != 0 {
		t.Fatalf("registry add exit %d", code)
	}

	c, ok := checkCwdRegistered()
	if !ok {
		t.Fatal("no check reported for a bd workspace")
	}
	if c.status != statusPass {
		t.Errorf("status = %v, want PASS (detail %q)", c.status, c.detail)
	}
}

// Running doctor from outside any bd workspace (say $HOME) must not
// invent a failure — there is no repo to register.
func TestCheckCwdRegistered_SkippedOutsideAWorkspace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	if _, ok := checkCwdRegistered(); ok {
		t.Error("reported a check outside any bd workspace")
	}
}

func TestFixCwdRegistration(t *testing.T) {
	t.Run("registers and saves", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		root := mkWorkspace(t)
		t.Chdir(root)
		reg, regPath, err := loadRegistryForCmd()
		if err != nil {
			t.Fatal(err)
		}

		registered, failed := fixCwdRegistration(reg, regPath, false)
		if !registered || failed {
			t.Fatalf("got (registered=%v, failed=%v), want (true, false)", registered, failed)
		}
		if got := readRegistry(t); !got.Has(root) {
			t.Errorf("not persisted; registry = %+v", got.Repos)
		}
	})

	t.Run("dry-run reports without writing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		root := mkWorkspace(t)
		t.Chdir(root)
		reg, regPath, err := loadRegistryForCmd()
		if err != nil {
			t.Fatal(err)
		}

		registered, failed := fixCwdRegistration(reg, regPath, true)
		if !registered || failed {
			t.Fatalf("got (registered=%v, failed=%v), want (true, false)", registered, failed)
		}
		if got := readRegistry(t); len(got.Repos) != 0 {
			t.Errorf("dry-run wrote to the registry: %+v", got.Repos)
		}
	})

	t.Run("already registered is a no-op", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		root := mkWorkspace(t)
		t.Chdir(root)
		if code := runRegistry([]string{"add"}); code != 0 {
			t.Fatalf("registry add exit %d", code)
		}
		reg, regPath, err := loadRegistryForCmd()
		if err != nil {
			t.Fatal(err)
		}

		registered, failed := fixCwdRegistration(reg, regPath, false)
		if registered || failed {
			t.Errorf("got (registered=%v, failed=%v), want (false, false)", registered, failed)
		}
	})

	t.Run("outside a workspace is a no-op", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Chdir(t.TempDir())
		reg, regPath, err := loadRegistryForCmd()
		if err != nil {
			t.Fatal(err)
		}

		registered, failed := fixCwdRegistration(reg, regPath, false)
		if registered || failed {
			t.Errorf("got (registered=%v, failed=%v), want (false, false)", registered, failed)
		}
	})
}

// The headless, agent-driven workspace that loses handoffs is also the one
// most likely to have an EMPTY registry — so -fix must register it rather
// than bailing with "no repos registered, nothing to fix".
func TestRunDoctorFix_RegistersCwdWithEmptyRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := withTempHome(t)
	if _, err := installMissingSkills(dir, false); err != nil {
		t.Fatal(err)
	}
	// A real git repo with a bd workspace in it — the shape of the repo
	// that lost the P0. Once registered, -fix goes on to the hook step for
	// it, which needs a .git to resolve.
	root := gitInit(t)
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	prev := installHookIn
	installHookIn = func(string, ...string) int { return 0 }
	t.Cleanup(func() { installHookIn = prev })

	if code := runDoctorFix(false); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if reg := readRegistry(t); !reg.Has(resolved) {
		t.Errorf("-fix did not register the cwd workspace; registry = %+v", reg.Repos)
	}
}
