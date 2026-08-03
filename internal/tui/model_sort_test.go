package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the sort axes and their cycling.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestSortCycle_RotatesThroughKeys(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "a", Priority: 2},
		{ID: "a-2", Title: "b", Priority: 0},
		{ID: "a-3", Title: "c", Priority: 1},
	}}
	m := applyFetched(New(src), src)

	// Default: no sort, bd's native order preserved.
	if m.visible[0].ID != "a-1" {
		t.Errorf("default sort should preserve order; got %q first", m.visible[0].ID)
	}

	// Press s → priority asc.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if m.sortBy != sortPriority {
		t.Errorf("first s should set sortPriority; got %v", m.sortBy)
	}
	if m.visible[0].Priority != 0 {
		t.Errorf("priority sort should put P0 first; got priority=%d", m.visible[0].Priority)
	}

	// Press s five more times → updated → repo → id → deps → none.
	for i := 0; i < 5; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = model.(Model)
	}
	if m.sortBy != sortNone {
		t.Errorf("cycle should return to sortNone after 6 presses; got %v", m.sortBy)
	}
}

func TestSortDeps_OrdersByDependencyCount(t *testing.T) {
	// DependencyCount-based ordering: 0-dep rows at top, then
	// 1-dep, then 2-dep. Tiebreak within a level by Priority
	// then ID — pin the deterministic order so future refactors
	// can't silently flip it.
	in := []beads.Issue{
		{ID: "a-3", Priority: 2, DependencyCount: 0},
		{ID: "a-2", Priority: 1, DependencyCount: 2},
		{ID: "a-1", Priority: 0, DependencyCount: 0},
		{ID: "a-4", Priority: 3, DependencyCount: 1},
		{ID: "a-5", Priority: 1, DependencyCount: 1},
	}
	// nil depCache → the deps sort degrades to the DependencyCount
	// level proxy this test pins.
	applySort(in, sortDeps, false, nil)
	wantIDs := []string{"a-1", "a-3", "a-5", "a-4", "a-2"}
	// a-1 (0 deps, P0) before a-3 (0 deps, P2) — Priority tiebreak.
	// a-5 (1 dep, P1) before a-4 (1 dep, P3) — Priority tiebreak.
	// a-2 (2 deps) last.
	for i, want := range wantIDs {
		if in[i].ID != want {
			t.Errorf("position %d: got %q, want %q (full order: %+v)", i, in[i].ID, want, idsOfIssues(in))
		}
	}
}

func TestSortDeps_ReverseFlipsOrder(t *testing.T) {
	in := []beads.Issue{
		{ID: "leaf", Priority: 0, DependencyCount: 0},
		{ID: "deep", Priority: 0, DependencyCount: 5},
	}
	applySort(in, sortDeps, true, nil)
	if in[0].ID != "deep" || in[1].ID != "leaf" {
		t.Errorf("reversed sortDeps should put deepest first; got %v", idsOfIssues(in))
	}
}

func TestByteToRuneIdxs_HandlesMultiByteRunes(t *testing.T) {
	// "café" — c(0), a(1), f(2), é(3,4 in bytes). A match on
	// byte offsets {0, 3} (c and é) should map to rune indices
	// {0, 3}.
	got := byteToRuneIdxs("café", []int{0, 3})
	want := []int{0, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("byteToRuneIdxs = %v, want %v", got, want)
	}
}

func TestByteToRuneIdxs_EmptyInput(t *testing.T) {
	if got := byteToRuneIdxs("anything", nil); got != nil {
		t.Errorf("nil byteIdxs should return nil; got %v", got)
	}
}

func TestSortReverse_FlipsActiveAxisDirection(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
		{ID: "a-2", Priority: 0},
		{ID: "a-3", Priority: 1},
	}}
	m := applyFetched(New(src), src)

	// Press s → priority ascending (P0 first).
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if m.visible[0].Priority != 0 {
		t.Fatalf("setup: expected P0 first; got %d", m.visible[0].Priority)
	}

	// Press S → reverse to descending (P2 first).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	m = model.(Model)
	if !m.sortDesc {
		t.Errorf("S should set sortDesc=true; got %v", m.sortDesc)
	}
	if m.visible[0].Priority != 2 {
		t.Errorf("reverse should put P2 first; got %d", m.visible[0].Priority)
	}

	// Switching axis (press s) should reset direction to natural.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if m.sortDesc {
		t.Errorf("axis change should reset sortDesc; got %v", m.sortDesc)
	}
}

