package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds modeDetail: the single-issue reading view, its link cycling, and
// dependency resolution.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestDetailClose_ConfirmsAndDispatches(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := enterDetailWithMutator(t, s)

	// `a` opens the confirm prompt overlaid on the detail view.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.mode != modeConfirmClose {
		t.Fatalf("`a` in detail should enter modeConfirmClose; got %v", m.mode)
	}
	if m.promptReturn != modeDetail {
		t.Errorf("promptReturn should be modeDetail; got %v", m.promptReturn)
	}
	if cmd != nil || len(s.closed) != 0 {
		t.Error("`a` alone must not dispatch a close")
	}
	// The confirm prompt must render over the detail view, not the list.
	if !strings.Contains(m.View(), "close "+s.issues[0].ID+"?") {
		t.Errorf("detail view should show the close confirm prompt; got:\n%s", m.View())
	}

	// `y` dispatches the close and drops back to the list (the
	// just-closed issue leaves the open view).
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("confirmed close from detail should land in modeList; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("confirmed close must return a write command")
	}
	if wm := cmd().(writeMsg); wm.action != "close" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg action=%q id=%q", wm.action, wm.id)
	}
	if len(s.closed) != 1 || s.closed[0] != s.issues[0].ID {
		t.Errorf("expected Close(%q); got %+v", s.issues[0].ID, s.closed)
	}
}

func TestDetailClose_CancelReturnsToDetail(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := enterDetailWithMutator(t, s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	// Any non-y key cancels — and we land back in detail, not the list.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("cancelling a detail-opened close should return to detail; got %v", m.mode)
	}
	if m.promptReturn != modeList {
		t.Errorf("promptReturn should reset to modeList after cancel; got %v", m.promptReturn)
	}
	if len(s.closed) != 0 {
		t.Errorf("cancel must not close; got %+v", s.closed)
	}
}

func TestDetailClose_ReopensWhenClosed(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := enterDetailWithMutator(t, s)
	// Simulate viewing a closed issue's detail (reachable via show-closed).
	m.detailIssue.Status = "closed"

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("reopen is immediate — mode should stay modeDetail; got %v", m.mode)
	}
	if m.detailIssue.Status != "open" {
		t.Errorf("reopen should optimistically flip status to open; got %q", m.detailIssue.Status)
	}
	if cmd == nil {
		t.Fatal("`a` on a closed issue should dispatch a reopen")
	}
	if wm := cmd().(writeMsg); wm.action != "reopen" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg action=%q id=%q, want reopen", wm.action, wm.id)
	}
	if len(s.reopened) != 1 || s.reopened[0] != s.issues[0].ID {
		t.Errorf("expected Reopen(%q); got %+v", s.issues[0].ID, s.reopened)
	}
}

func TestDetailDefer_DispatchesAndReturnsToDetail(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := enterDetailWithMutator(t, s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = model.(Model)
	if m.mode != modeDefer || m.promptReturn != modeDetail {
		t.Fatalf("`d` in detail should enter modeDefer with promptReturn=detail; got mode=%v ret=%v", m.mode, m.promptReturn)
	}
	if m.pendingTarget.ID != s.issues[0].ID {
		t.Errorf("defer should target the detail issue; got %q", m.pendingTarget.ID)
	}
	for _, r := range "+1d" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("defer from detail should return to detail; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("enter with a value should dispatch SetDefer")
	}
	if wm := cmd().(writeMsg); wm.action != "defer" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg action=%q id=%q", wm.action, wm.id)
	}
	if len(s.deferred) != 1 || s.deferred[0] != (labelOp{s.issues[0].ID, "+1d"}) {
		t.Errorf("SetDefer not dispatched correctly; got %+v", s.deferred)
	}
}

func TestDetailNote_DispatchesAppendsAndReturnsToDetail(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := enterDetailWithMutator(t, s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	if m.mode != modeNote || m.promptReturn != modeDetail {
		t.Fatalf("`n` in detail should enter modeNote with promptReturn=detail; got mode=%v ret=%v", m.mode, m.promptReturn)
	}
	// The textarea overlay renders over the detail view (not the list).
	if !strings.Contains(m.View(), "ctrl+s save") {
		t.Errorf("detail note overlay should render the textarea hint; got:\n%s", m.View())
	}
	m.noteArea.SetValue("verified on staging")
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("note from detail should return to detail; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("ctrl+s with a note should dispatch a write")
	}
	if wm := cmd().(writeMsg); wm.action != "note" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg action=%q id=%q", wm.action, wm.id)
	}
	// Optimistic append so the new note shows without re-opening the row.
	if !strings.Contains(m.detailIssue.Notes, "verified on staging") {
		t.Errorf("note should be optimistically appended to the detail body; Notes=%q", m.detailIssue.Notes)
	}
}

