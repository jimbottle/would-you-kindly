package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedWykConventions_CreatesWhenAbsent: no CLAUDE.md → a new file
// with the preamble and the marker-delimited block.
func TestSeedWykConventions_CreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	action, err := seedWykConventions(dir, false)
	if err != nil {
		t.Fatalf("seedWykConventions: %v", err)
	}
	if !strings.Contains(action, "created") {
		t.Errorf("action = %q, want it to mention 'created'", action)
	}
	body, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	for _, want := range []string{
		"# Project Instructions for AI Agents",
		wykConventionsBeginMarker,
		wykConventionsEndMarker,
		"no `wyk create`",
		"HUMAN-BLOCK",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("new CLAUDE.md missing %q:\n%s", want, body)
		}
	}
}

// TestSeedWykConventions_Idempotent: a second run on a current file is a
// no-op and leaves the bytes unchanged.
func TestSeedWykConventions_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := seedWykConventions(dir, false); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))

	action, err := seedWykConventions(dir, false)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if !strings.Contains(action, "already current") {
		t.Errorf("action = %q, want 'already current'", action)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if string(before) != string(after) {
		t.Errorf("idempotent seed rewrote the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSeedWykConventions_AppendsToExisting: an existing CLAUDE.md with no
// wyk block gets the block appended, with the original content preserved
// and exactly one blank line of separation.
func TestSeedWykConventions_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	original := "# My Project\n\nSome existing instructions.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	action, err := seedWykConventions(dir, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(action, "appended") {
		t.Errorf("action = %q, want 'appended'", action)
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), original) {
		t.Errorf("append clobbered existing content:\n%s", body)
	}
	if !strings.Contains(string(body), wykConventionsBeginMarker) {
		t.Errorf("append did not add the block:\n%s", body)
	}
	if strings.Contains(string(body), "\n\n\n") {
		t.Errorf("append left more than one blank line of separation:\n%s", body)
	}
}

// TestSeedWykConventions_RefreshesStaleBlock: a CLAUDE.md whose block is
// out of date is updated in place, and content outside the markers is
// left untouched.
func TestSeedWykConventions_RefreshesStaleBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	stale := "# Top matter\n\n" +
		wykConventionsBeginMarker + "\nOLD STALE CONTENT\n" + wykConventionsEndMarker +
		"\n\n## Trailing section kept\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	action, err := seedWykConventions(dir, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(action, "refreshed") {
		t.Errorf("action = %q, want 'refreshed'", action)
	}
	body, _ := os.ReadFile(path)
	s := string(body)
	if strings.Contains(s, "OLD STALE CONTENT") {
		t.Errorf("stale content survived the refresh:\n%s", s)
	}
	if !strings.Contains(s, "# Top matter") || !strings.Contains(s, "## Trailing section kept") {
		t.Errorf("refresh disturbed content outside the markers:\n%s", s)
	}
	if !strings.Contains(s, "no `wyk create`") {
		t.Errorf("refresh did not install the current block:\n%s", s)
	}
}

// TestSeedWykConventions_RefusesMalformed: a BEGIN marker with no
// matching END is left untouched rather than corrupted.
func TestSeedWykConventions_RefusesMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	malformed := "# Doc\n\n" + wykConventionsBeginMarker + "\nhalf a block, no end marker\n"
	if err := os.WriteFile(path, []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := seedWykConventions(dir, false); err == nil {
		t.Fatal("expected an error on a BEGIN-without-END file, got nil")
	}
	body, _ := os.ReadFile(path)
	if string(body) != malformed {
		t.Errorf("malformed file was modified:\n%s", body)
	}
}

// TestSeedWykConventions_DryRunWritesNothing: dry-run reports the action
// but leaves the filesystem untouched.
func TestSeedWykConventions_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	action, err := seedWykConventions(dir, true)
	if err != nil {
		t.Fatalf("dry-run seed: %v", err)
	}
	if !strings.Contains(action, "would create") {
		t.Errorf("action = %q, want 'would create'", action)
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("dry-run created CLAUDE.md; stat err = %v", err)
	}
}

// TestInit_SkipClaudeMD: the -skip-claude-md flag suppresses the seed.
func TestInit_SkipClaudeMD(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register", "-skip-claude-md"); code != 0 {
		t.Fatalf("runInit exit %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("-skip-claude-md still wrote CLAUDE.md; stat err = %v", err)
	}
}

// TestInit_HookFollowsCoreHooksPathRedirect regresses the fresh-repo
// hook-placement bug: `bd init` points core.hooksPath at .beads/hooks,
// so wyk's auto-close hook must land THERE, not in the (now bypassed)
// .git/hooks. We simulate the redirect bd would set, then assert wyk
// installs into the redirected dir and NOT into .git/hooks.
func TestInit_HookFollowsCoreHooksPathRedirect(t *testing.T) {
	dir := gitInit(t)
	hooksDir := filepath.Join(dir, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Point git at .beads/hooks, exactly as `bd init` does.
	if out, err := exec.Command("git", "-C", dir, "config", "core.hooksPath", hooksDir).CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v\n%s", err, out)
	}

	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register", "-skip-claude-md"); code != 0 {
		t.Fatalf("runInit exit %d, want 0", code)
	}

	redirected := filepath.Join(hooksDir, "post-commit")
	if body, err := os.ReadFile(redirected); err != nil {
		t.Fatalf("hook not installed into the redirected dir %s: %v", redirected, err)
	} else if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("redirected hook isn't wyk's:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Errorf("wyk wrote into the bypassed .git/hooks dir; stat err = %v (want not-exist)", err)
	}
}

// TestSeedWykConventions_RefreshesUnversionedBlock: a block written by an
// older/hand-rolled installer (begin marker without a v:N suffix) is
// detected via the version-agnostic prefix and refreshed in place, not
// duplicated.
func TestSeedWykConventions_RefreshesUnversionedBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	unversioned := "# Doc\n\n<!-- BEGIN WYK CONVENTIONS -->\nhand-rolled old text\n" +
		wykConventionsEndMarker + "\n"
	if err := os.WriteFile(path, []byte(unversioned), 0o644); err != nil {
		t.Fatal(err)
	}
	action, err := seedWykConventions(dir, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(action, "refreshed") {
		t.Errorf("action = %q, want 'refreshed'", action)
	}
	body, _ := os.ReadFile(path)
	if strings.Count(string(body), wykConventionsEndMarker) != 1 {
		t.Errorf("expected exactly one block after refresh; got:\n%s", body)
	}
	if strings.Contains(string(body), "hand-rolled old text") {
		t.Errorf("stale unversioned content survived:\n%s", body)
	}
}

// TestInit_SeedsClaudeMDByDefault: a default init (no skip flag) leaves a
// wyk-aware CLAUDE.md behind.
func TestInit_SeedsClaudeMDByDefault(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("runInit exit %d, want 0", code)
	}
	body, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(body), wykConventionsBeginMarker) {
		t.Errorf("default init did not seed the wyk block:\n%s", body)
	}
}
