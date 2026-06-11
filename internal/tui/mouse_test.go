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

// Init no longer requests mouse capture: the launch-time state rides
// main's tea.WithMouseCellMotion option (pinned by
// TestStartWithMouseCapture) and runtime switching goes through the
// MouseController (pinned by the TestMouseCapture_* suite) — cmds
// were dropped entirely after the batched-mouse-cmd delivery proved
// lossy live (PR #24, would-you-kindly-5i0e).

func TestRowsStartY_OverwideChromeStaysOneRow(t *testing.T) {
	// bubbletea's standard renderer TRUNCATES lines wider than the
	// window — it never lets the terminal soft-wrap them — so an
	// over-wide setup hint occupies exactly one screen row and must
	// count as exactly one in the click hit-test. Counting soft wrap
	// here overcounted and skewed clicks downward (roborev #2035).
	src := &stubSource{issues: manyIssues(5)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = model.(Model)
	m = applyFetched(m, src)
	base := m.rowsStartY()

	// A 100-visual-column single-line hint on a 40-column pane is
	// truncated to one row by the renderer.
	m.setupHint = strings.Repeat("x", 100)
	if got := m.rowsStartY() - base; got != 1 {
		t.Errorf("over-wide single-line hint must add exactly 1 row (renderer truncates); added %d", got)
	}

	// Explicit newlines DO consume rows.
	m.setupHint = "line one\nline two"
	if got := m.rowsStartY() - base; got != 2 {
		t.Errorf("two-line hint must add 2 rows; added %d", got)
	}
}

func TestToggleMouse_ReachableFromDetailAndOutput(t *testing.T) {
	// The toggle matters most while reading a long runbook or `:bd`
	// output — exactly when the user wants to click-drag-copy text —
	// so `m` must work there, not only in the list (roborev #2034).
	src := &stubSource{issues: manyIssues(3)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// Detail view.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: expected modeDetail; got %v", m.mode)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if !m.mouseOff {
		t.Error("m in detail view should toggle mouse capture off")
	}
	if m.mode != modeDetail {
		t.Errorf("toggle must not leave the detail view; mode is %v", m.mode)
	}

	// Output overlay.
	m.mode = modeOutput
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if m.mouseOff {
		t.Error("m in the output overlay should toggle capture back on")
	}
	if m.mode != modeOutput {
		t.Errorf("toggle must not close the output overlay; mode is %v", m.mode)
	}

	// Help overlay — the screen that advertises the binding.
	m.mode = modeHelp
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if !m.mouseOff {
		t.Error("m in the help overlay should toggle capture off")
	}
	if m.mode != modeHelp {
		t.Errorf("toggle must not close the help overlay; mode is %v", m.mode)
	}
}

// recordingMouse is a fake MouseController logging the switch calls
// the Update wrapper makes, so tests assert the actual terminal-state
// transitions without executing tea cmds.
type recordingMouse struct{ calls []string }

func (r *recordingMouse) EnableMouseCellMotion() { r.calls = append(r.calls, "enable") }
func (r *recordingMouse) DisableMouse()          { r.calls = append(r.calls, "disable") }

func TestMouseCapture_FollowsTheView(t *testing.T) {
	// The detail view is a reading surface: entering it must release
	// the mouse so click-drag selects runbook text bare, and leaving
	// must re-capture for list navigation (would-you-kindly-5i0e).
	// Derived centrally — the Update wrapper switches on any boundary
	// crossing, whatever caused it — and delivered as direct
	// controller calls, never cmds (batched mouse cmds get
	// delayed/dropped; the PR #24 live finding).
	rec := &recordingMouse{}
	src := &stubMutator{stubSource: stubSource{issues: manyIssues(3)}}
	m := New(src).WithMouseController(rec)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, &src.stubSource)
	rec.calls = nil

	// list -> detail: release.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: enter should open detail; got %v", m.mode)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "disable" {
		t.Errorf("entering detail must release capture; calls = %v", rec.calls)
	}

	// prompts overlaid ON detail (a -> confirm-close) keep it released.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.mode != modeConfirmClose {
		t.Fatalf("setup: a should open confirm-close; got %v", m.mode)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: n should cancel back to detail; got %v", m.mode)
	}
	if len(rec.calls) != 1 {
		t.Errorf("a detail-overlaid prompt must not flip capture; calls = %v", rec.calls)
	}

	// help overlaid ON detail (? -> overlay -> dismiss) also keeps
	// it released — selecting a keybinding line out of the help text
	// is the overlay's own documented click-drag case (roborev #2111).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	if m.mode != modeHelp {
		t.Fatalf("setup: ? should open help; got %v", m.mode)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: ? should dismiss back to detail; got %v", m.mode)
	}
	if len(rec.calls) != 1 {
		t.Errorf("help overlaid on detail must not flip capture; calls = %v", rec.calls)
	}

	// detail -> list: re-capture.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Fatalf("esc should return to the list; got %v", m.mode)
	}
	if len(rec.calls) != 2 || rec.calls[1] != "enable" {
		t.Errorf("leaving detail must re-capture; calls = %v", rec.calls)
	}
}

