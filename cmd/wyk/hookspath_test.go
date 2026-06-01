package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

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
