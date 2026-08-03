package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
)

// This file holds the column overlay, column ordering, and the sticky header.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestStickyHeader_HeaderAndAllRowsFitWithoutScroll(t *testing.T) {
	// 5 rows + terminal large enough to show everything → no
	// "↑/↓ more" hints, scroll stays at 0.
	src := &stubSource{issues: manyIssues(5)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.scroll != 0 {
		t.Errorf("scroll should be 0 when everything fits; got %d", m.scroll)
	}
	out := m.View()
	for _, want := range []string{"row 1", "row 5", "ID", "Status"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in view; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more above") || strings.Contains(out, "more below") {
		t.Errorf("no scroll-hint expected when everything fits; got:\n%s", out)
	}
}

func TestStickyHeader_BodyCappedToTerminalHeight(t *testing.T) {
	// 30 rows + cramped terminal → viewport shows a small window;
	// the column header MUST still appear in the rendered output.
	// This is the core fix: pre-72y the terminal scrolled the
	// header off the top.
	src := &stubSource{issues: manyIssues(30)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if !strings.Contains(out, "Status") {
		t.Errorf("header row should remain visible at the top of every paint; got:\n%s", out)
	}
	if !strings.Contains(out, "row 1") {
		t.Errorf("cursor row should be visible (cursor=0, row 1); got:\n%s", out)
	}
	// Some row beyond what fits in the body should NOT be in the
	// rendered output — proving the body is capped, not dumped.
	if strings.Contains(out, "row 30") {
		t.Errorf("row 30 should be off-screen in a cramped terminal; got:\n%s", out)
	}
	if !strings.Contains(out, "more below") {
		t.Errorf("expected '↓ N more below' hint when rows are clipped; got:\n%s", out)
	}
}

func TestStickyHeader_CursorScrollFollowsDown(t *testing.T) {
	// Press j past the bottom of the viewport — scroll must
	// advance so the cursor row stays visible, and the "↑ more
	// above" hint must appear.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 15; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	if m.cursor != 15 {
		t.Fatalf("cursor expected at 15 after 15 j's; got %d", m.cursor)
	}
	if m.scroll == 0 {
		t.Errorf("scroll should have advanced past 0; got %d", m.scroll)
	}
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the rendered window [%d, %d)", m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
	out := m.View()
	if !strings.Contains(out, "more above") {
		t.Errorf("expected '↑ N more above' hint after scrolling down; got:\n%s", out)
	}
}

func TestStickyHeader_TopAndBottomKeysAdjustScroll(t *testing.T) {
	// G jumps to the last row → scroll lands so the last row is
	// visible. g jumps back to the top → scroll = 0.
	src := &stubSource{issues: manyIssues(25)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = model.(Model)
	if m.cursor != 24 {
		t.Errorf("G expected to land on the last row (24); got %d", m.cursor)
	}
	if m.cursor < m.scroll {
		t.Errorf("G left the cursor above the scroll window: cursor=%d scroll=%d", m.cursor, m.scroll)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("g expected to land on row 0; got %d", m.cursor)
	}
	if m.scroll != 0 {
		t.Errorf("g should pull scroll to 0; got %d", m.scroll)
	}
}

func TestStickyHeader_WindowResizeClampsScroll(t *testing.T) {
	// User scrolls down, then resizes the terminal taller. The
	// scroll offset should re-clamp so we don't leave blank rows
	// past the end of the data.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 10})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 18; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	beforeScroll := m.scroll
	if beforeScroll == 0 {
		t.Fatal("setup: scroll should be > 0 after pressing j 18 times")
	}
	model, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = model.(Model)
	if m.scroll != 0 {
		t.Errorf("after resizing tall enough to show everything, scroll should clamp to 0; got %d (cursor=%d)", m.scroll, m.cursor)
	}
}

func TestStickyHeader_CursorStaysInViewWhenStatusBannerAppears(t *testing.T) {
	// Regression for the chrome-shrink-mid-update case: a write
	// failure sets m.status (no refetch), which grows chromeExtra()
	// by 1 and shrinks bodyHeight() by 1. If scroll isn't
	// re-clamped at that point, a cursor sitting at the bottom of
	// a long, scrolled list falls just outside the now-smaller
	// rendered window — the highlighted row briefly disappears.
	src := &stubSource{issues: manyIssues(40)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	// Drive the cursor down so it sits at the bottom of the
	// rendered window.
	for i := 0; i < 25; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	bodyBefore := m.bodyHeight()
	// Simulate a write failure: writeMsg with err non-nil → handleWriteResult
	// sets m.status and returns without a refetch.
	model, _ = m.Update(writeMsg{action: "close", id: m.visible[m.cursor].ID, err: errors.New("bd: simulated")})
	m = model.(Model)

	if m.status == "" {
		t.Fatal("setup: expected m.status to be set by the failure")
	}
	bodyAfter := m.bodyHeight()
	if bodyAfter >= bodyBefore {
		t.Fatalf("setup: expected bodyHeight to shrink with the new banner (was %d, now %d)", bodyBefore, bodyAfter)
	}
	// The actual invariant: cursor must still be inside the
	// rendered window after the chrome grew.
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the rendered window [%d, %d) after status banner appeared",
			m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
	// And the view must actually contain the cursor row.
	out := m.View()
	cursorRow := fmt.Sprintf("row %d", m.cursor+1)
	if !strings.Contains(out, cursorRow) {
		t.Errorf("cursor row %q missing from view (transient clip):\n%s", cursorRow, out)
	}
}

func TestStickyHeader_CursorStaysInViewWhenModalOpens(t *testing.T) {
	// Same invariant for the modal-entry path: opening modeFilter
	// (or any modal) adds 2 lines of chrome. The re-clamp call in
	// the entry handler must keep the cursor on-screen.
	src := &stubSource{issues: manyIssues(40)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 25; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	bodyBefore := m.bodyHeight()
	// Press '/' to open the fuzzy-filter prompt (modeFilter → +2 chrome).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("setup: expected modeFilter, got %v", m.mode)
	}
	bodyAfter := m.bodyHeight()
	if bodyAfter >= bodyBefore {
		t.Fatalf("setup: expected bodyHeight to shrink when modal opens (was %d, now %d)", bodyBefore, bodyAfter)
	}
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the viewport [%d, %d) when modal opened",
			m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
}

func TestColumnOrder_OwnerIsSecondFromLeft_LegacyHumanRenameCheck(t *testing.T) {
	// Header pin: in multi-repo mode the responsibility column
	// header is "Owner" (renamed from "human" so the column can carry
	// AGENT badges too). "Owner" must appear before "Repo" so the
	// responsibility signal stays second-from-left.
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "row in alpha"},
		{ID: "beta-9", Repo: "beta", Title: "row in beta"},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	oi := strings.Index(out, "Owner")
	ri := strings.Index(out, "Repo")
	if oi < 0 || ri < 0 {
		t.Fatalf("expected both 'Owner' and 'Repo' headers in view; got:\n%s", out)
	}
	if oi > ri {
		t.Errorf("'Owner' header should appear before 'Repo' header in the column row; got Owner at %d, Repo at %d", oi, ri)
	}
}

func TestColumnOrder_OwnerHeaderIsSecondFromLeft(t *testing.T) {
	// The column header renamed from 'human' to 'Owner' to reflect
	// the broader responsibility framing. Header must still appear
	// before 'Repo' (second-from-left position invariant).
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "x"},
		{ID: "beta-9", Repo: "beta", Title: "y"},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	oi := strings.Index(out, "Owner")
	ri := strings.Index(out, "Repo")
	if oi < 0 {
		t.Errorf("'Owner' header missing from view:\n%s", out)
	}
	if oi > ri {
		t.Errorf("'Owner' should appear before 'Repo'; got Owner=%d Repo=%d", oi, ri)
	}
}

