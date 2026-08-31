package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// splitFixture returns a model at a split-sized terminal with a fetched
// list of issues that have descriptions (so the pane has a body).
func splitFixture(t *testing.T, w, h int) (Model, *stubSource) {
	t.Helper()
	issues := manyIssues(30)
	for i := range issues {
		issues[i].Description = strings.Repeat("step "+issues[i].ID+" — do the thing. ", 6)
		issues[i].Priority = i % 5
	}
	issues[0].Labels = []string{"human", "src:agent"}
	src := &stubSource{issues: issues}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = applyFetched(model.(Model), src)
	return m, src
}

// detailStub is a Source that also serves Detail so the debounced
// follow has something to enrich with.
type detailStub struct {
	*stubSource
	detailCalls []string
}

func (d *detailStub) Detail(_ context.Context, i beads.Issue) (beads.Issue, error) {
	d.detailCalls = append(d.detailCalls, i.ID)
	i.Notes = "note for " + i.ID
	return i, nil
}

func TestSplit_AutoActivatesAtBreakpoint(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{splitMinWidth, splitMinHeight, true},
		{splitMinWidth - 1, splitMinHeight, false},
		{splitMinWidth, splitMinHeight - 1, false},
		{200, 50, true},
		{80, 24, false},
	}
	for _, c := range cases {
		m, _ := splitFixture(t, c.w, c.h)
		if got := m.splitActive(); got != c.want {
			t.Errorf("%dx%d: splitActive=%v, want %v", c.w, c.h, got, c.want)
		}
	}
}

func TestSplit_ToggleKeyCyclesAndExplains(t *testing.T) {
	m, _ := splitFixture(t, 160, 40)
	if !m.splitActive() {
		t.Fatal("precondition: split should be active at 160x40")
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = model.(Model)
	if m.splitActive() || m.layoutPref != layoutStacked {
		t.Fatalf("p should hide the pane; active=%v pref=%v", m.splitActive(), m.layoutPref)
	}
	if !strings.Contains(m.status, "hidden") {
		t.Errorf("status should say the pane is hidden; got %q", m.status)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = model.(Model)
	if !m.splitActive() || m.layoutPref != layoutAuto {
		t.Fatalf("p again should restore auto; active=%v pref=%v", m.splitActive(), m.layoutPref)
	}

	// Below the auto breakpoint but above the forced floor: p forces
	// the split on.
	m, _ = splitFixture(t, 120, 30)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = model.(Model)
	if !m.splitActive() || m.layoutPref != layoutSplit {
		t.Fatalf("p at 120x30 should force split; active=%v pref=%v", m.splitActive(), m.layoutPref)
	}

	// Below even the forced floor: pref stays put and the status says why.
	m, _ = splitFixture(t, 80, 24)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = model.(Model)
	if m.splitActive() {
		t.Fatal("80x24 must not split")
	}
	if !strings.Contains(m.status, "too small") {
		t.Errorf("status should explain the terminal is too small; got %q", m.status)
	}
}

func TestSplit_PaneFollowsCursorWithDebounce(t *testing.T) {
	m, src := splitFixture(t, 160, 40)
	ds := &detailStub{stubSource: src}
	m.src = ds

	// The first frame after the fetch already stages row 0.
	if m.detailIssue.ID != "a-1" {
		t.Fatalf("pane should stage the cursor row on fetch; got %q", m.detailIssue.ID)
	}

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	if m.detailIssue.ID != "a-2" {
		t.Fatalf("pane should follow j immediately; got %q", m.detailIssue.ID)
	}
	if cmd == nil {
		t.Fatal("moving the cursor must schedule the follow debounce")
	}
	if len(ds.detailCalls) != 0 {
		t.Fatalf("enrichment must wait for the debounce; got calls %v", ds.detailCalls)
	}

	// A stale tick (older gen) is dropped; the live one enriches.
	model, cmd = m.Update(detailFollowMsg{gen: m.followGen - 1})
	m = model.(Model)
	if cmd != nil {
		t.Fatal("stale follow tick must be a no-op")
	}
	model, cmd = m.Update(detailFollowMsg{gen: m.followGen})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("live follow tick must dispatch the enrichment")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				if dm, ok := c().(detailMsg); ok {
					msg = dm
				}
			}
		}
	}
	dm, ok := msg.(detailMsg)
	if !ok {
		t.Fatalf("expected a detailMsg from the enrichment; got %T", msg)
	}
	model, _ = m.Update(dm)
	m = model.(Model)
	if m.detailIssue.Notes != "note for a-2" {
		t.Fatalf("enrichment should land on the pane while the list is focused; notes=%q", m.detailIssue.Notes)
	}

	// Enter focuses the pane without re-staging the slim row (the
	// notes must survive).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail || !m.paneFocused() {
		t.Fatalf("enter should focus the pane; mode=%v", m.mode)
	}
	if m.detailIssue.Notes != "note for a-2" {
		t.Fatalf("focusing the pane must keep the enriched issue; notes=%q", m.detailIssue.Notes)
	}
	if !m.splitView() {
		t.Fatal("the focused pane still renders through the split composition")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Fatalf("esc should return focus to the list; mode=%v", m.mode)
	}
}

