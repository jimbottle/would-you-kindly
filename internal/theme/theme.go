// Package theme loads optional user-supplied color overrides for the
// TUI. The defaults live inside internal/tui/styles.go as
// hardcoded lipgloss.Color values; a theme.json file (located at
// $XDG_CONFIG_HOME/wyk/theme.json by default) lets a user
// override any subset of those colors without rebuilding wyk.
//
// Design choices:
//
//   - Empty / missing keys keep the built-in default. The Theme
//     struct uses zero-value strings so the unmarshaller naturally
//     produces an empty-keys-fall-through Theme for any subset
//     written by the user.
//   - The file is read once at startup; live reloading is out of
//     scope. The TUI's chrome is recomputed every paint anyway,
//     but lipgloss styles are package-level vars wired into render
//     code, and refactoring every reference to a "current theme"
//     pointer for the sake of in-session reloading would be a much
//     larger change than the brief asks for.
//   - Color strings are passed verbatim to lipgloss.Color, which
//     accepts ANSI 256 codes ("212") and hex literals ("#ff66cc").
//     We do not validate them here; lipgloss prints invalid colors
//     as the terminal default, which is the same failure mode as
//     a missing key.
package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Theme carries the optional color overrides. Field names map 1:1
// to the lipgloss.Style vars in internal/tui/styles.go; the
// json tags pick the user-visible config keys.
type Theme struct {
	Title            string `json:"title,omitempty"`
	StatusOpen       string `json:"status_open,omitempty"`
	StatusInProgress string `json:"status_in_progress,omitempty"`
	StatusBlocked    string `json:"status_blocked,omitempty"`
	StatusClosed     string `json:"status_closed,omitempty"`
	StatusOther      string `json:"status_other,omitempty"`
	HumanBadgeBG     string `json:"human_badge_bg,omitempty"`
	HumanBadgeFG     string `json:"human_badge_fg,omitempty"`
	AgentBadgeBG     string `json:"agent_badge_bg,omitempty"`
	AgentBadgeFG     string `json:"agent_badge_fg,omitempty"`
	HumanBlockBG     string `json:"human_block_bg,omitempty"`
	HumanBlockFG     string `json:"human_block_fg,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	StatusBarBG      string `json:"status_bar_bg,omitempty"`
	StatusBarFG      string `json:"status_bar_fg,omitempty"`
	Error            string `json:"error,omitempty"`
	Empty            string `json:"empty,omitempty"`
	DetailHeader     string `json:"detail_header,omitempty"`
	DetailLabel      string `json:"detail_label,omitempty"`
	Help             string `json:"help,omitempty"`
	Confirm          string `json:"confirm,omitempty"`
	StatusBanner     string `json:"status_banner,omitempty"`
	SetupHint        string `json:"setup_hint,omitempty"`
	WykIndicator     string `json:"wyk_indicator,omitempty"`
	ChipActiveBG     string `json:"chip_active_bg,omitempty"`
	ChipActiveFG     string `json:"chip_active_fg,omitempty"`
	FetchError       string `json:"fetch_error,omitempty"`
	FuzzyMatch       string `json:"fuzzy_match,omitempty"`
}

// DefaultPath returns the canonical theme-file location, honoring
// $XDG_CONFIG_HOME before falling back to ~/.config — mirrors
// internal/uiconfig.DefaultPath so a single config tree holds
// every wyk preference file regardless of OS.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "wyk", "theme.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "wyk", "theme.json"), nil
}

// LoadFile reads a theme.json from an explicit path. Used by
// tests; production wiring goes through LoadDefault.
func LoadFile(path string) (Theme, error) {
	b, err := os.ReadFile(path) //nolint:gosec // user-provided config path is intentional
	if err != nil {
		return Theme{}, err
	}
	var t Theme
	if err := json.Unmarshal(b, &t); err != nil {
		return Theme{}, err
	}
	return t, nil
}

// LoadDefault reads from DefaultPath when the file exists. A
// missing file returns an empty Theme with no error — the
// expected "no overrides; use built-in colors" path. Other
// failures (malformed JSON, permission denied) surface as
// non-nil errors so startup can log them; the caller decides
// whether to fail loudly or fall back silently.
func LoadDefault() (Theme, error) {
	path, err := DefaultPath()
	if err != nil {
		// Surface this rather than swallow it: the doc comment
		// promises non-missing-file failures land in the caller's
		// log, and an unresolvable home dir is exactly that.
		return Theme{}, err
	}
	t, err := LoadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Theme{}, nil
	}
	return t, err
}
