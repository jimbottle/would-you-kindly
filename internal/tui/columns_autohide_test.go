package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/raylytics/would-you-kindly/internal/beads"
)

// TestComputeAutoHidden_DropsLeastImportantFirst verifies the
// width-driven auto-hide: nothing at a wide width, and at a constrained
// width the least-valuable columns drop first while Status and Owner
// (the headline badge) survive.
func TestComputeAutoHidden_DropsLeastImportantFirst(t *testing.T) {
	src := &stubSource{issues: manyIssues(5)} // single-repo (no Repo set)
	m := applyFetched(New(src), src)

	m.width = 0
	if m.computeAutoHidden() != nil {
		t.Error("unknown width should auto-hide nothing (nil)")
	}
	m.width = 200
	if h := m.computeAutoHidden(); len(h) != 0 {
		t.Errorf("wide terminal should auto-hide nothing; got %v", h)
	}

	// At 65 cols the two least-valuable shown columns (Updated, Type)
	// drop; Status and Owner are kept.
	m.width = 65
	h := m.computeAutoHidden()
	if !h[colIDUpdated] || !h[colIDType] {
		t.Errorf("expected Updated+Type auto-hidden at 65 cols; got %v", h)
	}
	if h[colIDStatus] || h[colIDOwner] {
		t.Errorf("Status/Owner should survive at 65 cols; got %v", h)
	}
}

// TestNarrowWidth_RowDoesNotOverflow is the regression guard for the
// finding itself: across a range of widths, a row's visual width never
// exceeds the terminal (no wrap/clip), because auto-hide keeps the
// fixed columns plus a floored title within budget. The sweep starts at
// 40 — the inherent floor (cursor + ID + priority + a 20-col title;
// see computeAutoHidden) below which a row can't physically fit and a
// slight overrun is unavoidable.
func TestNarrowWidth_RowDoesNotOverflow(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{{
		ID: "a-1", Title: strings.Repeat("x", 200), Status: "open", Priority: 2,
	}}}
	m := applyFetched(New(src), src)
	for _, w := range []int{40, 50, 65, 80, 120, 200} {
		m.width = w
		m.autoHidden = m.computeAutoHidden()
		row := m.renderRow(m.visible[0], false)
		if got := lipgloss.Width(row); got > w {
			t.Errorf("width %d: row visual width %d overflows", w, got)
		}
	}
}

// TestComputeAutoHidden_RespectsUserHidden confirms width-hiding stacks
// on top of the user's own hidden columns without resurrecting them.
func TestComputeAutoHidden_RespectsUserHidden(t *testing.T) {
	src := &stubSource{issues: manyIssues(5)}
	m := applyFetched(New(src), src)
	m.colsHidden = map[string]bool{colIDStatus: true}
	m.width = 200 // wide: nothing auto-hidden
	m.autoHidden = m.computeAutoHidden()
	if m.colVisible(colIDStatus) {
		t.Error("a user-hidden column must stay hidden even at a wide width")
	}
	if !m.colVisible(colIDOwner) {
		t.Error("a non-hidden column should be visible at a wide width")
	}
}
