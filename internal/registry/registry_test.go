package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsEmptyRegistry(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load on missing file should not error; got %v", err)
	}
	if r == nil || r.Version != CurrentVersion || len(r.Repos) != 0 {
		t.Errorf("expected empty registry at current version; got %+v", r)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.json")
	r := &Registry{Repos: []Repo{
		{Name: "a", Path: "/tmp/a"},
		{Name: "b", Path: "/tmp/b"},
	}}
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != CurrentVersion {
		t.Errorf("loaded version = %d, want %d", got.Version, CurrentVersion)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("loaded len = %d, want 2", len(got.Repos))
	}
	for i, want := range r.Repos {
		if got.Repos[i] != want {
			t.Errorf("repo %d: got %+v, want %+v", i, got.Repos[i], want)
		}
	}
}

func TestAdd_IsIdempotentAndDerivesName(t *testing.T) {
	r := &Registry{}
	tmp := t.TempDir() // some real absolute path
	if err := r.Add(tmp); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if len(r.Repos) != 1 {
		t.Fatalf("after first Add: len = %d, want 1", len(r.Repos))
	}
	if r.Repos[0].Name != filepath.Base(tmp) {
		t.Errorf("derived name = %q, want %q", r.Repos[0].Name, filepath.Base(tmp))
	}

	// Adding the same path again is a no-op.
	if err := r.Add(tmp); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if len(r.Repos) != 1 {
		t.Errorf("idempotent Add should not duplicate; len = %d", len(r.Repos))
	}
}

func TestAdd_NormalizesPath(t *testing.T) {
	r := &Registry{}
	dir := t.TempDir()
	if err := r.Add(dir); err != nil {
		t.Fatal(err)
	}
	// Adding the same path via "./" prefix and trailing slash should
	// be treated as the same entry.
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd) //nolint:errcheck
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("."); err != nil {
		t.Fatal(err)
	}
	if len(r.Repos) != 1 {
		t.Errorf("Add(.) inside the same dir should dedupe; got %d entries", len(r.Repos))
	}
}

func TestRemove(t *testing.T) {
	r := &Registry{}
	dir := t.TempDir()
	_ = r.Add(dir)

	removed, err := r.Remove(dir)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Error("Remove should report true when an entry was removed")
	}
	if len(r.Repos) != 0 {
		t.Errorf("after Remove: len = %d, want 0", len(r.Repos))
	}

	// Removing again should report false, not error.
	removed, err = r.Remove(dir)
	if err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if removed {
		t.Error("Remove of missing entry should report false")
	}
}

func TestLoad_RejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.json")
	bad, _ := json.Marshal(map[string]any{"version": 99, "repos": []any{}})
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for unknown version")
	}
}

func TestLoad_AcceptsMissingVersionAsV1(t *testing.T) {
	// Documented behaviour: a JSON object with no version field is
	// treated as v1 (older builds may have written that shape). The
	// load succeeds and the next Save normalises to CurrentVersion.
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.json")
	if err := os.WriteFile(path, []byte(`{"repos":[{"name":"x","path":"/tmp/x"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load of version-less file should succeed; got %v", err)
	}
	if r.Version != CurrentVersion {
		t.Errorf("expected normalised version %d; got %d", CurrentVersion, r.Version)
	}
	if len(r.Repos) != 1 || r.Repos[0].Name != "x" {
		t.Errorf("repos not preserved across version-less load; got %+v", r.Repos)
	}
}

func TestDefaultPath_RespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != "/custom/xdg/wyk/repos.json" {
		t.Errorf("DefaultPath() = %q, want /custom/xdg/wyk/repos.json", got)
	}
}
