package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// mkWorkspace creates a directory tree with a .beads/ dir at its root and
// returns the (symlink-resolved) root — the same normalisation
// registry.Add applies, so tests can compare paths directly. On macOS
// t.TempDir() lives under /var, a symlink to /private/var, and comparing
// the un-resolved path would spuriously fail.
func mkWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// readRegistry loads the registry from the ambient $XDG_CONFIG_HOME.
func readRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	path, err := registry.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestFindBeadsRoot(t *testing.T) {
	root := mkWorkspace(t)
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("at the root", func(t *testing.T) {
		got, ok := findBeadsRoot(root)
		if !ok || got != root {
			t.Fatalf("got (%q, %v), want (%q, true)", got, ok, root)
		}
	})

	// The whole point of the upward walk: an agent runs `wyk create` from
	// wherever it happens to be, not from the workspace root.
	t.Run("from a subdirectory", func(t *testing.T) {
		got, ok := findBeadsRoot(nested)
		if !ok || got != root {
			t.Fatalf("got (%q, %v), want (%q, true)", got, ok, root)
		}
	})

	t.Run("empty dir means cwd", func(t *testing.T) {
		t.Chdir(nested)
		got, ok := findBeadsRoot("")
		if !ok || got != root {
			t.Fatalf("got (%q, %v), want (%q, true)", got, ok, root)
		}
	})

	t.Run("no workspace anywhere above", func(t *testing.T) {
		// A bare temp dir: nothing from here to / holds a .beads.
		if got, ok := findBeadsRoot(t.TempDir()); ok {
			t.Fatalf("got (%q, true), want no match", got)
		}
	})

	// A FILE named .beads is not a workspace; only a directory is. Without
	// the IsDir check the walk would stop early and register a non-workspace.
	t.Run("a .beads file is not a workspace", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".beads"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok := findBeadsRoot(dir); ok {
			t.Fatalf("got (%q, true), want no match", got)
		}
	})
}

func TestRealMaybeAutoRegister_RegistersUnregisteredWorkspace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)

	var buf bytes.Buffer
	realMaybeAutoRegister("wyk create", root, &buf)

	reg := readRegistry(t)
	if !reg.Has(root) {
		t.Fatalf("workspace not registered; registry = %+v", reg.Repos)
	}
	// The notice is the entire safeguard against this being a silent
	// mutation — assert it names the path AND says why it mattered.
	out := buf.String()
	if !strings.Contains(out, root) {
		t.Errorf("notice does not name the registered path: %q", out)
	}
	if !strings.Contains(out, "wyk inbox") {
		t.Errorf("notice does not explain the consequence: %q", out)
	}
}

func TestRealMaybeAutoRegister_AlreadyRegisteredIsSilent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)

	var first bytes.Buffer
	realMaybeAutoRegister("wyk create", root, &first)

	// Second call: idempotent, and — just as important — quiet. A notice on
	// every write would train users to ignore it.
	var second bytes.Buffer
	realMaybeAutoRegister("wyk create", root, &second)
	if second.Len() != 0 {
		t.Errorf("re-registering printed %q, want silence", second.String())
	}
	if reg := readRegistry(t); len(reg.Repos) != 1 {
		t.Errorf("registry has %d entries, want 1: %+v", len(reg.Repos), reg.Repos)
	}
}

func TestRealMaybeAutoRegister_NonWorkspaceIsNoOp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var buf bytes.Buffer
	realMaybeAutoRegister("wyk create", t.TempDir(), &buf)

	if buf.Len() != 0 {
		t.Errorf("non-workspace printed %q, want silence", buf.String())
	}
	if reg := readRegistry(t); len(reg.Repos) != 0 {
		t.Errorf("non-workspace registered %+v", reg.Repos)
	}
}

// A registry that can't be written must not be fatal: the create/handoff
// it follows has already succeeded, and turning a bookkeeping failure into
// a command failure would be worse than the bookkeeping failure.
func TestRealMaybeAutoRegister_SaveFailureWarnsAndReturns(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// Make the wyk config dir un-creatable by planting a FILE where the
	// directory needs to be, so Save's MkdirAll fails.
	if err := os.WriteFile(filepath.Join(cfg, "wyk"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := mkWorkspace(t)

	var buf bytes.Buffer
	realMaybeAutoRegister("wyk create", root, &buf)

	if !strings.Contains(buf.String(), "could not") {
		t.Errorf("expected a warning about the failed registry write; got %q", buf.String())
	}
}

func TestRunCreate_AutoRegisters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)
	t.Chdir(root)

	withStubCreate(t, "abc-123", nil)

	if code := runCreate([]string{"--title", "x"}); code != 0 {
		t.Fatalf("runCreate exit %d, want 0", code)
	}
	if reg := readRegistry(t); !reg.Has(root) {
		t.Fatalf("runCreate did not register %s; registry = %+v", root, reg.Repos)
	}
}

// The bug this fixes lost a P0 that was filed correctly: registration must
// happen even when the bd write behind it fails, so the next attempt (and
// every other view) can see the workspace.
func TestRunCreate_AutoRegistersEvenWhenBDFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := mkWorkspace(t)
	t.Chdir(root)

	withStubCreate(t, "", errors.New("bd exploded"))

	if code := runCreate([]string{"--title", "x"}); code != 1 {
		t.Fatalf("runCreate exit %d, want 1", code)
	}
	if reg := readRegistry(t); !reg.Has(root) {
		t.Fatalf("failed create skipped registration; registry = %+v", reg.Repos)
	}
}

func TestReposToQuery_DashCRegisters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	root := mkWorkspace(t)

	if _, err := reposToQuery(root, "", false); err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if reg := readRegistry(t); !reg.Has(root) {
		t.Fatalf("-C did not register %s; registry = %+v", root, reg.Repos)
	}
}
