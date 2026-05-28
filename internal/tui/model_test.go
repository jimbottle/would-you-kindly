package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// stubSource lets tests fix the Fetch result.
type stubSource struct {
	issues []beads.Issue
	err    error
	calls  int
	last   filter.Preset
}

func (s *stubSource) Fetch(_ context.Context, p filter.Preset) ([]beads.Issue, error) {
	s.calls++
	s.last = p
	return s.issues, s.err
}

func sampleIssues() []beads.Issue {
	return []beads.Issue{
		{ID: "a-1", Title: "rotate password", Status: "open", Priority: 1,
			Labels: []string{"human", "src:agent"}, Description: "step one\nstep two"},
		{ID: "a-2", Title: "deploy preview", Status: "in_progress", Priority: 2,
			Labels: []string{"src:agent"}, Description: "no human needed"},
		{ID: "a-3", Title: "approve release", Status: "open", Priority: 1,
			Labels: []string{"human", "src:agent"}, Description: "click publish"},
	}
}

// applyFetched simulates the first fetch completing under the model's
// current preset. The preset tag matters: the model now drops results
// for any preset other than the one currently selected, so tests must
// echo m.preset back into the message.
func applyFetched(m Model, src *stubSource) Model {
	model, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	return model.(Model)
}

func TestHumanKeyJumpsToHumanPreset(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if m.preset != filter.PresetHuman {
		t.Errorf("preset = %q, want %q", m.preset, filter.PresetHuman)
	}
	if cmd == nil {
		t.Error("expected a fetch command after pressing h")
	}
}

func TestTabCyclesPresets(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	first := m.preset
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = model.(Model)
	if m.preset == first {
		t.Errorf("tab did not advance preset; still %q", m.preset)
	}
}

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

func TestErrorStateShowsFriendlyMessage(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: src.err})
	out := model.(Model).View()
	if !strings.Contains(out, "bd is not installed") {
		t.Errorf("error view missing friendly bd-not-installed copy:\n%s", out)
	}
}

func TestStaleFetchIsDroppedAfterPresetChange(t *testing.T) {
	// A tick fires while the user is on the default preset, then the
	// user switches to PresetHuman before the fetch returns. The late
	// fetched message must not overwrite the model's state.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// switch to human, model.all clears
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if len(m.all) != 0 {
		t.Fatalf("preset switch should clear m.all, got %d", len(m.all))
	}

	// late fetch for the OLD preset arrives
	stale := []beads.Issue{{ID: "stale-1", Title: "stale", Labels: []string{}}}
	model, _ = m.Update(fetchedMsg{preset: filter.PresetAll, issues: stale})
	m = model.(Model)
	if len(m.all) != 0 {
		t.Errorf("stale fetch should have been dropped; m.all = %+v", m.all)
	}
}

func TestTickSuspendsOnTerminalError(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)
	_, cmd := m.Update(tickMsg{gen: m.tickGen})
	if cmd != nil {
		t.Error("tick should not re-arm while error state is terminal")
	}
}

func TestCtrlCQuitsFromFilterPrompt(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// open the / prompt
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("setup: expected modeFilter, got %v", m.mode)
	}

	// ctrl+c at the prompt must produce tea.Quit, not be absorbed by textinput.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in / prompt should return a command")
	}
	if got := cmd(); got != tea.Quit() {
		t.Errorf("ctrl+c in / prompt should produce tea.Quit, got %T", got)
	}
}

func TestSwitchPresetClearsRowsAndShowsLoading(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)

	if len(m.all) != 0 || len(m.visible) != 0 {
		t.Errorf("switchPreset should clear all/visible; got all=%d visible=%d",
			len(m.all), len(m.visible))
	}
	if m.cursor != 0 {
		t.Errorf("switchPreset should reset cursor; got %d", m.cursor)
	}
	if !m.loading {
		t.Error("switchPreset should set loading=true")
	}
	if !strings.Contains(m.View(), "loading…") {
		t.Error("view should render the loading indicator during a preset switch")
	}
}

func TestRefreshAfterTerminalErrorRestartsTickAndRetiresOldChain(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	// land a terminal error and let the tick handler retire the current chain.
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)
	model, _ = m.Update(tickMsg{gen: m.tickGen})
	m = model.(Model)
	preGen := m.tickGen

	// manual refresh from the error state: tickGen bumps and a new tick is scheduled.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(Model)
	if m.tickGen <= preGen {
		t.Errorf("refresh from terminal error should bump tickGen (was %d, now %d)",
			preGen, m.tickGen)
	}
	if cmd == nil {
		t.Error("refresh from terminal error should produce a batched command")
	}

	// a tick from the OLD generation must be dropped — it would otherwise
	// re-arm and yield duplicate tick chains forever.
	_, staleCmd := m.Update(tickMsg{gen: preGen})
	if staleCmd != nil {
		t.Error("stale-gen tick should be dropped, not re-arm a chain")
	}
}