func TestColumnsOverlay_TogglesHidesColumnFromHeader(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	// Force a known terminal width so titleBudget is non-zero and
	// renderHeader has all columns laid out.
	m.width = 200

	// Sanity: type column is in the header by default.
	if !strings.Contains(m.renderHeader(), "Type") {
		t.Fatalf("baseline header should include the Type column")
	}

	// Open overlay with `o`.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	if m.mode != modeColumns {
		t.Fatalf("expected modeColumns after pressing o; got %v", m.mode)
	}

	// Press 4 — toggleableColumns[3] is "type" (owner=1, repo=2,
	// branch=3, type=4). Multi-only entries are inert in
	// single-repo mode but still occupy a slot — so the test
	// relies on the registry order, not on a runtime filter.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m = model.(Model)
	if !m.colsHidden[colIDType] {
		t.Errorf("expected colIDType hidden after pressing 4; got hidden=%v", m.colsHidden)
	}

	// Press esc to close the overlay.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("expected modeList after esc; got %v", m.mode)
	}

	// Header no longer renders the T column.
	if strings.Contains(m.renderHeader(), "Type") {
		t.Errorf("expected Type column hidden from header; got %q", m.renderHeader())
	}
}

func TestColumnsOverlay_PersistsHiddenColumnsToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.json")

	src := &stubSource{issues: sampleIssues()}
	m := New(src).WithHiddenColumns(map[string]bool{}, path)
	m = applyFetched(m, src)

	// Open, toggle the owner column (slot 1), close.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = model.(Model)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Load straight from disk to confirm the save happened.
	cfg, err := uiconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.HiddenColumns) != 1 || cfg.HiddenColumns[0] != colIDOwner {
		t.Errorf("HiddenColumns = %v, want [%q]", cfg.HiddenColumns, colIDOwner)
	}
}

func TestColumnsOverlay_MultiOnlySlotInertInSingleRepo(t *testing.T) {
	// In single-repo mode the repo/branch slots (2-3) are
	// inert — pressing them shouldn't toggle anything. The slot
	// numbering still has to match the registry order so the
	// keystroke means the same column whether wyk launches into
	// single- or multi-repo mode.
	src := &stubSource{issues: sampleIssues()} // sampleIssues has no Repo → single-repo
	m := applyFetched(New(src), src)
	if m.isMultiRepo() {
		t.Fatalf("test premise: sampleIssues should produce a single-repo view")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	// Slot 2 → repo (multi-only). Slot 3 → branch.
	for _, r := range []rune{'2', '3'} {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	if len(m.colsHidden) != 0 {
		t.Errorf("multi-only slot toggles should be no-ops in single-repo mode; got %v", m.colsHidden)
	}

	// Confirm the registry order — slot 2 still names repo after
	// the no-op, so a user who learns the mapping in one mode
	// keeps it in the other.
	if toggleableColumns[1].ID != colIDRepo {
		t.Errorf("slot 2 should be repo (multi-only); got %q", toggleableColumns[1].ID)
	}
}
