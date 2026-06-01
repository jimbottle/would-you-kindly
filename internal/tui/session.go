// Session state persists the TUI's in-flight workspace — the active
// filter preset, the chosen sort key, and the selected issue — across
// restarts. For a tool that lives in a terminal tab and gets
// re-opened constantly, restoring that state removes the friction of
// re-driving the keymap back to where you were on every launch.
//
// This is deliberately separate from internal/uiconfig (which holds
// durable, hand-editable preferences like column visibility). Session
// state is disposable "last-seen position" data, not configuration:
// the cursor ID in particular is meaningless to edit by hand and
// churns constantly. Keeping it in its own state.json file means a
// hand-edited ui.json never collides with the cursor write on every
// quit, and a future wyk that doesn't understand the file can simply
// overwrite it with no loss (unlike a forward-compatible prefs file).
//
// File location follows the same XDG-first pattern the rest of wyk's
// config uses (~/.config/wyk/state.json by default), mirroring
// internal/theme.DefaultPath and internal/uiconfig.DefaultPath.
package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// sessionVersion is the state.json schema version. Unlike uiconfig
// (which rejects unknown versions to avoid clobbering a forward-
// compatible prefs file), session state is disposable: a version we
// don't recognise is simply ignored on load and overwritten on the
// next quit, so there's no ErrUnsupportedVersion sentinel here.
const sessionVersion = 1

// SessionState is the on-disk shape of the last-session restore file.
// Every field is optional and zero-valued fields decode to "nothing
// to restore" — a partial or empty file is always safe to load.
type SessionState struct {
	Version int `json:"version"`
	// Preset is the active filter preset's string name (filter.Preset
	// is stringly-typed). Validated against filter.IsPreset on
	// hydration; an unknown name is ignored rather than coerced.
	Preset string `json:"preset,omitempty"`
	// Sort is the active sort key's label ("priority", "deps", …) as
	// produced by sortKey.label(). Persisting the label rather than
	// the raw enum int keeps the file stable if the enum is ever
	// reordered, and readable if a user peeks at it.
	Sort string `json:"sort,omitempty"`
	// SortDesc reverses the active sort's natural direction (Shift-S).
	// Only meaningful alongside a Sort axis — it's omitted when no
	// sort is active (sortNone has no direction), and an old state.json
	// without the field decodes to false, matching today's default.
	SortDesc bool `json:"sort_desc,omitempty"`
	// CursorID is the full bd ID of the selected row. Best-effort on
	// restore: if the ID isn't in the current visible set (closed,
	// filtered out, deleted) the cursor falls back to the top.
	CursorID string `json:"cursor_id,omitempty"`
}

// SessionDefaultPath returns the canonical state-file location,
// honoring XDG_CONFIG_HOME before falling back to ~/.config — the
// same idiom as theme.DefaultPath / uiconfig.DefaultPath so a single
// config tree holds every wyk file.
func SessionDefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "state.json"), nil
}

// LoadSession reads the session state from path. A missing file
// decodes to an empty SessionState at the current version with no
// error — first-run users have nothing to restore and that's not a
// failure. A malformed file returns the zero value AND a non-nil
// error: the caller (main) logs it and proceeds with defaults, so a
// corrupt state.json degrades to "no restore" rather than blocking
// launch. It is never fatal.
func LoadSession(path string) (SessionState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SessionState{Version: sessionVersion}, nil
	}
	if err != nil {
		return SessionState{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s SessionState
	if err := json.Unmarshal(b, &s); err != nil {
		return SessionState{}, fmt.Errorf("parse %s: %w", path, err)
	}
	// A version we don't recognise carries fields we can't trust;
	// ignore the restore (zero value) but don't error — the next
	// quit overwrites the file cleanly.
	if s.Version != sessionVersion {
		return SessionState{Version: sessionVersion}, nil
	}
	return s, nil
}

// SaveSession writes the state to path atomically (write-temp-then-
// rename) so a crash mid-write can't corrupt the file. The parent
// directory is created on demand for the first-time-save path.
// Mirrors uiconfig.Save's durability contract.
func SaveSession(path string, s SessionState) error {
	if s.Version == 0 {
		s.Version = sessionVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state.json.*")
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