func TestMouseCapture_MasterSwitchOffMeansNoSwitching(t *testing.T) {
	// With the persisted toggle off, view boundaries must not touch
	// terminal mouse state in either direction.
	rec := &recordingMouse{}
	src := &stubSource{issues: manyIssues(3)}
	m := New(src).WithSession(SessionState{MouseOff: true}, "").WithMouseController(rec)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	rec.calls = nil

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(rec.calls) != 0 {
		t.Errorf("mouseOff: view boundaries must not switch; calls = %v", rec.calls)
	}
}

func TestToggleMouse_InDetailDoesNotCaptureTheReadingView(t *testing.T) {
	// m inside the detail view flips the master switch but must not
	// capture the mouse THERE — the reading view stays released; the
	// list re-captures on the way out.
	rec := &recordingMouse{}
	src := &stubSource{issues: manyIssues(3)}
	m := New(src).WithMouseController(rec)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	rec.calls = nil

	// off… (already released in detail: no switch call)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if !m.mouseOff {
		t.Fatal("first m should turn the master switch off")
	}
	// …and back on, still inside detail: still no capture.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	if m.mouseOff {
		t.Fatal("second m should turn the master switch back on")
	}
	if len(rec.calls) != 0 {
		t.Errorf("toggling the master switch inside detail must not write mouse state; calls = %v", rec.calls)
	}
	// leaving detail then re-captures.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(rec.calls) != 1 || rec.calls[0] != "enable" {
		t.Errorf("leaving detail with the switch on must re-capture; calls = %v", rec.calls)
	}
}

func TestToggleMouse_InListSwitchesImmediately(t *testing.T) {
	// In the list (a navigation surface) the m toggle takes effect on
	// the spot, both directions.
	rec := &recordingMouse{}
	src := &stubSource{issues: manyIssues(3)}
	m := New(src).WithMouseController(rec)
	m = applyFetched(m, src)
	rec.calls = nil

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	_ = model
	if len(rec.calls) != 2 || rec.calls[0] != "disable" || rec.calls[1] != "enable" {
		t.Errorf("list-mode m must switch immediately both ways; calls = %v", rec.calls)
	}
}

func TestStartWithMouseCapture(t *testing.T) {
	// The launch-time half: main passes tea.WithMouseCellMotion iff
	// the model wants capture at start (list mode, master switch on).
	src := &stubSource{}
	if !New(src).StartWithMouseCapture() {
		t.Error("default launch should start captured")
	}
	if New(src).WithSession(SessionState{MouseOff: true}, "").StartWithMouseCapture() {
		t.Error("restored mouse_off must start released")
	}
}