func TestSortReverse_NoOpWhenNoSortActive(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	m = model.(Model)
	if m.sortDesc {
		t.Errorf("S with no active sort should NOT flip direction; got sortDesc=%v", m.sortDesc)
	}
	if !strings.Contains(m.status, "pick a sort first") {
		t.Errorf("status should hint at the missing sort; got %q", m.status)
	}
}

func TestSortByDeps_ChainRespectsEdges(t *testing.T) {
	// A→B→C dependency chain (C depends on B, B depends on A) plus
	// two unrelated rows, with mixed priorities. The topo order must
	// place A before B before C regardless of priority.
	rows := []beads.Issue{
		{ID: "a-c", Priority: 0, DependencyCount: 1},
		{ID: "a-b", Priority: 4, DependencyCount: 1},
		{ID: "a-a", Priority: 3, DependencyCount: 0},
		{ID: "a-x", Priority: 1, DependencyCount: 0},
		{ID: "a-y", Priority: 2, DependencyCount: 0},
	}
	src := &stubDepSource{
		stubSource: stubSource{issues: rows},
		edges: map[string][]string{
			"a-b": {"a-a"},
			"a-c": {"a-b"},
		},
	}
	m := New(src)
	m.all = rows
	m.recomputeVisible()
	m = resolveDepsForTest(t, m)

	pos := map[string]int{}
	for i, iss := range m.visible {
		pos[iss.ID] = i
	}
	if len(m.visible) != len(rows) {
		t.Fatalf("expected %d visible rows, got %d", len(rows), len(m.visible))
	}
	if pos["a-a"] >= pos["a-b"] || pos["a-b"] >= pos["a-c"] {
		t.Errorf("chain order violated: a-a=%d a-b=%d a-c=%d (want a<b<c)", pos["a-a"], pos["a-b"], pos["a-c"])
	}
}

func TestSortByDeps_CycleDoesNotCrashEmitsAll(t *testing.T) {
	// A↔B mutual dependency (a cycle) plus a clean root. The sort
	// must not spin or panic, and every node must appear exactly
	// once in the output.
	rows := []beads.Issue{
		{ID: "a-a", Priority: 1, DependencyCount: 1},
		{ID: "a-b", Priority: 0, DependencyCount: 1},
		{ID: "a-r", Priority: 2, DependencyCount: 0},
	}
	src := &stubDepSource{
		stubSource: stubSource{issues: rows},
		edges: map[string][]string{
			"a-a": {"a-b"},
			"a-b": {"a-a"},
		},
	}
	m := New(src)
	m.all = rows
	m.recomputeVisible()
	m = resolveDepsForTest(t, m)

	if len(m.visible) != len(rows) {
		t.Fatalf("cycle: expected all %d nodes emitted, got %d", len(rows), len(m.visible))
	}
	seen := map[string]int{}
	for _, iss := range m.visible {
		seen[iss.ID]++
	}
	for _, id := range []string{"a-a", "a-b", "a-r"} {
		if seen[id] != 1 {
			t.Errorf("cycle: node %s emitted %d times, want 1", id, seen[id])
		}
	}
}

func TestSortByDeps_OffScreenDepTreatedAsRoot(t *testing.T) {
	// a-1 depends on a-99 which is NOT in the visible set. That edge
	// must be treated as already satisfied, so a-1 sorts as a root
	// alongside the other zero-in-degree rows.
	rows := []beads.Issue{
		{ID: "a-1", Priority: 0, DependencyCount: 1},
		{ID: "a-2", Priority: 1, DependencyCount: 1},
		{ID: "a-3", Priority: 2, DependencyCount: 0},
	}
	src := &stubDepSource{
		stubSource: stubSource{issues: rows},
		edges: map[string][]string{
			"a-1": {"a-99"}, // off-screen
			"a-2": {"a-1"},  // on-screen, real edge
		},
	}
	m := New(src)
	m.all = rows
	m.recomputeVisible()
	m = resolveDepsForTest(t, m)

	pos := map[string]int{}
	for i, iss := range m.visible {
		pos[iss.ID] = i
	}
	// a-1's only dep is off-screen → it's a root; a-2 depends on a-1
	// (on-screen) so it must come after a-1.
	if pos["a-1"] >= pos["a-2"] {
		t.Errorf("off-screen dep not satisfied: a-1=%d should precede a-2=%d", pos["a-1"], pos["a-2"])
	}
	// a-1 and a-3 are both roots; the lower-priority a-1 (P0) sorts
	// ahead of a-3 (P2) by the within-level tiebreak.
	if pos["a-1"] >= pos["a-3"] {
		t.Errorf("root tiebreak: a-1 (P0) should precede a-3 (P2), got a-1=%d a-3=%d", pos["a-1"], pos["a-3"])
	}
}

