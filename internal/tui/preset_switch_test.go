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

func TestPresetCache_ImmuneToOptimisticMutation(t *testing.T) {
	// The cache must hold its own copies: optimisticListUpdate's close
	// path shifts m.all's backing array in place, and a cache entry
	// aliasing it would replay a corrupted slice (duplicated last row)
	// on the next instant paint (roborev #2062). fetch → optimistic
	// close → switch away and back → exactly the un-closed rows, no
	// duplicates.
	src := &stubSource{issues: manyIssues(4)}
	m := applyFetched(New(src), src) // caches the 4 all-rows

	victim := m.all[1]
	m.optimisticListUpdate("close", victim)
	if len(m.all) != 3 {
		t.Fatalf("setup: optimistic close should drop the row from m.all; len=%d", len(m.all))
	}

	// Away and back: the instant paint must be the pristine 4-row
	// fetch result, not the shifted array.
	model, _ := m.switchPreset(filter.PresetHuman)
	m = model.(Model)
	model, _ = m.switchPreset(filter.PresetAll)
	m = model.(Model)
	seen := map[string]int{}
	for _, i := range m.all {
		seen[i.ID]++
		if seen[i.ID] > 1 {
			t.Fatalf("duplicated row %q after cache paint — cache aliased the mutated array; rows: %v", i.ID, idsOf(m.all))
		}
	}
	if len(m.all) != 4 {
		t.Errorf("cache paint should restore the pristine 4-row fetch; got %v", idsOf(m.all))
	}
}

func TestWithCacheSnapshot_MinePresetAndClosedRows(t *testing.T) {
	// The mine predicate's two branches, both closed-filtered: the
	// snapshot may have been saved while showClosed was on, and
	// sessions relaunch closed-excluded (roborev #2061/#2062).
	snap := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now(),
		Issues: []beads.Issue{
			{ID: "m-1", Assignee: "ev", Status: "open"},
			{ID: "m-2", Assignee: "ev", Status: "closed"}, // closed: must not seed
			{ID: "o-1", Assignee: "someone-else", Status: "open"},
			{ID: "h-c", Status: "closed", Labels: []string{"human"}}, // closed human
		},
	}
	src := &stubSource{}

	// Identity set: only the open row assigned to me.
	m := New(src).WithMe("ev").WithPreset(filter.PresetMine).WithCacheSnapshot(snap, "")
	if len(m.all) != 1 || m.all[0].ID != "m-1" {
		t.Errorf("mine with identity should seed open assignee=ev rows only; got %v", idsOf(m.all))
	}

	// No identity: degrades to the non-closed set (the two open rows).
	m = New(src).WithPreset(filter.PresetMine).WithCacheSnapshot(snap, "")
	if len(m.all) != 2 {
		t.Errorf("mine without identity should seed the non-closed rows; got %v", idsOf(m.all))
	}

	// And the human predicate excludes the closed human row.
	m = New(src).WithPreset(filter.PresetHuman).WithCacheSnapshot(snap, "")
	if len(m.all) != 0 {
		t.Errorf("a closed human row must not seed the human view; got %v", idsOf(m.all))
	}
}
