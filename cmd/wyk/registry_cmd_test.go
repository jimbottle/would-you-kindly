package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
