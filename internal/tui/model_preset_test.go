package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// This file holds preset switching and the help overlay.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

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

func TestJumpToHuman_AdvancesAndWraps(t *testing.T) {
	// Sample has human at indices 0 and 2, src:agent (non-human) at 1.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// from cursor=0 (human), ] should jump to index 2 (next human).
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = model.(Model)
	if m.cursor != 2 {
		t.Errorf("] from human@0 should land on human@2; got cursor=%d", m.cursor)
	}

	// from cursor=2 (last human), ] should wrap to index 0.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("] should wrap to first human; got cursor=%d", m.cursor)
	}

	// [ from cursor=0 should wrap to last human at index 2.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = model.(Model)
	if m.cursor != 2 {
		t.Errorf("[ from human@0 should wrap to human@2; got cursor=%d", m.cursor)
	}
}

func TestJumpToHuman_NoneVisibleSetsBanner(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "x-1", Title: "no human one", Labels: []string{}},
		{ID: "x-2", Title: "no human two", Labels: []string{}},
	}}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = model.(Model)
	if !strings.Contains(m.status, "no human") {
		t.Errorf("expected 'no human' status banner; got %q", m.status)
	}
}

func TestHelpOverlay_OpensAndRestoresMode(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// open help from list
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	if m.mode != modeHelp {
		t.Fatalf("? should switch to modeHelp; got %v", m.mode)
	}
	if !strings.Contains(m.View(), "Keys") {
		t.Error("help view should render the title 'Keys'")
	}
	if !strings.Contains(m.View(), "next human") {
		t.Error("help view should list the bracket-navigation binding")
	}

	// esc returns to modeList
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("esc should return to modeList; got %v", m.mode)
	}
}

func TestHelpOverlay_FromDetailReturnsToDetail(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// enter detail
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: expected modeDetail, got %v", m.mode)
	}

	// ? opens help; esc must restore detail (not list)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("help opened from detail should restore to detail; got %v", m.mode)
	}
}

func TestHelpOverlay_IncludesStatusLegend(t *testing.T) {
	// The legend lives alongside the keybindings so a new user
	// can read it without leaving the TUI. Pin presence of the
	// section header + each status row so a future viewHelp
	// refactor doesn't silently drop the legend.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	out := m.viewHelp()

	for _, want := range []string{
		"Status column",
		"open",
		"wip",
		"blocked",
		"deferred",
		"closed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay should contain %q in the status legend; got\n%s", want, out)
		}
	}
}