func TestDetailReopen_FailureRollsBackOptimisticStatus(t *testing.T) {
	// A failed reopen must restore Status="closed" so the detail body
	// doesn't contradict the error banner (which would otherwise show
	// an "open" issue with an "a: close" footer).
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}, reopenErr: errors.New("boom")}
	m := enterDetailWithMutator(t, s)
	m.detailIssue.Status = "closed"

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.detailIssue.Status != "open" {
		t.Fatalf("setup: reopen should optimistically flip to open; got %q", m.detailIssue.Status)
	}
	// Feed the real (failing) writeMsg back through Update.
	model, _ = m.Update(cmd().(writeMsg))
	m = model.(Model)
	if m.detailIssue.Status != "closed" {
		t.Errorf("failed reopen should roll the detail status back to closed; got %q", m.detailIssue.Status)
	}
	if !strings.Contains(m.status, "failed") {
		t.Errorf("failed reopen should surface a failure banner; got %q", m.status)
	}
}

func TestDetailNote_FailureRollsBackOptimisticAppend(t *testing.T) {
	// A failed note must strip the optimistically-appended body text
	// so the detail view doesn't show a phantom note.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}, noteErr: errors.New("boom")}
	m := enterDetailWithMutator(t, s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	m.noteArea.SetValue("phantom note")
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = model.(Model)
	if !strings.Contains(m.detailIssue.Notes, "phantom note") {
		t.Fatalf("setup: note should be optimistically appended; Notes=%q", m.detailIssue.Notes)
	}
	// Feed the real (failing) writeMsg back through Update.
	model, _ = m.Update(cmd().(writeMsg))
	m = model.(Model)
	if strings.Contains(m.detailIssue.Notes, "phantom note") {
		t.Errorf("failed note should roll the optimistic append back out; Notes=%q", m.detailIssue.Notes)
	}
	if !strings.Contains(m.status, "failed") {
		t.Errorf("failed note should surface a failure banner; got %q", m.status)
	}
}

func TestDetailWrites_ReadOnlyWithoutMutator(t *testing.T) {
	// A plain Source (no Mutator) must refuse the detail write keys
	// with a status hint and stay in detail.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: expected modeDetail, got %v", m.mode)
	}
	for _, r := range []rune{'a', 'd', 'n'} {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
		if m.mode != modeDetail {
			t.Errorf("%q without a mutator should stay in detail; got %v", string(r), m.mode)
		}
		if !strings.Contains(m.status, "read-only") {
			t.Errorf("%q without a mutator should set a read-only hint; got %q", string(r), m.status)
		}
	}
}

func TestDetailView_RendersNotesWhenPresent(t *testing.T) {
	// viewDetail should show a "notes" section when Notes is set,
	// hide it when Notes is empty. The detailIssue field (populated
	// via the Detail dispatch) is the source of truth.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = beads.Issue{
		ID:          "would-you-kindly-42",
		Title:       "do the thing",
		Description: "the description",
		Status:      "open",
		Notes:       "first note\nsecond note",
	}
	out := m.View()
	if !strings.Contains(out, "notes") {
		t.Errorf("detail view should render the 'notes' label when Notes is set; got:\n%s", out)
	}
	if !strings.Contains(out, "second note") {
		t.Errorf("detail view should include the notes content; got:\n%s", out)
	}

	m.detailIssue.Notes = ""
	out = m.View()
	// Lowercase the haystack to avoid the "n note" key hint matching.
	lower := strings.ToLower(out)
	if strings.Contains(lower, "\nnotes\n") {
		t.Errorf("detail view should NOT render notes section when Notes is empty; got:\n%s", out)
	}
}

func TestDetailBody_WrapsLongDescription(t *testing.T) {
	// Long single-line description must wrap to the viewport
	// width — without this, the detail view spills horizontally
	// off the right edge and the body is unreadable.
	long := strings.Repeat("word ", 30) // ~150 chars, well past 40-col wrap
	i := beads.Issue{Description: long}
	out, _ := detailBody(i, 40, nil, nil, false, false, -1)
	// Each rendered line must be ≤ 40 cells. lipgloss may emit
	// ANSI; strip the test of ANSI by checking each line as-is
	// since this body has no foreground styles applied to its
	// body content.
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line wider than 40 cells (%d): %q", w, line)
		}
	}
}

