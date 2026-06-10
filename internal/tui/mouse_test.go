package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// These tests largely resurrect the coverage removed in 58db776 when
// mouse capture was dropped for native text selection; the feature is
// back behind the `m` toggle (would-you-kindly-hckw), so the old
// behavioral contracts apply again.

func TestMouseWheel_ScrollsCursorUpAndDown(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.cursor != 0 {
		t.Fatalf("setup: cursor should start at 0; got %d", m.cursor)
	}

	// Wheel down → cursor++.
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 1 {
		t.Errorf("wheel down should advance cursor; got %d, want 1", m.cursor)
	}

	// Wheel up → cursor--.
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("wheel up should retreat cursor; got %d, want 0", m.cursor)
	}

	// Wheel up at row 0 is a no-op (clamps).
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("wheel up at row 0 should clamp; got %d", m.cursor)
	}
}

func TestMouseLeftClick_LandsCursorOnTargetRow(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// Click two cells below the first row → target row 2.
	rowY := m.rowsStartY() + 2
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: rowY,
	})
	m = model.(Model)
	if m.cursor != 2 {
		t.Errorf("left-click at row offset 2 should land cursor on row 2; got %d", m.cursor)
	}
}

func TestMouseLeftClick_OutsideTableIsNoOp(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// Click on Y=0 (the title line). Should NOT change cursor.
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 0,
	})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("click in header area should be a no-op; cursor moved to %d", m.cursor)
	}

	// Click far below the last row → clamped (target out of range, no-op).
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 999,
	})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("click past end of list should be a no-op; cursor moved to %d", m.cursor)
	}
}

func TestMouseLeftClick_OnMoreBelowHintIsNoOp(t *testing.T) {
	// When the row window is smaller than len(visible), viewList
	// renders a "↓ N more below" hint line just past the last row.
	// Clicking that hint used to map to the next out-of-window row
	// (target = scroll + rowY, which is a valid index whenever the
	// view is partially scrolled), producing a surprising downward
	// cursor jump. The clamp treats such clicks as no-ops.
	src := &stubSource{issues: manyIssues(50)}
	m := New(src)
	// Constrain height so bodyHeight is small and the hint line
	// actually renders.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.bodyHeight() >= len(m.visible) {
		t.Fatalf("test premise: bodyHeight (%d) should be < visible (%d) so a hint line renders", m.bodyHeight(), len(m.visible))
	}

	preCursor := m.cursor
	// Click one cell past the body — the "↓ N more below" line.
	hintY := m.rowsStartY() + m.bodyHeight()
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: hintY,
	})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("click on more-below hint should be a no-op; cursor moved %d → %d", preCursor, m.cursor)
	}
}

func TestMouse_IgnoredOutsideListMode(t *testing.T) {
	// Help / modal / prompt modes own the canvas; mouse should
	// not steal focus and reset the list cursor.
	src := &stubSource{issues: manyIssues(20)}
	m := applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	if m.mode != modeHelp {
		t.Fatalf("setup: expected modeHelp; got %v", m.mode)
	}
	preCursor := m.cursor
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("mouse should be ignored in modeHelp; cursor changed from %d to %d", preCursor, m.cursor)
	}
}

func TestDetailView_MouseWheelScrollsViewport(t *testing.T) {
	// modeDetail wires mouse wheel events to detailVP so a long
	// description doesn't force the user to reach for the keyboard.
	// The cursor in modeList must NOT move on the wheel event — it's
	// owned by the viewport while the detail view is open.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("enter should open modeDetail; got %v", m.mode)
	}
	preCursor := m.cursor

	// Routing contract only: forward to the viewport without
	// touching the list cursor (a short body may not actually
	// change YOffset).
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("mouse wheel in detail view must not move the list cursor; was %d, now %d", preCursor, m.cursor)
	}
}

func TestRowsStartY_AccountsForChipStrip(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := applyFetched(New(src), src)
	baseY := m.rowsStartY()

	// Activate a priority cap → chip strip appears, rowsStartY
	// shifts down by one.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = model.(Model)
	if m.rowsStartY() != baseY+1 {
		t.Errorf("chip strip should bump rowsStartY by 1; was %d, now %d", baseY, m.rowsStartY())
	}
}

func TestToggleMouse_FlipsAndAnnounces(t *testing.T) {
	// `m` flips capture and tells the user what changed and how to
	// get back — the status line is the only discoverability the
	// shift/option-click escape hatch has.
	src := &stubSource{issues: manyIssues(3)}
	m := applyFetched(New(src), src)
	if m.mouseOff {
		t.Fatal("mouse capture should default ON")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if !m.mouseOff {
		t.Error("first m should turn capture OFF")
	}
	if !strings.Contains(m.status, "off") {
		t.Errorf("status should announce capture off; got %q", m.status)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if m.mouseOff {
		t.Error("second m should turn capture back ON")
	}
	if !strings.Contains(m.status, "on") {
		t.Errorf("status should announce capture on; got %q", m.status)
	}
}

func TestMousePreference_PersistsAcrossSessions(t *testing.T) {
	// The toggle survives a quit/relaunch via state.json: persisted
	// in the negative (mouse_off) so files written before the field
	// existed keep the capture-on default.
	path := filepath.Join(t.TempDir(), "state.json")
	src := &stubSource{issues: manyIssues(3)}
	m := applyFetched(New(src), src).WithSession(SessionState{}, path)
	if m.mouseOff {
		t.Fatal("empty session state should leave mouse capture ON")
	}

	// Toggle off, then quit — persistSession runs on the quit path.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	m.persistSession()

	st, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !st.MouseOff {
		t.Error("persisted state should carry mouse_off=true")
	}

	// Restore into a fresh model → capture stays off.
	m2 := New(src).WithSession(st, path)
	if !m2.mouseOff {
		t.Error("restored session should keep mouse capture OFF")
	}
}
