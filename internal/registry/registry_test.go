package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_CorruptJSONNamesRemedy(t *testing.T) {
	// The parse error must name the remedy, not just the file: the
	// registry is re-creatable (wyk init re-registers repos), so the
	// message tells the user deleting it is safe.
	path := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: want parse error, got nil")
	}
	if !strings.Contains(err.Error(), "wyk init") {
		t.Errorf("parse error should name the remedy; got %q", err)
	}
}

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

func TestRepoForDir(t *testing.T) {
	root := t.TempDir()
	// Build a real on-disk tree so normalizePath (EvalSymlinks) resolves.
	parent := filepath.Join(root, "ebay-watchlist-watch")
	nested := filepath.Join(parent, "android")
	other := filepath.Join(root, "would-you-kindly")
	for _, d := range []string{parent, nested, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	subdir := filepath.Join(nested, "app", "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}

	reg := &Registry{Repos: []Repo{
		{Name: "ebay-watchlist-watch", Path: parent},
		{Name: "android", Path: nested},
		{Name: "would-you-kindly", Path: other},
	}}

	tests := []struct {
		name    string
		dir     string
		want    string // expected repo Name; "" means no match
		wantHit bool
	}{
		{"exact parent", parent, "ebay-watchlist-watch", true},
		{"exact other", other, "would-you-kindly", true},
		{"nested wins over parent", nested, "android", true},
		{"subdir of nested resolves to nested (longest prefix)", subdir, "android", true},
		{"unregistered ancestor of nothing", root, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := reg.RepoForDir(tc.dir)
			if ok != tc.wantHit {
				t.Fatalf("RepoForDir(%q) hit=%v, want %v (repo=%+v)", tc.dir, ok, tc.wantHit, got)
			}
			if ok && got.Name != tc.want {
				t.Fatalf("RepoForDir(%q) = %q, want %q", tc.dir, got.Name, tc.want)
			}
		})
	}
}

func TestRepoForDir_MissTreatedGracefully(t *testing.T) {
	reg := &Registry{Repos: []Repo{{Name: "x", Path: t.TempDir()}}}
	if _, ok := reg.RepoForDir(filepath.Join(t.TempDir(), "elsewhere")); ok {
		t.Fatal("RepoForDir should miss for an unrelated dir")
	}
}

func TestHasAndAdd_MatchHandEditedSymlinkedEntry(t *testing.T) {
	// would-you-kindly-9vlt: a hand-written repos.json entry that names
	// the workspace through a symlink must count as registered, or the
	// auto-register path adds the same workspace a second time.
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	r := &Registry{Version: 1, Repos: []Repo{{Name: "demo", Path: link}}}
	if !r.Has(real) {
		t.Fatal("Has(real path) should match the symlinked stored entry")
	}
	if !r.Has(link) {
		t.Fatal("Has(symlink path) should match too")
	}
	if err := r.Add(real); err != nil {
		t.Fatal(err)
	}
	if len(r.Repos) != 1 {
		t.Fatalf("Add(real) must not duplicate the symlinked entry; got %d entries", len(r.Repos))
	}
	if removed, _ := r.Remove(real); !removed || len(r.Repos) != 0 {
		t.Fatalf("Remove(real) should drop the symlinked entry; removed=%v left=%d", removed, len(r.Repos))
	}
}
