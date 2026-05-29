// Package registry tracks the set of bd workspaces wyk should show
// in its multi-repo TUI view. The registry is a single JSON file in
// the user's config directory (~/.config/wyk/repos.json by default,
// overridable via $XDG_CONFIG_HOME). `wyk init` writes to it; the
// TUI reads from it on startup.
//
// The format is intentionally minimal and forward-compatible: a
// version number and a list of entries. Each entry carries an
// absolute path (the source of truth) and a derived display name
// the TUI uses to label the Repo column.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentVersion is the JSON file's schema version. Future bumps
// would let Load migrate older files; today, any version other than
// this one is rejected with a clear error.
const CurrentVersion = 1

// Repo is one registered bd workspace.
type Repo struct {
	// Name is the short label the TUI shows in the Repo column.
	// Derived from filepath.Base(Path) at registration time so the
	// user can override it later by editing repos.json by hand.
	Name string `json:"name"`

	// Path is the absolute path to the repo root (the directory
	// containing .beads/, not the .beads directory itself).
	Path string `json:"path"`
}

// Registry is the in-memory view of repos.json.
type Registry struct {
	Version int    `json:"version"`
	Repos   []Repo `json:"repos"`
}

// DefaultPath returns the canonical location of repos.json. It
// respects $XDG_CONFIG_HOME if set, otherwise falls back to
// $HOME/.config/wyk/repos.json. The directory is NOT created here;
// callers that intend to write should ensure the parent exists.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "repos.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "repos.json"), nil
}

// Load reads the registry from path. If the file does not exist, an
// empty Registry at CurrentVersion is returned — first-time users
// don't need to ceremonially create the file before running wyk.
func Load(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{Version: CurrentVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Version != 0 && r.Version != CurrentVersion {
		return nil, fmt.Errorf("registry %s has unknown version %d (this wyk knows %d)",
			path, r.Version, CurrentVersion)
	}
	r.Version = CurrentVersion
	return &r, nil
}

// Save writes the registry to path, creating the parent directory if
// necessary. The write is atomic: marshalled bytes go to a temp file
// in the same directory, then `os.Rename` swaps it into place. A
// crash, signal, or concurrent second writer leaves either the old
// file or a complete new one — never a truncated half-write.
func (r *Registry) Save(path string) error {
	if r.Version == 0 {
		r.Version = CurrentVersion
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// trailing newline so the file plays nicely with text editors.
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, ".repos.*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}

// Add registers a repo at the given path. The path is normalised to
// an absolute, cleaned form before lookup, so adding "." then the
// absolute path doesn't create two entries. Add is idempotent — a
// second call with the same path returns nil and does not duplicate.
// The Name is derived from filepath.Base unless the entry already
// existed (in which case the previous Name is preserved).
func (r *Registry) Add(path string) error {
	abs, err := normalizePath(path)
	if err != nil {
		return err
	}
	for _, repo := range r.Repos {
		if repo.Path == abs {
			return nil // already present
		}
	}
	r.Repos = append(r.Repos, Repo{
		Name: filepath.Base(abs),
		Path: abs,
	})
	return nil
}

// Remove unregisters the repo at the given path. Returns true if an
// entry was removed, false if none matched. Path normalisation
// matches Add's so callers can pass relative or absolute paths.
func (r *Registry) Remove(path string) (bool, error) {
	abs, err := normalizePath(path)
	if err != nil {
		return false, err
	}
	for i, repo := range r.Repos {
		if repo.Path == abs {
			r.Repos = append(r.Repos[:i], r.Repos[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// RemoveByName unregisters the repo with the given display name.
// Returns true if an entry was removed, false if no entry matched.
// Used by `wyk registry remove <name>` so users can target an entry
// by the same label the TUI shows, without having to type the
// absolute path.
func (r *Registry) RemoveByName(name string) bool {
	for i, repo := range r.Repos {
		if repo.Name == name {
			r.Repos = append(r.Repos[:i], r.Repos[i+1:]...)
			return true
		}
	}
	return false
}

// Has reports whether path is registered.
func (r *Registry) Has(path string) bool {
	abs, err := normalizePath(path)
	if err != nil {
		return false
	}
	for _, repo := range r.Repos {
		if repo.Path == abs {
			return true
		}
	}
	return false
}

// normalizePath returns the absolute, symlink-resolved form of path
// so the registry's deduplication can compare strings directly.
// Symlinks are resolved (via EvalSymlinks) when the path exists —
// without this, macOS's /var → /private/var symlink and any user
// shortcut symlink would let the same repo register twice. If the
// path doesn't exist on disk yet, fall back to Abs+Clean.
func normalizePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("abs %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
