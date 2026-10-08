package tui

import (
	"strings"
	"testing"
	"time"

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

func TestFilter_ArrowKeysMoveCursorWhileTyping(t *testing.T) {
	// Rows stay selectable while the / prompt is open: arrows (and
	// ctrl+n/ctrl+p) move the list cursor without leaving modeFilter,
	// so the user can pick a row over the live-narrowed list instead
	// of having to apply the filter first (would-you-kindly-mwry).
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("down must not leave modeFilter; mode=%v", m.mode)
	}
	if m.cursor != 1 {
		t.Fatalf("down should move cursor to 1; got %d", m.cursor)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = model.(Model)
	if m.cursor != 2 {
		t.Fatalf("ctrl+n should move cursor to 2; got %d", m.cursor)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(Model)
	if m.cursor != 2 {
		t.Fatalf("down at the last row must clamp; got %d", m.cursor)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(Model)
	if m.cursor != 1 {
		t.Fatalf("up should move cursor back to 1; got %d", m.cursor)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = model.(Model)
	if m.cursor != 0 {
		t.Fatalf("ctrl+p should move cursor to 0; got %d", m.cursor)
	}

	// The arrows must not have leaked into the query.
	if got := m.input.Value(); got != "" {
		t.Fatalf("cursor keys must not type into the filter input; value=%q", got)
	}

	// Applying keeps the selection made inside the prompt.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList || m.cursor != 1 {
		t.Fatalf("enter should return to list with cursor kept; mode=%v cursor=%d", m.mode, m.cursor)
	}
}

func typeFilter(t *testing.T, m Model, q string) Model {
	t.Helper()
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("setup: expected modeFilter, got %v", m.mode)
	}
	for _, r := range q {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	return m
}

func TestFilter_ClosingPromptKeepsFilter(t *testing.T) {
	// The query applies live as the user types, so esc and enter just
	// close the prompt and keep the filter. There's no separate
	// "apply" step to forget, and reaching for esc doesn't throw the
	// typed query away.
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &stubSource{issues: sampleIssues()}
			m := applyFetched(New(src), src)
			m = typeFilter(t, m, "rotate")
			if len(m.visible) != 1 {
				t.Fatalf("setup: live filter should narrow to 1 row; got %d", len(m.visible))
			}

			model, _ := m.Update(tc.key)
			m = model.(Model)
			if m.mode != modeList {
				t.Fatalf("%s should return to modeList; got %v", tc.name, m.mode)
			}
			if m.query != "rotate" {
				t.Errorf("%s should keep the query; got %q", tc.name, m.query)
			}
			if got := len(m.visible); got != 1 {
				t.Errorf("%s should keep the list narrowed; visible=%d", tc.name, got)
			}
			// Reopening the prompt resumes editing the kept query.
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
			m = model.(Model)
			if v := m.input.Value(); v != "rotate" {
				t.Errorf("reopened prompt should hold the kept query; got %q", v)
			}
		})
	}
}

func TestFilter_SlashClosesEmptyPromptOnly(t *testing.T) {
	// / on an empty prompt closes it (the / toggle), but once there's
	// text it's a literal slash — branches like feat/x are matched
	// fields and must stay searchable.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m = typeFilter(t, m, "feat/x")
	if m.mode != modeFilter {
		t.Fatalf("/ inside a non-empty query must not close the prompt; mode=%v", m.mode)
	}
	if m.query != "feat/x" {
		t.Errorf("/ should be typed as text; query=%q", m.query)
	}

	// Empty prompt: / closes it.
	m = applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("setup: / should open the prompt; mode=%v", m.mode)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("/ on an empty prompt should close it; mode=%v", m.mode)
	}
}

func TestFilter_EscInListClearsAppliedFilter(t *testing.T) {
	// After enter applies a filter, esc from the list clears it —
	// the empty-view hint promises "esc to clear the fuzzy filter".
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m = typeFilter(t, m, "rotate")
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList || m.query != "rotate" || len(m.visible) != 1 {
		t.Fatalf("setup: enter should apply the filter; mode=%v query=%q visible=%d", m.mode, m.query, len(m.visible))
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.query != "" {
		t.Errorf("esc in list should clear the applied query; got %q", m.query)
	}
	if got := len(m.visible); got != len(sampleIssues()) {
		t.Errorf("esc should restore the full list; visible=%d", got)
	}
	if m.status != "cleared filter" {
		t.Errorf("expected 'cleared filter' status; got %q", m.status)
	}
}

func TestFilter_EscInListClearsFilterBeforeMarks(t *testing.T) {
	// With both a filter and marks active, the first esc drops only
	// the filter; marks survive for a second esc.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.marked = map[string]bool{"a-2": true}
	m = typeFilter(t, m, "rotate")
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.query != "" {
		t.Errorf("first esc should clear the filter; got %q", m.query)
	}
	if len(m.marked) != 1 {
		t.Errorf("first esc must leave marks alone; marked=%v", m.marked)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if len(m.marked) != 0 {
		t.Errorf("second esc should clear marks; marked=%v", m.marked)
	}
}

func TestFilter_EscClearResolvesDepsUnderDepsSort(t *testing.T) {
	// Clearing the filter widens the list to rows the filter had
	// hidden; under the deps sort those rows may have no cached
	// edges, so esc from the list must schedule resolution the way
	// enter does (roborev #4692). (esc in the prompt keeps the filter,
	// so it never widens the list.)
	src := &stubDepSource{
		stubSource: stubSource{issues: sampleIssues()},
		edges:      map[string][]string{"a-2": {"a-1"}},
	}
	m := New(src)
	m = applyFetched(m, &src.stubSource)
	m = pressSortToDeps(m)
	if m.depLister == nil || m.sortBy != sortDeps {
		t.Fatalf("setup: deps sort with a DepLister; sortBy=%v lister=%v", m.sortBy, m.depLister != nil)
	}
	// Only the filtered-in row's edges are cached.
	m.depCache = map[string][]beads.Issue{"a-1": nil}
	defer withFlashClearDelay(t, time.Millisecond)()

	// Type a filter, close the prompt, then esc from the list.
	m = typeFilter(t, m, "rotate")
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.query != "" {
		t.Fatalf("esc in the list should clear the filter; got %q", m.query)
	}
	// The batch must actually carry the resolver alongside the
	// flash clear: with a pure flash-clear batch a-2 stays uncached.
	m = applyResolveCmd(t, m, cmd)
	if _, ok := m.depCache["a-2"]; !ok {
		t.Error("esc in the list should resolve the widened list's deps; a-2 still uncached")
	}
}