func TestSortByDeps_FallbackToCountProxyWhenUnresolved(t *testing.T) {
	// With no DepLister wired, the deps sort can't resolve edges and
	// must degrade to the DependencyCount level proxy rather than
	// crash or block. (stubSource does not implement DepLister.)
	rows := []beads.Issue{
		{ID: "a-1", Priority: 0, DependencyCount: 2},
		{ID: "a-2", Priority: 3, DependencyCount: 0},
		{ID: "a-3", Priority: 1, DependencyCount: 1},
	}
	m := New(&stubSource{issues: rows})
	if m.depLister != nil {
		t.Fatalf("stubSource should not satisfy DepLister")
	}
	m.all = rows
	m.recomputeVisible()
	m = pressSortToDeps(m)
	// Count proxy: level 0 (a-2) then level 1 (a-3) then level 2 (a-1).
	want := []string{"a-2", "a-3", "a-1"}
	for i, id := range want {
		if m.visible[i].ID != id {
			t.Errorf("fallback row %d: want %s, got %s", i, id, m.visible[i].ID)
		}
	}
}

func TestSortByDeps_RefreshReResolvesNewRows(t *testing.T) {
	// Regression for the silent revert-to-count-proxy bug: with the
	// deps sort active and resolved, an auto-refresh that introduces a
	// new dependent row must re-kick resolution from the fetchedMsg
	// handler so the real topological order is restored — not left
	// degraded until the user re-presses `s`.
	rows := []beads.Issue{
		{ID: "a-1", Priority: 0, DependencyCount: 0},
		{ID: "a-2", Priority: 1, DependencyCount: 1},
	}
	src := &stubDepSource{
		stubSource: stubSource{issues: rows},
		edges:      map[string][]string{"a-2": {"a-1"}},
	}
	m := New(src)
	m.all = rows
	m.recomputeVisible()
	m = resolveDepsForTest(t, m)

	// Refresh brings a-3, which depends on a-2 (edge added to source).
	src.edges["a-3"] = []string{"a-2"}
	newRows := []beads.Issue{
		{ID: "a-1", Priority: 0, DependencyCount: 0},
		{ID: "a-2", Priority: 1, DependencyCount: 1},
		{ID: "a-3", Priority: 0, DependencyCount: 1},
	}
	model, cmd := m.Update(fetchedMsg{preset: m.preset, issues: newRows})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("refresh while deps-sort active should return a resolve Cmd for the new row")
	}
	m = applyResolveCmd(t, m, cmd)

	pos := map[string]int{}
	for i, iss := range m.visible {
		pos[iss.ID] = i
	}
	if pos["a-1"] >= pos["a-2"] || pos["a-2"] >= pos["a-3"] {
		t.Errorf("topo order not restored after refresh: a-1=%d a-2=%d a-3=%d", pos["a-1"], pos["a-2"], pos["a-3"])
	}
}

func TestSortByDeps_DuplicateIDKeepsAllRows(t *testing.T) {
	// Two visible rows share a bare ID (a cross-repo collision). The
	// topo sort must neither drop a row nor leave a stale duplicate in
	// the tail: every input row survives exactly once.
	issues := []beads.Issue{
		{ID: "x-1", Repo: "a", Priority: 0},
		{ID: "x-2", Repo: "a", Priority: 1, DependencyCount: 1},
		{ID: "x-1", Repo: "b", Priority: 2}, // collision on x-1
	}
	depCache := map[string][]beads.Issue{
		"x-1": {},
		"x-2": {{ID: "x-1"}},
	}
	sortByDeps(issues, depCache)
	if len(issues) != 3 {
		t.Fatalf("row count changed: got %d, want 3", len(issues))
	}
	// IDs collide, so count by Repo to prove no row was lost/duplicated.
	repos := map[string]int{}
	for _, iss := range issues {
		repos[iss.Repo]++
	}
	if repos["a"] != 2 || repos["b"] != 1 {
		t.Errorf("rows lost/duplicated on collision: repo counts %v, want a=2 b=1", repos)
	}
}
