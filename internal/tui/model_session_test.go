package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// This file holds session persistence: warm start, cache snapshots, and the state
// written on quit.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestWithSession_HydratesPresetAndSort(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := New(src).WithSession(SessionState{
		Version:  sessionVersion,
		Preset:   "human",
		Sort:     "priority",
		SortDesc: true,
	}, "")
	if m.preset != filter.PresetHuman {
		t.Errorf("preset = %q, want human", m.preset)
	}
	if m.sortBy != sortPriority {
		t.Errorf("sortBy = %v, want sortPriority", m.sortBy)
	}
	if !m.sortDesc {
		t.Error("sortDesc should be restored alongside the sort axis")
	}
}

func TestWithSession_SortDescIgnoredWithoutAxis(t *testing.T) {
	// SortDesc must not apply when there's no sort axis to reverse —
	// an empty/unknown Sort leaves sortDesc at its default regardless
	// of the persisted bool, upholding the sortNone invariant.
	src := &stubSource{issues: sampleIssues()}
	m := New(src).WithSession(SessionState{Version: sessionVersion, Sort: "", SortDesc: true}, "")
	if m.sortBy != sortNone {
		t.Fatalf("sortBy = %v, want sortNone", m.sortBy)
	}
	if m.sortDesc {
		t.Error("sortDesc must stay false when no sort axis is restored")
	}
}

func TestQuit_PersistsSortDirection(t *testing.T) {
	// A reversed sort must survive a quit→restore so the user lands on
	// exactly the view they left (axis AND direction).
	path := filepath.Join(t.TempDir(), "state.json")
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src).WithSession(SessionState{Version: sessionVersion}, path), src)
	m.sortBy = sortPriority
	m.sortDesc = true

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}); cmd == nil {
		t.Fatal("q should quit")
	}
	got, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sort != "priority" || !got.SortDesc {
		t.Errorf("persisted sort = %q desc=%v, want priority desc=true", got.Sort, got.SortDesc)
	}
}

func TestWithSession_IgnoresUnknownPresetAndSort(t *testing.T) {
	// A bogus preset / sort (e.g. from a hand-mangled file or an older
	// wyk's vocabulary) must leave the defaults intact rather than
	// snap to PresetAll / sortNone, which would silently lose the
	// user's actual default view.
	src := &stubSource{issues: sampleIssues()}
	base := New(src)
	m := base.WithSession(SessionState{Preset: "nonsense", Sort: "nonsense"}, "")
	if m.preset != base.preset {
		t.Errorf("unknown preset changed it to %q, want unchanged %q", m.preset, base.preset)
	}
	if m.sortBy != base.sortBy {
		t.Errorf("unknown sort changed it to %v, want unchanged %v", m.sortBy, base.sortBy)
	}
}

func TestWithSession_RestoresCursorOnFetch(t *testing.T) {
	issues := []beads.Issue{{ID: "a-1"}, {ID: "a-2"}, {ID: "a-3"}}
	src := &stubSource{issues: issues}
	m := New(src).WithSession(SessionState{Version: sessionVersion, CursorID: "a-3"}, "")
	// Before the fetch the visible set is empty, so the cursor stays
	// staged — restoration happens once the rows land.
	m = applyFetched(m, src)
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (the saved a-3 row)", m.cursor)
	}
	// One-shot: a subsequent refresh must not re-snap the cursor if
	// the user has since moved it.
	m.cursor = 0
	m = applyFetched(m, src)
	if m.cursor != 0 {
		t.Errorf("cursor re-snapped to %d on refresh; restore should be one-shot", m.cursor)
	}
}

func TestWithSession_CursorFallsBackWhenIDGone(t *testing.T) {
	// The saved issue is closed / filtered out / deleted — restoring
	// must fall back to the top, never index out of range.
	issues := []beads.Issue{{ID: "a-1"}, {ID: "a-2"}}
	src := &stubSource{issues: issues}
	m := New(src).WithSession(SessionState{Version: sessionVersion, CursorID: "a-99"}, "")
	m = applyFetched(m, src)
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 fallback for a vanished saved ID", m.cursor)
	}
}

