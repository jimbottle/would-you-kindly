package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
)

// TestRenderRow_PriorityEmphasisPrecedence pins the precedence the
// commit relies on: emphasis applies only when on, and a closed row's
// dim wins over it. Asserts against the actual styles' rendered output
// (not raw SGR literals) so it's independent of the test environment's
// colour profile / background.
func TestRenderRow_PriorityEmphasisPrecedence(t *testing.T) {
	forceColor(t)
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "open p0", Status: "open", Priority: 0},
		{ID: "a-2", Title: "closed p0", Status: "closed", Priority: 0},
	}}
	m := applyFetched(New(src), src)
	m.width = 200

	wantLoud := priorityStyleFor(0).Render("P0") // emphasis: danger + bold
	wantDim := closedRowStyle.Render("P0")       // closed-row dim

	// Emphasis off: an open P0 is the flat default, never the loud style.
	m.priorityEmphasis = false
	off := m.renderRow(m.visible[0], false)
	if strings.Contains(off, wantLoud) {
		t.Error("emphasis off: open P0 should not carry the loud priority style")
	}

	// Emphasis on: the open P0 gets the loud style; the row changes.
	m.priorityEmphasis = true
	on := m.renderRow(m.visible[0], false)
	if !strings.Contains(on, wantLoud) {
		t.Errorf("emphasis on: open P0 should carry the loud priority style\nrow: %q", on)
	}
	if off == on {
		t.Error("toggling emphasis should change the open P0 row")
	}

	// Closed P0 with emphasis on: closed-row dim wins over emphasis.
	closed := m.renderRow(m.visible[1], false)
	if !strings.Contains(closed, wantDim) {
		t.Errorf("closed P0 should render dim; row: %q", closed)
	}
	if strings.Contains(closed, wantLoud) {
		t.Error("closed P0 must not carry the loud emphasis — dim takes precedence")
	}
}

// TestPriorityStyleFor checks the opt-in emphasis scale directly (no
// rendering, so it's independent of the test environment's color
// profile): P0 loud+bold, P3/P4 dim, P2 neutral, each foreground
// distinct from the neutral default.
func TestPriorityStyleFor(t *testing.T) {
	if !priorityStyleFor(0).GetBold() {
		t.Error("P0 should be bold (loud)")
	}
	if priorityStyleFor(2).GetBold() {
		t.Error("P2 should be neutral, not bold")
	}
	// P0 and P3 carry a foreground; P2 does not — so urgent pops and
	// backlog recedes while the mid case stays flat.
	if priorityStyleFor(0).GetForeground() == priorityStyleFor(2).GetForeground() {
		t.Error("P0 and P2 should differ in foreground")
	}
	if priorityStyleFor(3).GetForeground() == priorityStyleFor(2).GetForeground() {
		t.Error("P3 and P2 should differ in foreground")
	}
	if priorityStyleFor(0).GetForeground() == priorityStyleFor(1).GetForeground() {
		t.Error("P0 (danger) and P1 (warn) should differ in foreground")
	}
}

// TestPriorityEmphasis_OverlayTogglePersists drives the `o` overlay:
// `p` flips the emphasis, and closing the overlay writes it to ui.json
// so it survives a restart — the same persistence path the column
// toggles use.
func TestPriorityEmphasis_OverlayTogglePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")
	src := &stubSource{issues: manyIssues(3)}
	m := applyFetched(New(src).WithHiddenColumns(nil, path), src)
	if m.priorityEmphasis {
		t.Fatal("priority emphasis should default off")
	}

	m.mode = modeColumns
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = model.(Model)
	if !m.priorityEmphasis {
		t.Fatal("`p` in the overlay should toggle emphasis on")
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("esc should close the overlay; mode = %v", m.mode)
	}

	cfg, err := uiconfig.Load(path)
	if err != nil {
		t.Fatalf("Load after toggle: %v", err)
	}
	if !cfg.PriorityEmphasis {
		t.Error("priority emphasis should have persisted to ui.json")
	}
}
