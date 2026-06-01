package tui

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
var widthDropOrder = []string{colIDBranch, colIDUpdated, colIDType, colIDStatus, colIDRepo, colIDOwner}

// computeAutoHidden returns the toggleable columns to hide PURELY to
// keep rows within the terminal width, on top of whatever the user hid
// via the `o` overlay. Render-time only (never persisted), so widening
// the terminal restores every column without disturbing saved prefs.
// Returns nil when everything already fits or the width is unknown.
// Reads m.colsHidden directly (not colVisible) to avoid recursion.
func (m Model) computeAutoHidden() map[string]bool {
	const (
		sep      = 2  // the "  " between columns, mirrors renderRow
		minTitle = 20 // titleBudget's floor — keep the title usable
	)
	if m.width <= 0 {
		return nil
	}
	colWidth := map[string]int{
		colIDOwner: colResp, colIDRepo: colRepo, colIDBranch: colBranch,
		colIDType: colType, colIDStatus: colStatus, colIDUpdated: colUpdated,
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
	total := 2 + (colID + sep) + (colPrio + sep) + minTitle
	for id, w := range colWidth {
		if shown(id) {
			total += w + sep
		}
	}
	if total <= m.width {
		return nil
	}
	hidden := map[string]bool{}
	for _, id := range widthDropOrder {
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
