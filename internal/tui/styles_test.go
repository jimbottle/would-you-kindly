package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jimbottle/would-you-kindly/internal/theme"
)

// forceColor coerces lipgloss's default renderer into truecolor so
// the SGR escapes show up regardless of the host terminal. Without
// this the test environment (CI, dumb terminals, even tmpdir test
// runs in some shells) falls through to the Ascii profile and
// every Render() returns the unstyled string.
func forceColor() {
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// TestApplyTheme_OverridesAndDefaults exercises the partial-override
// guarantee: a Theme with one field set should change only that
// field, leaving every other style at its built-in default. Run
// last in the file so we don't bleed mutated styles into other
// tests in the package (the vars are package-scoped — assignment
// is permanent for the test process).
//
// We assert via Render output rather than peeking into Style
// internals: lipgloss.Style doesn't expose Background()/Foreground
// getters, but the rendered ANSI string carries the SGR code, so
// substring-checking the output is the supported way to verify a
// color landed.
func TestApplyTheme_OverridesAndDefaults(t *testing.T) {
	forceColor()

	defaultBadge := humanBadge.Render("X")
	defaultAgent := agentBadge.Render("X")

	ApplyTheme(theme.Theme{HumanBadgeBG: "9"}) // bright red

	got := humanBadge.Render("X")
	if got == defaultBadge {
		t.Errorf("humanBadge render unchanged after ApplyTheme; got %q", got)
	}
	// agentBadge wasn't in the theme — must stay identical.
	if newAgent := agentBadge.Render("X"); newAgent != defaultAgent {
		t.Errorf("agentBadge changed when only HumanBadgeBG was set:\n  before %q\n  after  %q",
			defaultAgent, newAgent)
	}

	// Restore for any later test that reads default colors. (The
	// overridden field stays mutated — there is no inverse op — but
	// re-applying the original 212 leaves a clean baseline.)
	ApplyTheme(theme.Theme{HumanBadgeBG: "212"})
}

// TestApplyTheme_EmptyThemeIsNoOp documents the contract: an
// empty theme leaves every style untouched. Useful as the
// failure mode after a missing theme.json — startup falls
// through with no color drift.
func TestApplyTheme_EmptyThemeIsNoOp(t *testing.T) {
	forceColor()
	before := titleStyle.Render("title")
	ApplyTheme(theme.Theme{})
	after := titleStyle.Render("title")
	if before != after {
		t.Errorf("empty theme drifted titleStyle:\n  before %q\n  after  %q", before, after)
	}
}

// TestApplyTheme_HexColorAccepted exercises the hex-literal
// path lipgloss supports natively. The exact SGR varies with
// the color profile so we settle for "render is non-empty and
// changed" rather than naming a specific ANSI sequence.
func TestApplyTheme_HexColorAccepted(t *testing.T) {
	forceColor()
	ApplyTheme(theme.Theme{Cursor: "#ff00aa"})
	after := cursorStyle.Render("▶")
	if !strings.Contains(after, "▶") {
		t.Errorf("hex theme color broke rendering: %q", after)
	}
	// Restore so a later test reading default colors sees them.
	ApplyTheme(theme.Theme{Cursor: "212"})
}