func TestQuit_PersistsSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	issues := []beads.Issue{{ID: "a-1"}, {ID: "a-2"}, {ID: "a-3"}}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src).WithSession(SessionState{Version: sessionVersion}, path), src)
	// Drive the model into a non-default state: cursor on a-2, sort by id.
	m.cursor = 1
	m.sortBy = sortID
	m.preset = filter.PresetHuman

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = model
	if cmd == nil {
		t.Fatal("q should return the tea.Quit command")
	}
	got, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession after quit: %v", err)
	}
	want := SessionState{Version: sessionVersion, Preset: "human", Sort: "id", CursorID: "a-2"}
	if got != want {
		t.Errorf("persisted session:\n got %+v\nwant %+v", got, want)
	}
}

func TestQuit_NoSessionPathIsNoOp(t *testing.T) {
	// An empty sessionPath (tests, read-only runs) must not panic or
	// write anywhere on quit.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src) // no WithSession → sessionPath==""
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}); cmd == nil {
		t.Error("q should still quit even with no session path")
	}
}

func TestUpdateNudge_RenderedAboveStatusBar(t *testing.T) {
	// When WithUpdateNudge is set, the model renders the nudge
	// line above the status bar. Pin both that it appears AND
	// that it does NOT appear when unset, so a future banner
	// shuffle can't accidentally hide it.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	nudge := "↑ wyk v0.99.0 available — run `wyk update`"
	m = m.WithUpdateNudge(nudge)
	out := m.View()
	if !strings.Contains(out, nudge) {
		t.Errorf("update nudge should render in the view; got:\n%s", out)
	}
}

func TestUpdateNudge_EmptyByDefault(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	out := m.View()
	if strings.Contains(out, "wyk update") || strings.Contains(out, "available — run") {
		t.Errorf("update nudge should NOT render when unset; got:\n%s", out)
	}
}

func TestWithCacheSnapshot_PaintsCachedRowsImmediately(t *testing.T) {
	// User-visible payoff: opening wyk shouldn't blank-flash a
	// "loading…" line when there are cached rows we can paint
	// right away. WithCacheSnapshot pre-populates m.all so the
	// view's "len(m.all) > 0" branch fires on the first frame,
	// while m.loading is still true and the live fetch is in
	// flight.
	src := &stubSource{}
	cache := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now().Add(-30 * time.Minute),
		Issues:  []beads.Issue{{ID: "wyk-1", Title: "from cache"}},
	}
	m := New(src).WithCacheSnapshot(cache, "")
	if len(m.all) != 1 || m.all[0].ID != "wyk-1" {
		t.Errorf("WithCacheSnapshot should seed m.all; got %+v", m.all)
	}
	if !m.cacheStale {
		t.Error("cacheStale should be true while m.all is sourced from cache")
	}
	if m.cacheSavedAt.IsZero() {
		t.Error("cacheSavedAt should reflect the seeded snapshot time")
	}
	if !m.loading {
		t.Error("m.loading should remain true until the live fetch lands — the cache seed doesn't replace it")
	}
}

func TestWithCacheSnapshot_IgnoresMismatchedPreset(t *testing.T) {
	// An "all" snapshot now seeds a mismatched preset through the
	// in-memory filter (TestWithCacheSnapshot_SeedsAcrossPresets) —
	// but when the filtered approximation is EMPTY, as here (no
	// human-labeled rows in the snapshot), there's no value in
	// pre-painting an empty list: the cold-path "loading…"
	// experience stays.
	src := &stubSource{}
	cache := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now(),
		Issues:  []beads.Issue{{ID: "wyk-1"}},
	}
	m := New(src).WithPreset(filter.PresetHuman).WithCacheSnapshot(cache, "")
	if len(m.all) != 0 {
		t.Errorf("mismatched preset should not seed; got %+v", m.all)
	}
	if m.cacheStale {
		t.Error("cacheStale should remain false on mismatch")
	}
}