func TestSplit_RefetchRefreshesStagedRowKeepingNotes(t *testing.T) {
	m, src := splitFixture(t, 160, 40)
	m.detailIssue.Notes = "kept"
	src.issues[0].Title = "renamed"
	src.issues[0].Labels = []string{"src:agent"}
	m = applyFetched(m, src)
	if m.detailIssue.Title != "renamed" {
		t.Fatalf("pane should pick up the refetched row; title=%q", m.detailIssue.Title)
	}
	if m.detailIssue.Notes != "kept" {
		t.Fatalf("refresh of the same row must carry notes across; notes=%q", m.detailIssue.Notes)
	}
}

func TestSplit_ViewLinesAlignOnDivider(t *testing.T) {
	m, _ := splitFixture(t, 160, 40)
	view := m.View()
	if os.Getenv("WYK_DUMP") != "" {
		t.Logf("\n%s", view)
	}
	g := m.splitGeometry()
	lines := strings.Split(view, "\n")
	var bodyLines int
	for _, l := range lines {
		if !strings.Contains(l, splitDivider) {
			continue
		}
		bodyLines++
		if w := lipgloss.Width(l); w != g.listW+lipgloss.Width(splitDivider)+g.paneW {
			t.Errorf("body line width %d, want %d: %q", w, g.listW+3+g.paneW, l)
		}
	}
	if bodyLines != g.rows {
		t.Fatalf("expected %d body rows with a divider, got %d", g.rows, bodyLines)
	}
	if !strings.Contains(view, "instructions") {
		t.Error("pane should show the runbook section")
	}
	if !strings.Contains(view, "HUMAN") {
		t.Error("pane/list should show the HUMAN badge for row 0")
	}
	if strings.Count(view, "\n") > 40 {
		t.Errorf("split view is %d lines for a 40-row terminal", strings.Count(view, "\n")+1)
	}
}

func TestSplit_StackedIsUnchangedBelowBreakpoint(t *testing.T) {
	m, _ := splitFixture(t, 120, 30)
	if strings.Contains(m.View(), splitDivider) {
		t.Fatal("no divider expected below the breakpoint")
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.splitView() {
		t.Fatal("stacked detail must not use the split composition")
	}
}

func TestSplit_MouseRoutesByPane(t *testing.T) {
	m, _ := splitFixture(t, 160, 40)
	g := m.splitGeometry()
	paneX := g.listW + 5

	// Wheel over the pane scrolls the pane, not the list.
	pre := m.cursor
	model, _ := m.Update(tea.MouseMsg{X: paneX, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != pre {
		t.Fatalf("wheel over the pane must not move the list cursor; %d → %d", pre, m.cursor)
	}
	// Click on the pane focuses it.
	model, _ = m.Update(tea.MouseMsg{X: paneX, Y: 10, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("click on the pane should focus it; mode=%v", m.mode)
	}
	// Wheel over the list while the pane is focused hands focus back
	// and moves the cursor (the pane follows).
	model, _ = m.Update(tea.MouseMsg{X: 3, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.mode != modeList {
		t.Fatalf("wheel over the list should return focus; mode=%v", m.mode)
	}
	if m.cursor != pre+1 {
		t.Fatalf("wheel over the list should move the cursor; %d → %d", pre, m.cursor)
	}
	if m.detailIssue.ID != m.visible[m.cursor].ID {
		t.Fatalf("pane should follow the wheel; pane=%q cursor=%q", m.detailIssue.ID, m.visible[m.cursor].ID)
	}
}

func TestSplit_FooterShowsPaneKeysWhenFocused(t *testing.T) {
	m, _ := splitFixture(t, 160, 40)
	listKeys := m.footerBindings()
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	paneKeys := m.footerBindings()
	if len(paneKeys) == 0 || paneKeys[0].Help().Key != "esc" {
		t.Fatalf("pane-focused footer should lead with esc; got %v", paneKeys)
	}
	if len(listKeys) == len(paneKeys) && listKeys[0].Help().Key == paneKeys[0].Help().Key {
		t.Fatal("footer bindings should differ between list and pane focus")
	}
	if !strings.Contains(m.View(), "esc back") {
		t.Error("status bar should render the pane bindings while the pane is focused")
	}
}

func TestSplit_LayoutPrefRoundTripsThroughSession(t *testing.T) {
	m := New(&stubSource{})
	m.layoutPref = layoutStacked
	m.sessionPath = t.TempDir() + "/state.json"
	m.persistSession()
	st, err := LoadSession(m.sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Layout != "stacked" {
		t.Fatalf("persisted layout %q, want stacked", st.Layout)
	}
	restored := New(&stubSource{}).WithSession(st, "")
	if restored.layoutPref != layoutStacked {
		t.Fatalf("restored pref %v, want stacked", restored.layoutPref)
	}
	if layoutPrefFromLabel("garbage") != layoutAuto {
		t.Error("unknown label must decode to auto")
	}
}

func TestSplit_PaneClearsWhenListEmpties(t *testing.T) {
	m, src := splitFixture(t, 160, 40)
	if m.detailIssue.ID == "" {
		t.Fatal("precondition: pane should be staged")
	}
	src.issues = nil
	m = applyFetched(m, src)
	if m.detailIssue.ID != "" {
		t.Fatalf("pane should clear when the list empties; still showing %q", m.detailIssue.ID)
	}
	if !strings.Contains(m.View(), "no issue selected") {
		t.Error("empty pane should say so")
	}
}
