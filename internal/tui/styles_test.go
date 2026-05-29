package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/theme"
)

// forceColor coerces lipgloss's default renderer into truecolor so
// the SGR escapes show up regardless of the host terminal. Without
// this the test environment (CI, dumb terminals, even tmpdir test
// runs in some shells) falls through to the Ascii profile and
// every Render() returns the unstyled string.
//
// The default renderer is package-global; restoring the prior
// profile via t.Cleanup prevents bleed into other tests in this
// package (Go does not guarantee cross-file test ordering, so any
// later assertion that depends on the host profile must see the
// pre-test value).
func forceColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
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
	forceColor(t)

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
	forceColor(t)
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
func TestClosedRowStyle_DimsTheTitleBody(t *testing.T) {
	forceColor(t)
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "still going", Status: "open"},
		{ID: "a-2", Title: "wrapped up", Status: "closed"},
	}}
	m := applyFetched(New(src), src)
	m.width = 200 // wide enough for the title not to truncate
	openRow := m.renderRow(m.visible[0], false)
	closedRow := m.renderRow(m.visible[1], false)
	closedSGR := "\x1b[38;5;240m"
	// The closed-row dim must reach the title body — otherwise the
	// stated UX cue ("metadata stays bright, body dims") doesn't
	// hold. An earlier envelope-wrapping implementation passed a
	// "SGR appears somewhere" assertion but didn't actually dim
	// the title because inner column resets cleared the envelope.
	// Pin the *title* itself as the property that matters: every
	// run of title text in the closed row must be immediately
	// preceded (no other content between) by the dim SGR.
	idx := strings.Index(closedRow, "wrapped up")
	if idx < 0 {
		t.Fatalf("closed row missing the title; got:\n%q", closedRow)
	}
	// Walk backwards until we hit an ANSI escape. The most recent
	// SGR before the title text must be the dim color.
	preceding := closedRow[:idx]
	lastEsc := strings.LastIndex(preceding, "\x1b[")
	if lastEsc < 0 {
		t.Fatalf("no ANSI escape precedes the title; got:\n%q", closedRow)
	}
	// Slice from the escape to the end-of-SGR (`m`) and compare.
	endIdx := strings.Index(preceding[lastEsc:], "m")
	if endIdx < 0 {
		t.Fatalf("malformed SGR before title; got:\n%q", preceding[lastEsc:])
	}
	got := preceding[lastEsc : lastEsc+endIdx+1]
	if got != closedSGR {
		t.Errorf("title preceded by %q, want %q (dim should reach the title body)", got, closedSGR)
	}
	if strings.Contains(openRow, closedSGR) {
		t.Errorf("open row should NOT carry closedRowStyle; got:\n%q", openRow)
	}
}

func TestApplyTheme_HexColorAccepted(t *testing.T) {
	forceColor(t)
	ApplyTheme(theme.Theme{Cursor: "#ff00aa"})
	after := cursorStyle.Render("▶")
	if !strings.Contains(after, "▶") {
		t.Errorf("hex theme color broke rendering: %q", after)
	}
	// Restore so a later test reading default colors sees them.
	ApplyTheme(theme.Theme{Cursor: "212"})
}
