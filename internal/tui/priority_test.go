package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
)

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
