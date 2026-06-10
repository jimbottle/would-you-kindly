package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// Rework of PR #18's behaviors onto the preset-tagged fetchedMsg
// architecture (would-you-kindly-tjiy).

func TestHumanKey_TogglesBackToOriginPreset(t *testing.T) {
	src := &stubSource{issues: manyIssues(5)}
	m := applyFetched(New(src), src)

	// Start from blocked, jump into human, toggle back out.
	model, _ := m.switchPreset(filter.PresetBlocked)
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if m.preset != filter.PresetHuman {
		t.Fatalf("h should enter the human view; got %v", m.preset)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if m.preset != filter.PresetBlocked {
		t.Errorf("second h should return to the origin preset (blocked); got %v", m.preset)
	}
}

func TestHumanKey_ToggleFallsBackToAll(t *testing.T) {
	// Launching directly into human (wyk -preset human) has no
	// recorded origin: the toggle must land on all, not strand the
	// user.
	src := &stubSource{issues: manyIssues(3)}
	m := applyFetched(New(src).WithPreset(filter.PresetHuman), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if m.preset != filter.PresetAll {
		t.Errorf("h from human with no origin should land on all; got %v", m.preset)
	}
}

func TestSwitchPreset_PaintsCachedRowsInstantly(t *testing.T) {
	src := &stubSource{issues: manyIssues(4)}
	m := applyFetched(New(src), src) // seeds presetCache[all]

	// Land a human-preset fetch so its rows are cached too.
	humanRows := []beads.Issue{{ID: "h-1", Title: "human row"}}
	model, _ := m.switchPreset(filter.PresetHuman)
	m = model.(Model)
	model, _ = m.Update(fetchedMsg{preset: filter.PresetHuman, issues: humanRows})
	m = model.(Model)

	// Back to all: the cached all-rows must paint on THIS frame,
	// before any fetch returns.
	model, _ = m.switchPreset(filter.PresetAll)
	m = model.(Model)
	if len(m.visible) != 4 {
		t.Errorf("switch to all should instantly paint the 4 cached rows; visible=%d", len(m.visible))
	}
	// And back to human: its single cached row paints instantly.
	model, _ = m.switchPreset(filter.PresetHuman)
	m = model.(Model)
	if len(m.visible) != 1 || m.visible[0].ID != "h-1" {
		t.Errorf("switch to human should instantly paint its cached row; got %v", idsOf(m.visible))
	}
	if !m.refreshing {
		t.Error("instant paint must still dispatch the reconciling fetch (refreshing=true)")
	}
}

func TestToggleShowClosed_DropsPresetCache(t *testing.T) {
	// Cached rows were fetched under the old closed-scope; keeping
	// them would paint the wrong set after C.
	src := &stubSource{issues: manyIssues(3)}
	m := applyFetched(New(src), src)
	if len(m.presetCache) == 0 {
		t.Fatal("setup: fetch should seed the preset cache")
	}
	model, _ := m.toggleShowClosed()
	m = model.(Model)
	if m.presetCache != nil {
		t.Error("C must drop the preset cache (stale closed-scope)")
	}
}

func TestWithCacheSnapshot_SeedsAcrossPresets(t *testing.T) {
	// Quit on all, relaunch restoring human: the all-superset
	// snapshot must seed the human view via the in-memory filter
	// instead of cold-starting into ~9s of spinner (PR #18's
	// 3a0934a, reworked).
	snap := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now(),
		Issues: []beads.Issue{
			{ID: "a-1", Title: "agent row", Status: "open"},
			{ID: "h-1", Title: "human row", Status: "open", Labels: []string{"human"}},
			{ID: "b-1", Title: "blocked row", Status: "blocked"},
		},
	}
	src := &stubSource{}
	m := New(src).WithPreset(filter.PresetHuman).WithCacheSnapshot(snap, "")
	if len(m.all) != 1 || m.all[0].ID != "h-1" {
		t.Errorf("human view should seed with just the human-labeled row; got %v", idsOf(m.all))
	}

	// blocked works the same way…
	m = New(src).WithPreset(filter.PresetBlocked).WithCacheSnapshot(snap, "")
	if len(m.all) != 1 || m.all[0].ID != "b-1" {
		t.Errorf("blocked view should seed with the blocked row; got %v", idsOf(m.all))
	}

	// …ready cannot be approximated (bd computes dep-readiness):
	// no seed, cold start.
	m = New(src).WithPreset(filter.PresetReady).WithCacheSnapshot(snap, "")
	if len(m.all) != 0 {
		t.Errorf("ready view must not seed from a field-predicate filter; got %v", idsOf(m.all))
	}

	// …and a non-all snapshot can't reconstruct a different view.
	humanSnap := snap
	humanSnap.Preset = string(filter.PresetHuman)
	m = New(src).WithPreset(filter.PresetAll).WithCacheSnapshot(humanSnap, "")
	if len(m.all) != 0 {
		t.Errorf("a subset snapshot must not seed the all view; got %v", idsOf(m.all))
	}
}
