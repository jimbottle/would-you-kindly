package tui

import (
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// TestRenderRow_ReviewMark pins the per-row provenance marker: a row whose
// issue carries the review label (label=roborev) renders the ◆ glyph in the
// title cell; a non-review row does not. Asserts against the rendered style
// output so it's independent of the test environment's colour profile.
func TestRenderRow_ReviewMark(t *testing.T) {
	forceColor(t)
	src := &stubSource{issues: []beads.Issue{
		{ID: "rv-1", Title: "flaky test on CI", Status: "open", Labels: []string{"roborev"}},
		{ID: "ag-1", Title: "add metrics endpoint", Status: "open", Labels: []string{"src:agent"}},
	}}
	m := applyFetched(New(src), src)
	m.width = 200

	want := reviewMarkStyle.Render(reviewMarkGlyph)

	if rev := m.renderRow(m.visible[0], false); !strings.Contains(rev, want) {
		t.Errorf("review-sourced row should carry the ◆ marker; row: %q", rev)
	}
	if plain := m.renderRow(m.visible[1], false); strings.Contains(plain, want) {
		t.Errorf("non-review row should NOT carry the marker; row: %q", plain)
	}
}
