// Package filters persists named fuzzy-filter shortcuts. Each
// alias is a (name → query) pair the TUI looks up when the user
// types `@name` in the / prompt — typing `@auth` then enter
// applies the saved "auth" query without the user having to
// retype the full string.
//
// The file lives at ~/.config/wyk/filters.json (XDG-aware) and is
// intentionally hand-editable so a user can add aliases without a
// dedicated TUI save flow. wyk reads it once at startup; reload
// on save isn't necessary because the file is small enough that a
// future `wyk filters reload` (or restart) is the right escape
// hatch.
package filters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentVersion is the JSON file's schema version. Future bumps
// would let Load migrate older shapes; for now any other version
// is a clear error.
const CurrentVersion = 1

// ErrUnsupportedVersion is the sentinel for a future-schema file.
// Callers can distinguish "future file, leave alone" from
// "corrupt file, fall back to defaults".
var ErrUnsupportedVersion = errors.New("filters: unsupported file version")

// Aliases is the on-disk shape. The map preserves no ordering;
// the TUI sorts alphabetically when listing.
type Aliases struct {
	Version int               `json:"version"`
	Aliases map[string]string `json:"aliases"`
}

// DefaultPath returns the canonical config-file location. XDG
// first, then ~/.config/wyk/filters.json.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "filters.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "filters.json"), nil
}

// Load reads the filters file. A missing file returns an empty
// Aliases at CurrentVersion — first-run users don't need to
// create the file to use the TUI.
func Load(path string) (Aliases, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Aliases{Version: CurrentVersion, Aliases: map[string]string{}}, nil
	}
	if err != nil {
		return Aliases{}, fmt.Errorf("read %s: %w", path, err)
	}
	var a Aliases
	if err := json.Unmarshal(b, &a); err != nil {
		return Aliases{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if a.Version == 0 {
		a.Version = CurrentVersion
	} else if a.Version != CurrentVersion {
		return Aliases{}, fmt.Errorf("%w: %s declares version %d (this wyk understands version %d)", ErrUnsupportedVersion, path, a.Version, CurrentVersion)
	}
	if a.Aliases == nil {
		a.Aliases = map[string]string{}
	}
	return a, nil
}

// Lookup resolves an `@name` token to its stored query. The `@`
// prefix is included in `token` (the caller passes the raw input
// like `@blocked`). Returns the saved query and true on hit;
// empty string and false when there's no `@` prefix or no
// matching alias.
func (a Aliases) Lookup(token string) (string, bool) {
	if len(token) < 2 || token[0] != '@' {
		return "", false
	}
	q, ok := a.Aliases[token[1:]]
	return q, ok
}

// Save writes the on-disk Aliases atomically (write-temp-then-
// rename). The parent directory is created if needed so a first-
// time save doesn't require manual `mkdir`. Same shape as
// uiconfig.Save — keeps the per-user config files consistent.
func Save(path string, a Aliases) error {
	if a.Version == 0 {
		a.Version = CurrentVersion
	}
	if a.Aliases == nil {
		a.Aliases = map[string]string{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".filters.json.*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}
