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
		"`wyk create`",
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
	if !strings.Contains(s, "`wyk create`") {
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

// TestWriteFileAtomic_PreservesSymlinksAndModes pins the two
// os.WriteFile behaviors the atomic swap must not lose (the file is
// user-authored CLAUDE.md territory): writes land on a symlink's
// TARGET — the link survives, resolvable or dangling — and an
// existing file keeps its own mode, perm applying only on create.
func TestWriteFileAtomic_PreservesSymlinksAndModes(t *testing.T) {
	t.Run("fresh file gets the passed perm", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		if err := writeFileAtomic(path, []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("fresh file mode = %o, want 644", got)
		}
	})

	t.Run("existing file keeps its own mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(path, []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("rewrite reset mode to %o, want the original 600", got)
		}
	})

	t.Run("writes through a resolvable symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "AGENTS.md")
		link := filepath.Join(dir, "CLAUDE.md")
		if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("AGENTS.md", link); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(link, []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("CLAUDE.md is no longer a symlink (err=%v mode=%v)", err, fi.Mode())
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "new\n" {
			t.Errorf("target content = %q, %v; want the new body", got, err)
		}
	})

	t.Run("dangling symlink creates the target, keeps the link", func(t *testing.T) {
		// os.WriteFile would follow the link and create AGENTS.md;
		// the atomic path must do the same, not clobber the link.
		dir := t.TempDir()
		link := filepath.Join(dir, "CLAUDE.md")
		if err := os.Symlink("AGENTS.md", link); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(link, []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("CLAUDE.md is no longer a symlink (err=%v mode=%v)", err, fi.Mode())
		}
		got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil || string(got) != "body\n" {
			t.Errorf("AGENTS.md content = %q, %v; want the written body", got, err)
		}
	})
}

// TestResolveWriteTarget_ChainsAndLoops covers the two paths the
// Lstat/Readlink walk adds beyond EvalSymlinks: multi-hop chains of
// relative targets, and the loop cap (which must error naming the
// path the caller asked about, not wherever the chain wandered).
func TestResolveWriteTarget_ChainsAndLoops(t *testing.T) {
	t.Run("multi-hop chain writes the final target", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink("mid.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("AGENTS.md", filepath.Join(dir, "mid.md")); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(filepath.Join(dir, "CLAUDE.md"), []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"CLAUDE.md", "mid.md"} {
			if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("%s is no longer a symlink (err=%v)", name, err)
			}
		}
		got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil || string(got) != "body\n" {
			t.Errorf("AGENTS.md content = %q, %v; want the written body", got, err)
		}
	})

	t.Run("self-loop errors naming the asked-about path", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "CLAUDE.md")
		if err := os.Symlink("CLAUDE.md", link); err != nil {
			t.Fatal(err)
		}
		_, err := resolveWriteTarget(link)
		if err == nil {
			t.Fatal("self-loop should error, not spin or clobber")
		}
		if !strings.Contains(err.Error(), "too many levels of symbolic links") ||
			!strings.Contains(err.Error(), link) {
			t.Errorf("loop error should name the original path %q; got %q", link, err)
		}
	})

	t.Run("dangling link into a missing dir fails like os.WriteFile", func(t *testing.T) {
		// MkdirAll-on-demand applies to the path as given, not to a
		// resolved link target: a mis-pointed link must not silently
		// materialize directory trees.
		dir := t.TempDir()
		link := filepath.Join(dir, "CLAUDE.md")
		if err := os.Symlink(filepath.Join("missing-dir", "AGENTS.md"), link); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(link, []byte("body\n"), 0o644); err == nil {
			t.Fatal("write through a link into a missing dir should fail, got nil")
		}
		if _, err := os.Stat(filepath.Join(dir, "missing-dir")); !os.IsNotExist(err) {
			t.Errorf("missing-dir should not have been created (stat err=%v)", err)
		}
	})
}
