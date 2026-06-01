package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// withTempRegistry plants a Registry at a per-test path and points
// XDG_CONFIG_HOME at the temp dir so loadRegistryForCmd resolves to
// it. Returns the registry-file path so tests can inspect after
// mutation. The original XDG_CONFIG_HOME is restored on cleanup.
func withTempRegistry(t *testing.T, repos []registry.Repo) string {
	t.Helper()
	tmp := t.TempDir()
	regDir := filepath.Join(tmp, "wyk")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(regDir, "repos.json")
	reg := &registry.Registry{Version: registry.CurrentVersion, Repos: repos}
	if err := reg.Save(regPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmp)
	return regPath
}

func TestRegistry_List_EmptyExitsZero(t *testing.T) {
	withTempRegistry(t, nil)
	if code := runRegistryList(nil); code != 0 {
		t.Errorf("list on empty registry should exit 0; got %d", code)
	}
}

func TestRegistry_Remove_Found(t *testing.T) {
	regPath := withTempRegistry(t, []registry.Repo{
		{Name: "foo", Path: "/x/foo"},
		{Name: "bar", Path: "/x/bar"},
	})
	if code := runRegistryRemove([]string{"foo"}); code != 0 {
		t.Fatalf("remove foo should exit 0; got %d", code)
	}
	// Verify on disk.
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Repos) != 1 || reg.Repos[0].Name != "bar" {
		t.Errorf("expected only 'bar' to remain; got %+v", reg.Repos)
	}
}

func TestRegistry_Remove_NotFoundExitsOne(t *testing.T) {
	withTempRegistry(t, []registry.Repo{{Name: "foo", Path: "/x/foo"}})
	if code := runRegistryRemove([]string{"nope"}); code != 1 {
		t.Errorf("remove of missing name should exit 1; got %d", code)
	}
}

func TestRegistry_Remove_RejectsZeroOrMultipleArgs(t *testing.T) {
	withTempRegistry(t, nil)
	if code := runRegistryRemove(nil); code != 64 {
		t.Errorf("zero args should exit 64; got %d", code)
	}
	if code := runRegistryRemove([]string{"a", "b"}); code != 64 {
		t.Errorf("two args should exit 64; got %d", code)
	}
}

