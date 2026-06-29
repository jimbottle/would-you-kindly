// Package wykconfig persists machine-wide wyk behavior settings that
// survive across invocations — things that change what a command DOES,
// as opposed to internal/uiconfig (TUI display prefs) or
// internal/registry (the workspace list). Today that's a single key,
// default_scope, governing whether the multi-repo commands (inbox,
// stats, activity, dashboard, depgraph, export) default to querying
// every registered repo or just the one containing the cwd.
//
// The file is intentionally small JSON so users can edit it by hand;
// `wyk config get/set` is the supported front door. New settings slot
// in as additional optional, zero-valued-friendly fields without a
// schema bump — the same forward-compatibility contract uiconfig uses.
//
// File location follows the same XDG-first pattern the registry and
// uiconfig packages use (~/.config/wyk/config.json by default).
package wykconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentVersion is the JSON file's schema version. Any other version
// is rejected with ErrUnsupportedVersion rather than silently merged —
// a forward-incompatible field could otherwise corrupt a future wyk's
// settings. Mirrors uiconfig.CurrentVersion.
const CurrentVersion = 1

// Scope values for the default_scope setting.
const (
	// ScopeAll queries every registered repo (the built-in default —
	// preserves the pre-config behavior so existing users see no change).
	ScopeAll = "all"
	// ScopeCwd scopes the multi-repo commands to the repo containing the
	// current working directory.
	ScopeCwd = "cwd"
)

// Color values for the color setting. The built-in default is "auto"
// (color on, the pre-config behavior); "never" disables color the same
// way NO_COLOR / --no-color do. ColorAuto and "" are equivalent.
const (
	ColorAuto  = "auto"
	ColorNever = "never"
)

// ErrUnsupportedVersion is returned when the on-disk file declares a
// schema version this binary doesn't understand. A distinct sentinel
// so callers can keep persistence DISABLED rather than overwriting a
// forward-compatible file. Mirrors uiconfig.ErrUnsupportedVersion.
var ErrUnsupportedVersion = errors.New("wykconfig: unsupported file version")

// ErrInvalidScope wraps every ValidateScope rejection so callers can
// distinguish "the user supplied a bad scope value" (a usage error) from
// a parse / I/O failure, and map it to the usage exit code. Its text
// omits a package prefix because it surfaces directly in CLI messages
// (e.g. "wyk config set: invalid scope …").
var ErrInvalidScope = errors.New("invalid scope")

// ErrInvalidValue wraps a rejected value for any non-scope setting (e.g.
// color), so callers map it to the usage exit code the same way they do
// ErrInvalidScope. Package-prefix-free for the same reason.
var ErrInvalidValue = errors.New("invalid value")

// Config is the on-disk shape. Every field is optional and zero-valued
// to the pre-config behavior, so a missing key (or a missing file) leaves
// wyk behaving exactly as it did before the setting existed. Future keys
// follow the same rule: add an optional field whose zero value is the
// current default.
type Config struct {
	Version int `json:"version"`
	// DefaultScope: "" (unset → "all"), "all", or "cwd".
	DefaultScope string `json:"default_scope,omitempty"`
	// DisableUpdateCheck skips the TUI's background release check and the
	// update nudge. Default false (checks run).
	DisableUpdateCheck bool `json:"disable_update_check,omitempty"`
	// CompactJSON / SlimJSON set the default for the per-command -compact
	// / -slim JSON flags. Default false (indented JSON, full bodies); the
	// per-run flags still override (e.g. -compact=false).
	CompactJSON bool `json:"compact_json,omitempty"`
	SlimJSON    bool `json:"slim_json,omitempty"`
	// Color: "" (unset → "auto"), "auto", or "never". NO_COLOR /
	// WYK_NO_COLOR / --no-color still force color off regardless.
	Color string `json:"color,omitempty"`
}

// DefaultPath returns the canonical config-file location, honoring
// XDG_CONFIG_HOME before falling back to ~/.config.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "config.json"), nil
}

// Load reads the config from path. A missing file decodes to an empty
// Config at CurrentVersion — first-run users don't need to create the
// file before running wyk. A version-0 file (written before the field
// existed) is treated as v1; any other version is ErrUnsupportedVersion.
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
		c.Version = CurrentVersion
	} else if c.Version != CurrentVersion {
		return Config{}, fmt.Errorf("%w: %s declares version %d (this wyk understands version %d)", ErrUnsupportedVersion, path, c.Version, CurrentVersion)
	}
	return c, nil
}

// Save writes the config to path atomically (write-temp-then-rename)
// so a crash mid-write can't corrupt the file. The parent directory is
// created on demand for the first-time-save path.
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.json.*")
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

// ValidateScope accepts the empty string (unset), "all", or "cwd".
// Any other value is an error — a set-but-invalid scope is a hard
// failure rather than a silent fallthrough, so a typo can't quietly
// change which repos a command queries.
func ValidateScope(s string) error {
	switch s {
	case "", ScopeAll, ScopeCwd:
		return nil
	default:
		return fmt.Errorf("%w %q (valid: %s, %s)", ErrInvalidScope, s, ScopeAll, ScopeCwd)
	}
}

// ValidateColor accepts the empty string (unset → auto), "auto", or
// "never". Any other value wraps ErrInvalidValue so the CLI maps it to a
// usage error.
func ValidateColor(s string) error {
	switch s {
	case "", ColorAuto, ColorNever:
		return nil
	default:
		return fmt.Errorf("%w %q for color (valid: %s, %s)", ErrInvalidValue, s, ColorAuto, ColorNever)
	}
}