func TestDetailView_YankCopiesDescriptionAndNotes(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate", Description: "step one\nstep two", Notes: "first note"},
	}}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = m.visible[0]

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = model.(Model)
	want := "step one\nstep two\n\nfirst note"
	if copied != want {
		t.Errorf("c in detail mode copied %q, want %q", copied, want)
	}
	if !strings.Contains(m.status, "copied a-1 instructions") {
		t.Errorf("status should announce the copy; got %q", m.status)
	}
}

func TestDetailView_YankEmptyBodyNoOp(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "stub", Description: "", Notes: ""},
	}}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = m.visible[0]

	called := false
	orig := clipboardCopy
	clipboardCopy = func(s string) error { called = true; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if called {
		t.Error("empty body must not touch the clipboard")
	}
	if !strings.Contains(m.status, "nothing to copy") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestDetailBody_WidthZeroSkipsWrap(t *testing.T) {
	// Pre-WindowSizeMsg the viewport width is zero; the body
	// should still render (without wrap) so the first paint
	// shows something legible. The next paint with a real width
	// re-wraps correctly.
	i := beads.Issue{Description: "a very long line that should not be touched here"}
	out, _ := detailBody(i, 0, nil, nil, false, false, -1)
	if !strings.Contains(out, "a very long line that should not be touched here") {
		t.Errorf("width=0 should pass body through verbatim; got %q", out)
	}
}

func TestDetailBody_RendersDependencySections(t *testing.T) {
	// Both directions populated: each section renders its header plus
	// one `ID — title (status)` row per edge.
	i := beads.Issue{ID: "a-1", Description: "root"}
	deps := []beads.Issue{{ID: "a-2", Title: "needs this", Status: "open"}}
	dependents := []beads.Issue{{ID: "a-3", Title: "blocks that", Status: "in_progress"}}
	out, _ := detailBody(i, 80, deps, dependents, false, false, -1)
	for _, want := range []string{
		"dependencies", "a-2 — needs this (open)",
		"dependents", "a-3 — blocks that (in_progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail body missing %q; got:\n%s", want, out)
		}
	}
}

func TestDetailBody_EmptySectionsCollapse(t *testing.T) {
	// Zero rows and no error → the section (and its header) is omitted
	// entirely so the body doesn't sprout empty "dependencies" labels.
	i := beads.Issue{ID: "a-1", Description: "lonely"}
	out, _ := detailBody(i, 80, nil, nil, false, false, -1)
	if strings.Contains(out, "dependencies") || strings.Contains(out, "dependents") {
		t.Errorf("empty dep sections should collapse; got:\n%s", out)
	}
}

func TestDetailBody_FailedLookupShowsUnavailable(t *testing.T) {
	// A failed fetch renders the header plus a single unavailable
	// line so the body still flows (and the user knows it's an error,
	// not a genuinely empty set).
	i := beads.Issue{ID: "a-1", Description: "boom"}
	out, _ := detailBody(i, 80, nil, nil, true, true, -1)
	for _, want := range []string{
		"dependencies", "(deps unavailable)",
		"dependents", "(dependents unavailable)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail body missing %q on failed lookup; got:\n%s", want, out)
		}
	}
}

func TestResolveDetailDeps_NilWhenAlreadyCached(t *testing.T) {
	// Re-opening an issue whose both directions are already cached
	// must not re-shell `bd dep list` — resolveDetailDeps returns nil.
	src := &stubDepSource{
		stubSource: stubSource{issues: []beads.Issue{{ID: "a-1"}}},
		edges:      map[string][]string{"a-1": {"a-2"}},
	}
	m := New(src)
	m.depCache["a-1"] = nil
	m.dependentCache["a-1"] = nil
	if cmd := m.resolveDetailDeps("a-1"); cmd != nil {
		t.Error("both directions cached → resolveDetailDeps should return nil")
	}
}

func TestResolveDetailDeps_FetchesAndCaches(t *testing.T) {
	// Opening a fresh issue resolves both directions via the
	// DepLister and merges them into the per-ID caches.
	src := &stubDepSource{
		stubSource: stubSource{issues: []beads.Issue{{ID: "a-1"}, {ID: "a-2"}}},
		edges:      map[string][]string{"a-1": {"a-2"}},
	}
	m := New(src)
	cmd := m.resolveDetailDeps("a-1")
	if cmd == nil {
		t.Fatal("uncached issue should produce a resolve Cmd")
	}
	msg := cmd()
	model, _ := m.Update(msg)
	m = model.(Model)
	// Forward: a-1 depends on a-2.
	if got := m.depCache["a-1"]; len(got) != 1 || got[0].ID != "a-2" {
		t.Errorf("depCache[a-1] = %+v, want [a-2]", got)
	}
	// Reverse: a-2 is a dependent of a-1, so a-1's dependents... the
	// stub inverts edges, so a-1's dependents are nodes that depend on
	// a-1 (none here → empty, but cached so it won't re-fetch).
	if _, ok := m.dependentCache["a-1"]; !ok {
		t.Error("dependentCache[a-1] should be populated after resolve")
	}
}

