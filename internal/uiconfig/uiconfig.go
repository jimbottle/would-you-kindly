// Package uiconfig persists per-user TUI preferences that survive
// across `wyk` invocations. Today that's the column-visibility
// state (`o` toggle); the file is intentionally small and JSON so
// users can edit it by hand. Future preferences (default sort,
// flagged window width, etc.) can extend the Config struct without
// a schema bump as long as new fields are optional and zero-valued
// fields decode to a sane default.
//
// File location follows the same XDG-first pattern the registry
// package uses (~/.config/wyk/ui.json by default).
package uiconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentVersion is the JSON file's schema version. Any other
// version is rejected with a clear error rather than silently
// merged — a forward-incompat field name could otherwise corrupt
// a future wyk's preferences.
const CurrentVersion = 1

// Config is the on-disk shape. HiddenColumns is the list of column
// IDs the user has turned off via the `o` overlay. Stored as a
// list-of-strings (rather than a map[string]bool) so a hand-edited
// file reads cleanly and adding a new column simply leaves it on
// by default (absence == visible).
type Config struct {
	Version       int      `json:"version"`
	HiddenColumns []string `json:"hidden_columns,omitempty"`
}

// DefaultPath returns the canonical config-file location, honoring
// XDG_CONFIG_HOME before falling back to ~/.config.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "ui.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "ui.json"), nil
}

// Load reads the config from path. A missing file decodes to an
// empty Config at CurrentVersion — first-run users don't need to
// create the file before launching wyk.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: CurrentVersion}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Version == 0 {
		// Pre-v1 file written before the field existed. Treat as v1.
		c.Version = CurrentVersion
	} else if c.Version != CurrentVersion {
		return Config{}, fmt.Errorf("%s: unsupported version %d (this wyk understands version %d)", path, c.Version, CurrentVersion)
	}
	return c, nil
}

// Save writes the config to path atomically (write-temp-then-
// rename) so a crash mid-write can't corrupt the file. The parent
// directory is created on demand for the first-time-save path.
func Save(path string, c Config) error {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ui.json.*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
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

// HiddenSet turns the persisted slice into a lookup map for the
// per-paint visibility check. Keeps callers from re-allocating a
// map on every render.
func (c Config) HiddenSet() map[string]bool {
	out := make(map[string]bool, len(c.HiddenColumns))
	for _, id := range c.HiddenColumns {
		out[id] = true
	}
	return out
}