func TestRegistry_Prune_RemovesMissingPaths(t *testing.T) {
	// One alive repo (real tempdir + .git), one dead (non-existent
	// path). Prune should drop only the dead one. -y skips the
	// interactive prompt.
	alive := t.TempDir()
	if err := os.Mkdir(filepath.Join(alive, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	regPath := withTempRegistry(t, []registry.Repo{
		{Name: "alive", Path: alive},
		{Name: "dead", Path: "/nope/does/not/exist"},
	})
	if code := runRegistryPrune([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("prune -y should exit 0; got %d", code)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Repos) != 1 || reg.Repos[0].Name != "alive" {
		t.Errorf("expected only 'alive' to remain; got %+v", reg.Repos)
	}
}

func TestRegistry_Prune_NoDeadEntriesExitsZeroNoOp(t *testing.T) {
	alive := t.TempDir()
	if err := os.Mkdir(filepath.Join(alive, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	withTempRegistry(t, []registry.Repo{{Name: "alive", Path: alive}})
	if code := runRegistryPrune([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Errorf("prune with no dead entries should exit 0; got %d", code)
	}
}

func TestRegistry_Prune_NConsentAborts(t *testing.T) {
	regPath := withTempRegistry(t, []registry.Repo{
		{Name: "dead", Path: "/nope/does/not/exist"},
	})
	// "n\n" answers the prompt with No — prune must NOT delete.
	if code := runRegistryPrune(nil, strings.NewReader("n\n")); code != 0 {
		t.Errorf("aborted prune should exit 0; got %d", code)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Repos) != 1 {
		t.Errorf("aborted prune should leave registry untouched; got %+v", reg.Repos)
	}
}

func TestReadYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false}, // empty line == no (matches [y/N] default)
		{"", false},   // EOF treated as no
		{"yeah\n", false},
	}
	for _, c := range cases {
		got, err := readYesNo(strings.NewReader(c.in))
		if err != nil {
			t.Errorf("readYesNo(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("readYesNo(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRegistry_Prune_DupNameKeepsAliveDropsDead(t *testing.T) {
	// Registry.Add derives Name from filepath.Base, so two repos
	// with the same basename at different paths share a Name. A
	// prune that removed by name would drop whichever entry came
	// first (probably the alive one). Pre-fix this was the
	// behaviour — exactly the inverse of what the user wants.
	// Regression: assert the *alive* entry survives, the *dead*
	// one is gone, and the path-based identity is what determined
	// which is which.
	alive := t.TempDir()
	if err := os.Mkdir(filepath.Join(alive, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// alive's Name will be filepath.Base(alive); reuse it for the dead entry too.
	sharedName := filepath.Base(alive)
	regPath := withTempRegistry(t, []registry.Repo{
		{Name: sharedName, Path: alive},
		{Name: sharedName, Path: "/nope/" + sharedName},
	})
	if code := runRegistryPrune([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("prune -y should exit 0; got %d", code)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Repos) != 1 {
		t.Fatalf("expected 1 entry after prune; got %d (%+v)", len(reg.Repos), reg.Repos)
	}
	// The survivor must be the alive one (real path), not whichever
	// shared the name first.
	resolvedAlive, _ := filepath.EvalSymlinks(alive)
	if reg.Repos[0].Path != resolvedAlive && reg.Repos[0].Path != alive {
		t.Errorf("prune removed the alive entry; survivor path=%q want %q", reg.Repos[0].Path, alive)
	}
}

func TestFindDeadEntries(t *testing.T) {
	alive := t.TempDir()
	if err := os.Mkdir(filepath.Join(alive, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitless := t.TempDir() // path exists, no .git
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "alive", Path: alive},
		{Name: "gitless", Path: gitless},
		{Name: "missing", Path: "/nope/does/not/exist"},
	}}
	dead := findDeadEntries(reg)
	if len(dead) != 2 {
		t.Fatalf("expected 2 dead entries (gitless+missing); got %d (%+v)", len(dead), dead)
	}
	names := map[string]string{}
	for _, d := range dead {
		names[d.Name] = d.reason
	}
	if names["gitless"] != ".git missing" {
		t.Errorf("gitless reason = %q, want %q", names["gitless"], ".git missing")
	}
	if names["missing"] != "path missing" {
		t.Errorf("missing reason = %q, want %q", names["missing"], "path missing")
	}
}

func TestFindBrokenWorkspaces_DropsOnlyDefinitiveNoWorkspace(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "good", Path: "/a"},
		{Name: "broken", Path: "/b"},
		{Name: "wrapped", Path: "/e"},
		{Name: "timeout", Path: "/c"},
		{Name: "skipped", Path: "/d"},
	}}
	probe := func(dir string) error {
		switch dir {
		case "/a":
			return nil // responds → keep
		case "/b":
			return beads.ErrNoWorkspace // definitive → drop
		case "/e":
			return fmt.Errorf("bd query: %w", beads.ErrNoWorkspace) // wrapped → drop
		case "/c":
			return errors.New("timed out after 5s") // transient → keep
		}
		return nil
	}
	skip := map[string]bool{"/d": true} // already filesystem-dead → not probed
	got := findBrokenWorkspaces(reg, skip, probe)

	gotPaths := map[string]bool{}
	for _, d := range got {
		gotPaths[d.Path] = true
		if d.reason != "no bd workspace" {
			t.Errorf("unexpected reason for %s: %q", d.Path, d.reason)
		}
	}
	if len(got) != 2 || !gotPaths["/b"] || !gotPaths["/e"] {
		t.Errorf("only definitive (incl. wrapped) ErrNoWorkspace entries should be flagged; got %+v", got)
	}
}

func TestRegistry_Prune_BrokenDropsNonWorkspaceKeepsHealthy(t *testing.T) {
	// Two repos, both with a real path + .git (so the filesystem prune
	// leaves them alone). The bd probe reports one as having no
	// workspace; `prune -broken` must drop only that one.
	// Resolve symlinks up front (macOS /tmp -> /private/tmp) so the
	// planted paths match what registry.Remove normalizes to, the way
	// `wyk init`'s Add already stores them in production.
	healthy := mustResolve(t, t.TempDir())
	broken := mustResolve(t, t.TempDir())
	for _, d := range []string{healthy, broken} {
		if err := os.Mkdir(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	regPath := withTempRegistry(t, []registry.Repo{
		{Name: "healthy", Path: healthy},
		{Name: "broken", Path: broken},
	})

	orig := defaultWorkspaceProbe
	defaultWorkspaceProbe = func(dir string) error {
		if dir == broken {
			return beads.ErrNoWorkspace
		}
		return nil
	}
	t.Cleanup(func() { defaultWorkspaceProbe = orig })

	if code := runRegistryPrune([]string{"-broken", "-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("prune -broken -y should exit 0; got %d", code)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Repos) != 1 || reg.Repos[0].Name != "healthy" {
		t.Errorf("expected only 'healthy' to remain; got %+v", reg.Repos)
	}
}

func TestRegistry_Prune_WithoutBrokenLeavesNonWorkspace(t *testing.T) {
	// Default prune (no -broken) must NOT probe bd or drop a
	// present-but-no-workspace entry — that's the opt-in behavior.
	broken := t.TempDir()
	if err := os.Mkdir(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	regPath := withTempRegistry(t, []registry.Repo{{Name: "broken", Path: broken}})

	probed := false
	orig := defaultWorkspaceProbe
	defaultWorkspaceProbe = func(string) error { probed = true; return beads.ErrNoWorkspace }
	t.Cleanup(func() { defaultWorkspaceProbe = orig })

	if code := runRegistryPrune([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("prune -y should exit 0; got %d", code)
	}
	if probed {
		t.Error("default prune must not probe bd")
	}
	reg, _ := registry.Load(regPath)
	if len(reg.Repos) != 1 {
		t.Errorf("default prune should leave the present-but-broken entry; got %+v", reg.Repos)
	}
}

func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