func TestPatchDepCacheStatus_OnCloseMutation(t *testing.T) {
	// Closing an issue must update its cached (status) wherever it
	// appears as a dependency/dependent row, so re-opening a related
	// issue's detail shows "closed", not the stale "open". Covers the
	// case refreshDepCachesFromList can't: a closed issue leaves the
	// open-only list, so only the explicit patch corrects it.
	src := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(src), src)
	// Seed caches: issue a-1 depends on b-2 (open); b-2 is a dependent
	// of a-3. Both cached rows mention b-2 with the now-stale status.
	m.depCache["a-1"] = []beads.Issue{{ID: "b-2", Title: "blocker", Status: "open"}}
	m.dependentCache["a-3"] = []beads.Issue{{ID: "b-2", Title: "blocker", Status: "open"}}

	model, _ := m.Update(writeMsg{action: "close", id: "b-2"})
	m = model.(Model)

	if got := m.depCache["a-1"][0].Status; got != "closed" {
		t.Errorf("depCache row for b-2 = %q, want closed", got)
	}
	if got := m.dependentCache["a-3"][0].Status; got != "closed" {
		t.Errorf("dependentCache row for b-2 = %q, want closed", got)
	}
}

func TestPatchDepCacheStatus_NonStatusActionNoOp(t *testing.T) {
	// A non-status mutation (e.g. note) must NOT touch cached dep
	// statuses — statusForAction returns false for it.
	src := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(src), src)
	m.depCache["a-1"] = []beads.Issue{{ID: "b-2", Status: "open"}}

	model, _ := m.Update(writeMsg{action: "note", id: "b-2"})
	m = model.(Model)
	if got := m.depCache["a-1"][0].Status; got != "open" {
		t.Errorf("a non-status mutation should leave the cached status alone; got %q", got)
	}
}

func TestDetailMsg_PreservesBlockedByHumanBadge(t *testing.T) {
	// Regression: opening a HUMAN-BLOCK issue then having the Detail
	// (bd show) enrichment land must NOT flip the badge to AGENT. The
	// enriched issue from bd show has no BlockedByHuman (that flag is
	// computed only in Fetch's dep-scan), so the handler must carry it
	// forward from the opened row.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = beads.Issue{ID: "a-1", Title: "t", Labels: []string{"src:agent"}, BlockedByHuman: true}

	// Enriched issue (same ID, fuller body) WITHOUT BlockedByHuman.
	enriched := beads.Issue{ID: "a-1", Title: "t", Description: "full body", Labels: []string{"src:agent"}}
	model, _ := m.Update(detailMsg{issue: enriched})
	m = model.(Model)

	if !m.detailIssue.BlockedByHuman {
		t.Error("BlockedByHuman must survive Detail enrichment so the badge stays HUMAN-BLOCK")
	}
	if m.detailIssue.Description != "full body" {
		t.Error("the enriched body should still be adopted")
	}
	// And the badge itself must render HUMAN-BLOCK, not AGENT.
	if got := responsibilityBadgeFor(m.detailIssue); !strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("badge should be HUMAN-BLOCK; got %q", got)
	}
}

func TestDetailLinks_FlattensDepsThenDependents(t *testing.T) {
	m := detailWithLinks(t)
	links := m.detailLinks()
	got := []string{}
	for _, l := range links {
		got = append(got, l.ID)
	}
	want := []string{"a-2", "a-3", "a-9"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("detailLinks = %v, want %v", got, want)
	}
}

func TestCycleDetailLink_TabSelectsThenWraps(t *testing.T) {
	m := detailWithLinks(t)
	if m.detailLinkIdx != -1 {
		t.Fatalf("fresh detail should have no link selected; got %d", m.detailLinkIdx)
	}
	tab := func(mm Model) Model { model, _ := mm.Update(tea.KeyMsg{Type: tea.KeyTab}); return model.(Model) }
	m = tab(m)
	if m.detailLinkIdx != 0 {
		t.Errorf("first Tab should select link 0; got %d", m.detailLinkIdx)
	}
	m = tab(m)
	m = tab(m)
	if m.detailLinkIdx != 2 {
		t.Errorf("three Tabs should land on link 2; got %d", m.detailLinkIdx)
	}
	m = tab(m)
	if m.detailLinkIdx != 0 {
		t.Errorf("Tab past the end should wrap to 0; got %d", m.detailLinkIdx)
	}
}

