package tui

// Column IDs used by both the on-disk uiconfig file and the
// render-time visibility check. Kept as constants so a typo in one
// place becomes a compile error rather than a silently-ignored
// hidden column.
const (
	colIDOwner    = "owner"
	colIDWyk      = "wyk"
	colIDRepo     = "repo"
	colIDBranch   = "branch"
	colIDType     = "type"
	colIDStatus   = "status"
	colIDUpdated  = "updated"
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
	{ID: colIDWyk, Label: "Wyk hook", MultiOnly: true},
	{ID: colIDRepo, Label: "Repo", MultiOnly: true},
	{ID: colIDBranch, Label: "Branch", MultiOnly: true},
	{ID: colIDType, Label: "Type"},
	{ID: colIDStatus, Label: "Status"},
	{ID: colIDUpdated, Label: "Updated"},
}

// colVisible reports whether a column should render. Unknown IDs
// default to visible — a future column added without a uiconfig
// migration still appears.
func (m Model) colVisible(id string) bool {
	return !m.colsHidden[id]
}

// toggleableColIDs returns the column IDs the `o` overlay can
// touch, in display order.
func toggleableColIDs() []string {
	out := make([]string, len(toggleableColumns))
	for i, c := range toggleableColumns {
		out[i] = c.ID
	}
	return out
}
