package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// Column IDs used by both the on-disk uiconfig file and the
// render-time visibility check. Kept as constants so a typo in one
// place becomes a compile error rather than a silently-ignored
// hidden column.
const (
	colIDOwner   = "owner"
	colIDRepo    = "repo"
	colIDBranch  = "branch"
	colIDType    = "type"
	colIDStatus  = "status"
	colIDUpdated = "updated"
	colIDSession = "session"
)

// toggleableCol describes one column the `o` overlay can hide/show.
// The remaining columns (ID, priority, title) are intentionally
// always on — title because it's the row's content, ID because it's
// how the user opens or references an issue, priority because it's
// 2 chars wide and never the column you wish you had back.
type toggleableCol struct {
	ID        string
	Label     string // human label shown in the overlay
	MultiOnly bool   // overlay shows a note and skips the toggle when single-repo
}

// toggleableColumns is the registry order the overlay numbers from
// 1. Multi-only columns sit at the top so the numbers stay stable
// when the view switches between single- and multi-repo modes.
var toggleableColumns = []toggleableCol{
	{ID: colIDOwner, Label: "Owner"},
	{ID: colIDRepo, Label: "Repo", MultiOnly: true},
	{ID: colIDBranch, Label: "Branch", MultiOnly: true},
	{ID: colIDType, Label: "Type"},
	{ID: colIDStatus, Label: "Status"},
	{ID: colIDUpdated, Label: "Updated"},
	{ID: colIDSession, Label: "Session"},
}

// colVisible reports whether a column should render: not hidden by the
// user (colsHidden) and not auto-hidden to fit a narrow terminal
// (autoHidden, recomputed each paint by viewList). Unknown IDs default
// to visible — a future column added without a uiconfig migration still
// appears.
func (m Model) colVisible(id string) bool {
	return !m.colsHidden[id] && !m.autoHidden[id]
}

// widthDropOrder is the sequence in which columns are auto-hidden to
// fit a narrow terminal — least valuable first. Owner (the
// responsibility badge) is last because it's the headline "whose move
// is it" signal; ID, priority, and title are never dropped.
var widthDropOrder = []string{colIDBranch, colIDSession, colIDUpdated, colIDType, colIDStatus, colIDRepo, colIDOwner}

// repoImpliedByID reports whether the Repo column is pure duplication
// of the ID column. Since the ID renders in full (`<workspace>-<suffix>`,
// would-you-kindly-rvv9), a row whose ID literally starts with its own
// `<repo>-` shows that workspace name twice. When that holds for EVERY
// decorated row, Repo is the cheapest column to give up — dropping it
// costs no information at all, unlike Branch or Status.
//
// Deliberately strict on two counts. A workspace whose bd prefix
// differs from its registry name (so the ID does NOT carry it) keeps
// its Repo column at normal priority, because there the column is the
// only place the workspace appears. And a row whose ID does not FIT
// m.cw.id counts as not-implied: the ID cell middle-elides, dropping
// the workspace name to save the suffix, so the prefix isn't on screen
// to imply anything. Without that second check the two behaviours
// collide exactly where it hurts — a narrow terminal truncates the ID
// AND sacrifices Repo first, leaving the row with no readable
// workspace name at all (roborev #4028).
func (m Model) repoImpliedByID(rows []beads.Issue) bool {
	decorated := false
	for _, i := range rows {
		if i.Repo == "" {
			continue
		}
		decorated = true
		if !strings.HasPrefix(i.ID, i.Repo+"-") {
			return false
		}
		if lipgloss.Width(i.ID) > m.cw.id {
			return false
		}
	}
	return decorated
}

// dropOrderFor returns widthDropOrder, moved to drop a redundant Repo
// column FIRST. Without this the wider full-ID column would squeeze the
// title on multi-repo views while a duplicate of the ID's own prefix
// kept its 18 cells.
func (m Model) dropOrderFor(rows []beads.Issue) []string {
	if !m.repoImpliedByID(rows) {
		return widthDropOrder
	}
	out := make([]string, 0, len(widthDropOrder))
	out = append(out, colIDRepo)
	for _, id := range widthDropOrder {
		if id != colIDRepo {
			out = append(out, id)
		}
	}
	return out
}

// computeAutoHidden returns the toggleable columns to hide PURELY to
// keep rows within the terminal width, on top of whatever the user hid
// via the `o` overlay. Render-time only (never persisted), so widening
// the terminal restores every column without disturbing saved prefs.
// Returns nil when everything already fits or the width is unknown.
// Reads m.colsHidden directly (not colVisible) to avoid recursion.
//
// Floor: the always-present columns plus a usable title can't be
// dropped, so the narrowest a row can get is cursor(2) + colID + sep +
// colPrio + sep + minTitle(20) ≈ 40 cols. Below that, every droppable
// column is already gone and titleBudget's 20-col floor means the row
// still slightly overruns — an inherent limit (you can't show an ID, a
// priority, and any title under 40 cols), not a bug. Terminals that
// narrow are not a realistic target.
func (m Model) computeAutoHidden() map[string]bool {
	const (
		sep      = 2  // the "  " between columns, mirrors renderRow
		minTitle = 20 // titleBudget's floor — keep the title usable
	)
	if m.width <= 0 {
		return nil
	}
	colWidth := map[string]int{
		colIDOwner: m.cw.owner, colIDRepo: m.cw.repo, colIDBranch: m.cw.branch,
		colIDType: m.cw.typ, colIDStatus: m.cw.status, colIDUpdated: m.cw.updated,
		colIDSession: m.cw.session,
	}
	// shown = rendered before any width-driven hiding: not user-hidden,
	// and repo/branch only count in multi-repo mode.
	shown := func(id string) bool {
		if m.colsHidden[id] {
			return false
		}
		if (id == colIDRepo || id == colIDBranch) && !m.isMultiRepo() {
			return false
		}
		return true
	}
	// Always-present cost: cursor + ID + Priority + a minimum title.
	total := 2 + (m.cw.id + sep) + (m.cw.prio + sep) + minTitle
	for id, w := range colWidth {
		if shown(id) {
			total += w + sep
		}
	}
	if total <= m.width {
		return nil
	}
	hidden := map[string]bool{}
	for _, id := range m.dropOrderFor(m.visible) {
		if total <= m.width {
			break
		}
		if shown(id) {
			hidden[id] = true
			total -= colWidth[id] + sep
		}
	}
	return hidden
}
