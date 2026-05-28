package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mkBeads creates dir/<sub>/.beads under root, the same on-disk
// shape a real bd workspace has.
func mkBeads(t *testing.T, root, sub string) string {
	t.Helper()
	p := filepath.Join(root, sub, ".beads")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(filepath.Join(root, sub))
	return resolved
}

func TestScanForBeadsRepos_FindsNestedWorkspaces(t *testing.T) {
	root := t.TempDir()
	wantA := mkBeads(t, root, "alpha")
	wantB := mkBeads(t, root, "deep/nested/beta")
	// Should NOT find this — it's a child of an existing repo (the
	// walk doesn't descend into the .beads directory itself, but
	// other subdirs of the repo are fine to descend into).
	// Adding a non-beads sibling just to make sure walk traversal works.
	if err := os.MkdirAll(filepath.Join(root, "alpha", "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := scanForBeadsRepos(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	sort.Strings(got)
	want := []string{wantA, wantB}
	sort.Strings(want)
	if !equal(got, want) {
		t.Errorf("scan: got %v, want %v", got, want)
	}
}

func TestScanForBeadsRepos_SkipsHeavyDirsAndHidden(t *testing.T) {
	root := t.TempDir()
	keep := mkBeads(t, root, "real")
	// These should NOT be returned, even though they have a .beads:
	// the walk prunes node_modules / vendor / hidden subtrees before
	// reaching them.
	_ = mkBeads(t, root, "node_modules/pkg")
	_ = mkBeads(t, root, "vendor/dep")
	_ = mkBeads(t, root, ".cache/orphan")

	got, err := scanForBeadsRepos(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 1 || got[0] != keep {
		t.Errorf("scan: got %v, want exactly [%s]", got, keep)
	}
}

func TestScanForBeadsRepos_EmptyDirReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := scanForBeadsRepos(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no results from an empty tree; got %v", got)
	}
}

func TestRunScanAndRegister_AddsAndDedupesAcrossRuns(t *testing.T) {
	// Redirect XDG so the test doesn't touch the real registry.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	root := t.TempDir()
	_ = mkBeads(t, root, "one")
	_ = mkBeads(t, root, "two")

	if code := runScanAndRegister(root, false); code != 0 {
		t.Fatalf("first scan: exit %d", code)
	}
	regPath := filepath.Join(cfgDir, "wyk", "repos.json")
	first, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	if strings.Count(string(first), `"path"`) != 2 {
		t.Errorf("expected 2 entries after first scan; got:\n%s", first)
	}

	// Second run on the same tree: must dedupe, not double the entries.
	if code := runScanAndRegister(root, false); code != 0 {
		t.Fatalf("second scan: exit %d", code)
	}
	second, _ := os.ReadFile(regPath)
	if strings.Count(string(second), `"path"`) != 2 {
		t.Errorf("expected 2 entries (idempotent) after second scan; got:\n%s", second)
	}
}

func TestRunScanAndRegister_DryRunDoesNotWrite(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	root := t.TempDir()
	_ = mkBeads(t, root, "one")

	if code := runScanAndRegister(root, true); code != 0 {
		t.Fatalf("dry-run scan: exit %d", code)
	}
	regPath := filepath.Join(cfgDir, "wyk", "repos.json")
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("dry-run should NOT have written %s; err=%v", regPath, err)
	}
}

func TestRunScanAndRegister_NonExistentRootExits2(t *testing.T) {
	code := runScanAndRegister("/path/that/does/not/exist/xyz", false)
	if code != 2 {
		t.Errorf("expected exit 2 for a missing root; got %d", code)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
