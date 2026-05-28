package main

import (
	"context"
	"errors"
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

// alwaysProbeOK is the standard test probe: pretends every
// candidate workspace is bd-readable. Existing scan tests work on
// empty .beads/ subdirs that real bd would reject; the probe seam
// lets them stay focused on the walk + registration semantics
// instead of needing a real bd workspace per case.
func alwaysProbeOK(ctx context.Context, dir string) error { return nil }

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

	if code := runScanAndRegisterWithProbe(root, false, alwaysProbeOK); code != 0 {
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
	if code := runScanAndRegisterWithProbe(root, false, alwaysProbeOK); code != 0 {
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

	if code := runScanAndRegisterWithProbe(root, true, alwaysProbeOK); code != 0 {
		t.Fatalf("dry-run scan: exit %d", code)
	}
	regPath := filepath.Join(cfgDir, "wyk", "repos.json")
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("dry-run should NOT have written %s; err=%v", regPath, err)
	}
}

func TestRunScanAndRegister_NonExistentRootExits2(t *testing.T) {
	code := runScanAndRegisterWithProbe("/path/that/does/not/exist/xyz", false, alwaysProbeOK)
	if code != 2 {
		t.Errorf("expected exit 2 for a missing root; got %d", code)
	}
}

func TestRunScanAndRegister_PermissionDeniedExits1(t *testing.T) {
	// Pin the exit-1 contract for non-ENOENT stat errors. An
	// unreadable directory (mode 0) triggers EACCES on stat; the
	// implementation must return 1, not 2 (the exit-2 lane is
	// reserved for missing / not-a-directory).
	//
	// Skip if running as root — root can stat anything regardless
	// of mode bits, so the test can't reproduce the failure.
	if os.Geteuid() == 0 {
		t.Skip("test cannot reproduce EACCES when running as root")
	}
	parent := t.TempDir()
	bad := filepath.Join(parent, "no-stat")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	// Drop read+execute on the parent so stat-ing the child fails.
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) }) // let TempDir clean up

	if code := runScanAndRegisterWithProbe(bad, false, alwaysProbeOK); code != 1 {
		t.Errorf("expected exit 1 for permission-denied stat; got %d", code)
	}
}

func TestInit_ScanRejectsIncompatibleFlags(t *testing.T) {
	// -scan only registers — combining it with per-repo flags is a
	// usage error, not silent. Each per-repo flag should be flagged
	// individually so the user sees what's wrong.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	scanRoot := t.TempDir()
	_ = mkBeads(t, scanRoot, "one")

	cases := [][]string{
		{"-scan", scanRoot, "-force"},
		{"-scan", scanRoot, "-chain"},
		{"-scan", scanRoot, "-skip-bd-init"},
		{"-scan", scanRoot, "-skip-register"},
	}
	for _, args := range cases {
		if code := runInit(args); code != 64 {
			t.Errorf("runInit(%v) = %d, want 64 (incompatible flags)", args, code)
		}
	}
}

func TestRunScanAndRegister_SkipsProbeFailures(t *testing.T) {
	// Two candidates: "good" probes clean, "bad" probes with an
	// error. After the scan, registry must contain only "good".
	// Pre-vo7 the scan registered everything with a .beads/ subdir,
	// including jsonl-only exports and abandoned shells that real
	// bd couldn't query — the bug that put domo-mcp into the
	// registry as a silent failure.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	goodPath := mkBeads(t, root, "good")
	badPath := mkBeads(t, root, "bad")

	probe := func(ctx context.Context, dir string) error {
		if dir == badPath {
			return errors.New("simulated bd failure")
		}
		return nil
	}
	if code := runScanAndRegisterWithProbe(root, false, probe); code != 0 {
		t.Fatalf("scan exit %d", code)
	}
	regPath := filepath.Join(cfgDir, "wyk", "repos.json")
	body, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(body), goodPath) {
		t.Errorf("registry missing good path %s\n%s", goodPath, body)
	}
	if strings.Contains(string(body), badPath) {
		t.Errorf("registry should NOT contain bad path %s (probe failed)\n%s", badPath, body)
	}
	if strings.Count(string(body), `"path"`) != 1 {
		t.Errorf("expected exactly 1 entry (good only); got:\n%s", body)
	}
}

func TestRunScanAndRegister_AllProbeFailuresExitsZero(t *testing.T) {
	// Every candidate failing the probe is not an error — the user
	// has just discovered their tree has no usable bd workspaces.
	// Exit 0, registry stays empty (or unwritten if no new ones to
	// add at all). Stderr should carry skip messages but stdout's
	// summary tells the same story.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	_ = mkBeads(t, root, "one")
	_ = mkBeads(t, root, "two")
	probe := func(ctx context.Context, dir string) error {
		return errors.New("nope")
	}
	if code := runScanAndRegisterWithProbe(root, false, probe); code != 0 {
		t.Errorf("all-probes-fail scan should exit 0; got %d", code)
	}
	// Registry not written (no entries added).
	regPath := filepath.Join(cfgDir, "wyk", "repos.json")
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registry should not be written when no candidates pass; err=%v", err)
	}
}

func TestScanForBeadsRepos_NestedRepoIsAlsoRecorded(t *testing.T) {
	// Documented behaviour: if a project contains a nested .beads
	// (test fixture, sample workspace, embedded example), the scan
	// records the nested path AS WELL AS the outer one. This is the
	// current behaviour, intentional today. The test exists so a
	// future change to prune-on-match doesn't silently regress.
	root := t.TempDir()
	outer := mkBeads(t, root, "outer")
	inner := mkBeads(t, root, "outer/fixtures/inner")

	got, err := scanForBeadsRepos(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	have := map[string]bool{}
	for _, p := range got {
		have[p] = true
	}
	if !have[outer] {
		t.Errorf("scan missed outer repo: %s; got %v", outer, got)
	}
	if !have[inner] {
		t.Errorf("scan missed nested inner repo: %s — current behaviour records both; got %v", inner, got)
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
