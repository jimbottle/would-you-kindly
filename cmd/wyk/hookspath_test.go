package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// TestInit_InstallsIntoActiveHooksDirWhenRedirectedInRepo verifies that
// when core.hooksPath redirects hooks at an in-repo dir (the bd setup),
// wyk init chains into the hook git actually runs — not a dead file in
// .git/hooks.
func TestInit_InstallsIntoActiveHooksDirWhenRedirectedInRepo(t *testing.T) {
	repo := gitInit(t)
	active := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-existing (e.g. roborev) hook already lives in the active dir.
	if err := os.WriteFile(filepath.Join(active, "post-commit"), []byte("#!/bin/sh\n# roborev\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfigSet(t, repo, "core.hooksPath", active)

	if code := runInitIn(t, repo, "-chain", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}

	body, err := os.ReadFile(filepath.Join(active, "post-commit"))
	if err != nil {
		t.Fatalf("active post-commit: %v", err)
	}
	if !bytes.Contains(body, []byte(hookMarker)) {
		t.Error("the active-dir post-commit should be wyk's chained hook after init")
	}
	if _, err := os.Stat(filepath.Join(active, "post-commit.pre-wyk")); err != nil {
		t.Errorf("the pre-existing hook should be preserved as .pre-wyk in the active dir: %v", err)
	}
	// And nothing wyk in the bypassed .git/hooks.
	if b, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit")); err == nil && bytes.Contains(b, []byte(hookMarker)) {
		t.Error("wyk must not write to .git/hooks when core.hooksPath redirects elsewhere")
	}
}

// TestInit_RefusesWhenHooksPathOutsideRepo verifies wyk init refuses to
// write when core.hooksPath points outside the repo (stale/cross-repo),
// rather than installing into another location.
func TestInit_RefusesWhenHooksPathOutsideRepo(t *testing.T) {
	repo := gitInit(t)
	outside := t.TempDir()
	gitConfigSet(t, repo, "core.hooksPath", outside)

	if code := runInitIn(t, repo, "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("init exit = %d, want 64 (refused)", code)
	}
	if _, err := os.Stat(filepath.Join(outside, "post-commit")); err == nil {
		t.Error("init must not write into the external hooks dir")
	}
}

// TestUninstall_RemovesFromActiveHooksDir verifies uninstall targets the
// active (redirected) hooks dir, matching where init installed.
func TestUninstall_RemovesFromActiveHooksDir(t *testing.T) {
	repo := gitInit(t)
	active := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfigSet(t, repo, "core.hooksPath", active)

	if code := runInitIn(t, repo, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(active, "post-commit")); !bytes.Contains(b, []byte(hookMarker)) {
		t.Fatal("setup: wyk hook should be in the active dir after init")
	}
	if code := runInitIn(t, repo, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit = %d", code)
	}
	if b, err := os.ReadFile(filepath.Join(active, "post-commit")); err == nil && bytes.Contains(b, []byte(hookMarker)) {
		t.Error("uninstall should have removed wyk's hook from the active dir")
	}
}

// TestUninstall_SweepsOrphanFromDefaultHooksDir covers the migration
// case: a hook installed by an older wyk into .git/hooks, then a
// core.hooksPath redirect added later. Uninstall must still clean the
// orphaned (now-bypassed) hook out of .git/hooks, not report "nothing".
func TestUninstall_SweepsOrphanFromDefaultHooksDir(t *testing.T) {
	repo := gitInit(t)
	gitHooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(gitHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(gitHooks, "post-commit")
	if err := os.WriteFile(orphan, []byte("#!/bin/sh\n# "+hookMarker+"`\nexec wyk hook post-commit\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// core.hooksPath now redirects to an in-repo dir with no wyk hook.
	active := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfigSet(t, repo, "core.hooksPath", active)

	if code := runInitIn(t, repo, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit = %d", code)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("uninstall should sweep the orphaned wyk hook from .git/hooks")
	}
}

// TestUninstall_SweepsOrphanDespiteForeignActiveHook is the realistic
// migration shape: core.hooksPath redirects to another tool's dir that
// already holds ITS own (foreign) hook, while a bypassed wyk hook sits
// orphaned in .git/hooks. A foreign active hook must not abort the
// sweep — the orphan is removed, the foreign hook is left untouched.
func TestUninstall_SweepsOrphanDespiteForeignActiveHook(t *testing.T) {
	repo := gitInit(t)
	gitHooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(gitHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(gitHooks, "post-commit")
	if err := os.WriteFile(orphan, []byte("#!/bin/sh\n# "+hookMarker+"`\nexec wyk hook post-commit\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Active dir holds a FOREIGN (non-wyk) hook.
	active := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(active, "post-commit")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\n# roborev\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfigSet(t, repo, "core.hooksPath", active)

	if code := runInitIn(t, repo, "-uninstall"); code != 0 {
		t.Errorf("uninstall exit = %d, want 0 (orphan swept despite foreign active hook)", code)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("orphaned wyk hook in .git/hooks should have been swept")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("the foreign active hook must be left untouched")
	}
}

func gitConfigSet(t *testing.T, repo, key, val string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", repo, "config", key, val).CombinedOutput(); err != nil {
		t.Fatalf("git config %s=%s: %v\n%s", key, val, err, out)
	}
}

func TestPathWithin(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "a", "b")
	cases := []struct {
		parent, child string
		want          bool
	}{
		{tmp, tmp, true},  // equal → within
		{tmp, sub, true},  // nested → within
		{sub, tmp, false}, // parent under child → not within
		{tmp, filepath.Join(filepath.Dir(tmp), "x"), false}, // sibling → not within
	}
	for _, c := range cases {
		if got := pathWithin(c.parent, c.child); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

// TestPathWithin_ResolvesSymlinks locks in the symlink fix: a child
// reached through a symlink to the parent (and a not-yet-created tail
// under it) must still classify as within — guarding the macOS
// /var → /private/var representation gap.
func TestPathWithin_ResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if !pathWithin(real, filepath.Join(link, "hooks")) {
		t.Errorf("symlinked in-repo child classified as outside")
	}
	if !pathWithin(real, filepath.Join(link, "nope", "deep")) {
		t.Errorf("missing in-repo tail via symlink classified as outside")
	}
}

// TestHooksPathRedirect exercises the core detection: unset, in-repo
// redirect without/with wyk's hook, and an outside-repo redirect.
func TestHooksPathRedirect(t *testing.T) {
	repo := gitInit(t)

	// Unset core.hooksPath → not redirected.
	if _, redirected, _, _ := hooksPathRedirect(repo); redirected {
		t.Errorf("unset core.hooksPath: redirected = true, want false")
	}

	// Redirect to an in-repo dir that has no wyk hook yet.
	inDir := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfigSet(t, repo, "core.hooksPath", inDir)
	active, redirected, inside, wyk := hooksPathRedirect(repo)
	if !redirected || !inside || wyk {
		t.Errorf("in-repo redirect: active=%q redirected=%v inside=%v wyk=%v; want redirected, inside, !wyk", active, redirected, inside, wyk)
	}

	// Put a wyk-marked hook at the active location → wykHookActive true,
	// so doctor/init should NOT warn (wyk's hook will run).
	hook := "#!/bin/sh\n# " + hookMarker + "`\nexec wyk hook post-commit\n"
	if err := os.WriteFile(filepath.Join(inDir, "post-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, wyk := hooksPathRedirect(repo); !wyk {
		t.Errorf("wyk-marked active hook: wykHookActive = false, want true")
	}

	// Redirect to a dir OUTSIDE the repo.
	outDir := t.TempDir()
	gitConfigSet(t, repo, "core.hooksPath", outDir)
	if _, redirected, inside, _ := hooksPathRedirect(repo); !redirected || inside {
		t.Errorf("outside-repo redirect: redirected=%v inside=%v; want redirected, !inside", redirected, inside)
	}
}

// TestCheckRepo_WarnsOnHooksPathRedirect verifies the doctor surface
// emits the explanatory WARN when core.hooksPath redirects hooks away
// from wyk's installed hook — the de-silencing of the khyo failure.
func TestCheckRepo_WarnsOnHooksPathRedirect(t *testing.T) {
	repo := gitInit(t)
	outDir := t.TempDir() // outside-repo redirect, no wyk hook there
	gitConfigSet(t, repo, "core.hooksPath", outDir)

	var got *check
	for _, c := range checkRepo(registry.Repo{Name: "x", Path: repo}) {
		if strings.Contains(c.name, "core.hooksPath redirect") {
			c := c
			got = &c
		}
	}
	if got == nil {
		t.Fatal("expected a 'core.hooksPath redirect' check, got none")
	}
	if got.status != statusWarn {
		t.Errorf("status = %v, want warn", got.status)
	}
	if !strings.Contains(got.detail, "outside this repo") {
		t.Errorf("outside-repo detail should name the stale-config remediation; got %q", got.detail)
	}
}
