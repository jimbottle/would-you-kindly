package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// TestComputeAutoHidden_DropsLeastImportantFirst verifies the
// width-driven auto-hide: nothing at a wide width, and at a constrained
// width the least-valuable columns drop first while Status and Owner
// (the headline badge) survive.
func TestComputeAutoHidden_DropsLeastImportantFirst(t *testing.T) {
	src := &stubSource{issues: manyIssues(5)} // single-repo (no Repo set)
	m := applyFetched(New(src), src)
	m.cw = m.computeColWidths(m.visible) // seed widths as viewList does

	m.width = 0
	if m.computeAutoHidden() != nil {
		t.Error("unknown width should auto-hide nothing (nil)")
	}
	m.width = 1000
	if h := m.computeAutoHidden(); len(h) != 0 {
		t.Errorf("wide terminal should auto-hide nothing; got %v", h)
	}

	// Single-repo: the shown toggleables are owner, type, status,
	// updated, session (repo/branch don't render). Drop order is
	// session, updated, type, status, owner. Pick the exact width at
	// which session+updated+type have just dropped but status+owner
	// survive — derived from the computed widths so it's robust to the
	// content-sized columns.
	const sep, minTitle = 2, 20
	base := 2 + (m.cw.id + sep) + (m.cw.prio + sep) + minTitle
	full := base + (m.cw.owner + sep) + (m.cw.typ + sep) + (m.cw.status + sep) + (m.cw.updated + sep) + (m.cw.session + sep)
	m.width = full - (m.cw.session + sep) - (m.cw.updated + sep) - (m.cw.typ + sep)
	h := m.computeAutoHidden()
	if !h[colIDUpdated] || !h[colIDType] {
		t.Errorf("expected Updated+Type auto-hidden at width %d; got %v", m.width, h)
	}
	if h[colIDStatus] || h[colIDOwner] {
		t.Errorf("Status/Owner should survive at width %d; got %v", m.width, h)
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
	m.cw = m.computeColWidths(m.visible) // width-independent; seed once
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

// TestRepoImpliedByID pins when the Repo column counts as pure
// duplication of the full-ID column (would-you-kindly-rvv9).
func TestRepoImpliedByID(t *testing.T) {
	cases := []struct {
		name  string
		idCol int // m.cw.id — the width the ID cell will actually get
		rows  []beads.Issue
		want  bool
	}{
		{"every id carries its repo prefix and fits", 20, []beads.Issue{
			{ID: "alpha-1", Repo: "alpha"},
			{ID: "beta-9", Repo: "beta"},
		}, true},
		{"a repo whose bd prefix differs is NOT implied", 20, []beads.Issue{
			{ID: "alpha-1", Repo: "alpha"},
			{ID: "bd-7", Repo: "some-other-dir-name"},
		}, false},
		// The load-bearing case (roborev #4028): the ID cell is too
		// narrow, so it middle-elides and the workspace name is NOT on
		// screen. Repo must keep its normal priority or the row ends up
		// showing no readable workspace at all.
		{"a truncated id implies nothing", 10, []beads.Issue{
			{ID: "louisville-open-data-expenditure-bot-4jm", Repo: "louisville-open-data-expenditure-bot"},
		}, false},
		// Single-repo mode: no Repo column renders at all, so there is
		// nothing to call redundant.
		{"undecorated rows", 20, []beads.Issue{{ID: "alpha-1"}, {ID: "alpha-2"}}, false},
		{"no rows", 20, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{cw: colWidths{id: c.idCol}}
			if got := m.repoImpliedByID(c.rows); got != c.want {
				t.Errorf("repoImpliedByID = %v, want %v", got, c.want)
			}
		})
	}
}

// TestDropOrder_RedundantRepoGoesFirst verifies the width-pressure
// order puts a duplicated Repo column ahead of columns that carry
// information the row shows nowhere else.
func TestDropOrder_RedundantRepoGoesFirst(t *testing.T) {
	redundant := []beads.Issue{{ID: "alpha-1", Repo: "alpha"}, {ID: "beta-9", Repo: "beta"}}
	m := Model{cw: colWidths{id: 20}} // wide enough to render both IDs whole
	if got := m.dropOrderFor(redundant); got[0] != colIDRepo {
		t.Errorf("drop order starts with %q, want %q when Repo duplicates the ID", got[0], colIDRepo)
	}
	// Order is otherwise untouched, and no column is lost or doubled.
	got := m.dropOrderFor(redundant)
	if len(got) != len(widthDropOrder) {
		t.Fatalf("drop order has %d entries, want %d", len(got), len(widthDropOrder))
	}
	if got[len(got)-1] != colIDOwner {
		t.Errorf("Owner must still be the last column dropped; got %q", got[len(got)-1])
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("column %q appears twice in the drop order", id)
		}
		seen[id] = true
	}

	// Distinct-prefix workspaces keep the stock order — there the Repo
	// column is the only place the workspace name appears.
	distinct := []beads.Issue{{ID: "bd-7", Repo: "some-other-dir-name"}}
	if got := m.dropOrderFor(distinct); got[0] != widthDropOrder[0] {
		t.Errorf("non-redundant Repo should keep the stock order; got %q first", got[0])
	}
}