func TestCycleDetailLink_ShiftTabFromNoneSelectsLast(t *testing.T) {
	m := detailWithLinks(t)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = model.(Model)
	if m.detailLinkIdx != 2 {
		t.Errorf("Shift+Tab from no-selection should land on the last link; got %d", m.detailLinkIdx)
	}
}

func TestDetailBack_PopsStackThenReturnsToList(t *testing.T) {
	m := detailWithLinks(t)
	// Simulate having drilled a-1 -> a-3.
	m.detailStack = []beads.Issue{{ID: "a-1", Title: "root"}}
	m.detailIssue = beads.Issue{ID: "a-3"}
	// First Esc pops back to a-1, still in detail mode.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.detailIssue.ID != "a-1" || len(m.detailStack) != 0 || m.mode != modeDetail {
		t.Fatalf("first Esc should pop to a-1 in detail mode; got issue=%q stack=%d mode=%v", m.detailIssue.ID, len(m.detailStack), m.mode)
	}
	// Second Esc (empty stack) leaves to the list.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("Esc with empty stack should return to the list; mode=%v", m.mode)
	}
}

func TestCycleDetailLink_ScrollsOffScreenLinkIntoView(t *testing.T) {
	// A short viewport with many links: Tabbing down to a link below
	// the fold must advance YOffset to bring it on screen. Pins the
	// ensureDetailLinkVisible scroll math (the one piece of new arith).
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = beads.Issue{ID: "a-1", Title: "root", Description: "body"}
	// 30 dependency links; a 6-row viewport so most are below the fold.
	deps := make([]beads.Issue, 30)
	for i := range deps {
		deps[i] = beads.Issue{ID: fmt.Sprintf("a-%02d", i), Title: "dep", Status: "open"}
	}
	m.depCache["a-1"] = deps
	m.detailVP.Width = 80
	m.detailVP.Height = 6
	m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
	m.detailVP.GotoTop()

	// Tab to the last link (index 29) — far below the fold.
	for i := 0; i < 30; i++ {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = model.(Model)
	}
	if m.detailLinkIdx != 29 {
		t.Fatalf("expected to land on link 29; got %d", m.detailLinkIdx)
	}
	if m.detailVP.YOffset == 0 {
		t.Errorf("cycling to an off-screen link should have scrolled the viewport; YOffset still 0")
	}
	// The selected line must now be within the visible window.
	_, selLine := m.renderDetailBodyWithLine(m.detailIssue)
	if selLine < m.detailVP.YOffset || selLine >= m.detailVP.YOffset+m.detailVP.Height {
		t.Errorf("selected line %d not in view [%d,%d)", selLine, m.detailVP.YOffset, m.detailVP.YOffset+m.detailVP.Height)
	}
}

func TestDetailBack_ReEnrichesPoppedParent(t *testing.T) {
	// Regression for the slim-parent race: drilling in can push a
	// not-yet-enriched parent (Enter before its detailMsg landed). On
	// pop we must re-dispatch Detail() so its notes come back rather
	// than stranding a body-less parent.
	src := &stubDetailSource{stubSource: stubSource{issues: sampleIssues()}}
	mm := New(src)
	mm.mode = modeDetail
	mm.detailStack = []beads.Issue{{ID: "a-1", Title: "parent"}} // slim (no notes)
	mm.detailIssue = beads.Issue{ID: "a-3"}

	model, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = model.(Model)
	if mm.detailIssue.ID != "a-1" {
		t.Fatalf("pop should restore a-1; got %q", mm.detailIssue.ID)
	}
	if cmd == nil {
		t.Fatal("pop should dispatch a re-enrich Detail() cmd for the restored parent")
	}
	// Run the cmd; it must produce a detailMsg for a-1 (enriched).
	msg := cmd()
	dm, ok := msg.(detailMsg)
	if !ok {
		t.Fatalf("expected a detailMsg from the pop cmd; got %T", msg)
	}
	if dm.issue.ID != "a-1" || dm.issue.Notes != "enriched" {
		t.Errorf("pop should re-enrich a-1; got id=%q notes=%q", dm.issue.ID, dm.issue.Notes)
	}
}
