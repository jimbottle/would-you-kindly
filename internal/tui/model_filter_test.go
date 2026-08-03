package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds modeFilter: the fuzzy/substring query and what it matches.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestFuzzyFilterNarrowsVisible(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	if got, want := len(m.visible), 3; got != want {
		t.Fatalf("setup: visible = %d, want %d", got, want)
	}

	// open the / prompt
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)

	// type "release" character by character so the textinput model receives each rune
	for _, r := range "release" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	// confirm
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	if len(m.visible) != 1 || m.visible[0].ID != "a-3" {
		t.Errorf("fuzzy filter: visible = %+v, want only a-3", m.visible)
	}
}

func TestFuzzyFilterDoesNotBleedAcrossTitleDescBoundary(t *testing.T) {
	// Title and description are scored independently. A query that
	// would only match as a subsequence spanning the boundary
	// (e.g. "ad" against {title: "cat", desc: "dog"} — `a` in
	// "cat", `d` in "dog") must NOT match.
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "cat", Description: "dog", Labels: nil},
		{ID: "a-2", Title: "rotate password", Description: "step",
			Labels: []string{"human"}},
	}}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "ad" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	for _, i := range m.visible {
		if i.ID == "a-1" {
			t.Errorf("'ad' should NOT cross-field-match a-1 {cat, dog}; visible: %+v",
				visibleIDs(m.visible))
		}
	}
}

func TestFuzzyFilterMatchesSubsequence(t *testing.T) {
	// sahilm/fuzzy ranks by subsequence score, so a query that's
	// NOT a substring but IS a subsequence still matches. This is
	// the capability the brief's "fuzzy text filter" called for and
	// the old strings.Contains implementation couldn't deliver.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	// "rpw" is not a substring of any issue but IS a subsequence of
	// "rotate password" (r-o-t-a-te-P-asswo-W → r-p-w).
	for _, r := range "rpw" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	if len(m.visible) == 0 {
		t.Fatal("fuzzy filter should find 'rotate password' for query 'rpw'")
	}
	if m.visible[0].ID != "a-1" {
		t.Errorf("best fuzzy match should be a-1 (rotate password); got %q", m.visible[0].ID)
	}
}

func TestFilter_DescriptionMatchesSubstringNotSubsequence(t *testing.T) {
	// Regression: a fuzzy subsequence over a long description matched
	// almost anything (a 7-char query like "android" finds a scattered
	// a·n·d·r·o·i·d in nearly any body), flooding the filter. The
	// description must now match only as a CONTIGUOUS substring. The
	// title still matches as a subsequence (see the test above).
	src := &stubSource{issues: []beads.Issue{
		// "android" is a subsequence of "and droid" but NOT a substring,
		// and the title carries no a·n·d·r·o·i·d subsequence (no 'n').
		{ID: "noise", Title: "Rotate creds", Description: "and droid stuff"},
		// "android" is a real substring of the description → matches.
		{ID: "real", Title: "Data Safety form", Description: "the android app needs review"},
	}}
	m := applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "android" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	got := visibleIDs(m.visible)
	if len(got) != 1 || got[0] != "real" {
		t.Errorf("query \"android\" should match only the substring row; got %v", got)
	}
}

func TestFilter_MatchesRepoBranchAndID(t *testing.T) {
	// Repo is the most common filter target — the filter must match it
	// (plus branch and ID) as a substring, not only title/description.
	src := &stubSource{issues: []beads.Issue{
		{ID: "android-1", Repo: "android", Branch: "main", Title: "unrelated title", Description: ""},
		{ID: "ebay-9", Repo: "ebay-watchlist-watch", Branch: "feat/x", Title: "another thing", Description: "nothing here"},
	}}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"android", []string{"android-1"}}, // repo (and ID) substring
		{"feat/x", []string{"ebay-9"}},     // branch substring
		{"ebay-9", []string{"ebay-9"}},     // ID substring
		{"watchlist", []string{"ebay-9"}},  // repo substring
	} {
		m := applyFetched(New(src), src)
		m.query = c.query
		m.recomputeVisible()
		got := visibleIDs(m.visible)
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("query %q: got %v, want %v", c.query, got, c.want)
		}
	}
}

func TestFilterChip_RendersWhenActiveOnlyOnNonDefaultPresetOrCap(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// Default state — no chip line.
	if got := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); got != "" {
		t.Errorf("default preset + no cap should produce no chip; got %q", got)
	}

	// After a priority cap — chip appears.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = model.(Model)
	if got := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); !strings.Contains(got, "P1") {
		t.Errorf("expected ≤P1 chip after pressing '2'; got %q", got)
	}

	// After preset switch + cap — both chips appear.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed)
	if !strings.Contains(chips, "human") || !strings.Contains(chips, "P1") {
		t.Errorf("expected both human + P1 chips; got %q", chips)
	}
}
