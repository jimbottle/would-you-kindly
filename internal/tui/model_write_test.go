package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the single-row write paths: close, note, flag, quick-add, and the
// write-result banners.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

// This file holds the write paths: close, note, flag, quick-add, defer, assign,
// label, priority, type, repeat, undo, and their bulk forms.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestClose_RequiresConfirmationAndDispatches(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// `a` enters confirm mode but does NOT dispatch yet
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.mode != modeConfirmClose {
		t.Fatalf("`c` should enter modeConfirmClose, got %v", m.mode)
	}
	if cmd != nil {
		t.Error("`c` alone must not dispatch a Close — only after confirmation")
	}
	if len(s.closed) != 0 {
		t.Fatalf("Close called before confirmation: %+v", s.closed)
	}

	// pressing anything other than y cancels
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("cancel should return to list mode, got %v", m.mode)
	}
	if !strings.Contains(m.status, "cancelled") {
		t.Errorf("cancel should set a status banner; got %q", m.status)
	}
	if len(s.closed) != 0 {
		t.Errorf("Close should not have been called on cancel; got %+v", s.closed)
	}

	// re-enter confirm, then y this time
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("confirmed close must return a write command")
	}
	// run the command to actually invoke the mutator
	gotMsg := cmd()
	wm, ok := gotMsg.(writeMsg)
	if !ok {
		t.Fatalf("write command should produce writeMsg, got %T", gotMsg)
	}
	if wm.action != "close" || wm.id != s.issues[0].ID || wm.err != nil {
		t.Errorf("writeMsg: action=%q id=%q err=%v", wm.action, wm.id, wm.err)
	}
	if len(s.closed) != 1 || s.closed[0] != s.issues[0].ID {
		t.Errorf("expected Close(%q); got %+v", s.issues[0].ID, s.closed)
	}
}

func TestToggleHuman_AddsThenRemovesLabel(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// cursor starts on issue 0 ("rotate password") which already carries `human`.
	// pressing H should call RemoveLabel.
	if !s.issues[0].IsHuman() {
		t.Fatal("setup: first sample issue should have human label")
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("H should dispatch a write")
	}
	if msg := cmd(); msg.(writeMsg).action != "unflag" {
		t.Errorf("expected unflag action; got %+v", msg)
	}
	if len(s.removed) != 1 || s.removed[0] != (labelOp{s.issues[0].ID, "human"}) {
		t.Errorf("RemoveLabel(%q, human) not dispatched; got %+v", s.issues[0].ID, s.removed)
	}

	// move cursor to issue 1 which doesn't have `human`; H should AddLabel.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	if cmd == nil {
		t.Fatal("H on non-human issue should dispatch a write")
	}
	if msg := cmd(); msg.(writeMsg).action != "flag" {
		t.Errorf("expected flag action; got %+v", msg)
	}
	if len(s.added) != 1 || s.added[0] != (labelOp{s.issues[1].ID, "human"}) {
		t.Errorf("AddLabel(%q, human) not dispatched; got %+v", s.issues[1].ID, s.added)
	}
}

func TestNote_PromptsAndDispatchesOnCtrlS(t *testing.T) {
	// modeNote now uses a multi-line textarea; submit is ctrl+s
	// (enter inserts a newline so multi-line content survives).
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	if m.mode != modeNote {
		t.Fatalf("`n` should enter modeNote; got %v", m.mode)
	}

	// Seed body directly — bubbles/textarea's character-input
	// pipeline is exercised by the bubbles package's own tests;
	// pinning every keystroke here would just couple us to its
	// implementation. The behavior we care about is "submit
	// sends Value() through".
	m.noteArea.SetValue("rotated 2026-05-28\nfollowup step")

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("ctrl+s should return to list mode; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("ctrl+s with non-empty note should dispatch a write")
	}
	wm := cmd().(writeMsg)
	if wm.action != "note" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg: action=%q id=%q", wm.action, wm.id)
	}
	if len(s.notes) != 1 || s.notes[0] != (labelOp{s.issues[0].ID, "rotated 2026-05-28\nfollowup step"}) {
		t.Errorf("multi-line Note not dispatched correctly; got %+v", s.notes)
	}
}

func TestNote_EmptyInputCancelsOnCtrlS(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = model.(Model)
	if cmd != nil {
		t.Error("empty note should not dispatch a write")
	}
	if len(s.notes) != 0 {
		t.Errorf("Note should not have been called; got %+v", s.notes)
	}
	if !strings.Contains(m.status, "cancelled") {
		t.Errorf("empty note should set a status banner; got %q", m.status)
	}
}

func TestNote_EnterInsertsNewlineInsteadOfSubmitting(t *testing.T) {
	// Regression: enter used to submit; now it must just buffer
	// a newline so multi-line content can be drafted. Pin both
	// that the mode stays modeNote AND that the textarea grew a
	// newline char.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)

	preLen := len(m.noteArea.Value())
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeNote {
		t.Errorf("enter in modeNote must NOT submit; mode=%v", m.mode)
	}
	if len(m.noteArea.Value()) <= preLen {
		t.Errorf("enter should buffer a newline; before=%d after=%d", preLen, len(m.noteArea.Value()))
	}
}

func TestWriteResult_SuccessTriggersRefetchAndSetsBanner(t *testing.T) {
	// Shorten the auto-clear delay so synchronously invoking the
	// returned cmd doesn't block on tea.Tick's underlying timer.
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	pre := s.calls

	model, cmd := m.Update(writeMsg{action: "close", id: "wyk-42"})
	m = model.(Model)
	if !strings.Contains(m.status, "closed wyk-42") {
		t.Errorf("status banner missing; got %q", m.status)
	}
	if cmd == nil {
		t.Fatal("successful write should refetch")
	}
	// handleWriteResult returns tea.Batch(fetchCmd, flashClearCmd).
	// Drain the BatchMsg so the inner fetch cmd actually fires
	// against the stub.
	if msg := cmd(); msg != nil {
		if bm, ok := msg.(tea.BatchMsg); ok {
			for _, inner := range bm {
				if inner != nil {
					_ = inner()
				}
			}
		}
	}
	if s.calls <= pre {
		t.Errorf("expected Source.Fetch to be called; calls before=%d after=%d", pre, s.calls)
	}
}

func TestWriteResult_FailureSurfacesInBanner(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, cmd := m.Update(writeMsg{
		action: "close", id: "wyk-42",
		err: errors.New("bd: issue is pinned"),
	})
	m = model.(Model)
	// Failed writes intentionally return no cmd — errors stay
	// sticky until the next keystroke so a user who glances away
	// doesn't lose the error text before reading it. (Also
	// happens to keep the test fast — no tea.Tick to drain.)
	if cmd != nil {
		t.Errorf("failed write should NOT return a cmd (no refetch, no auto-clear); got %T", cmd)
	}
	if !strings.Contains(m.status, "close wyk-42 failed") {
		t.Errorf("status should describe the failure; got %q", m.status)
	}
	if !strings.Contains(m.status, "pinned") {
		t.Errorf("status should include the underlying error; got %q", m.status)
	}
}

func TestConfirmCloseTargetsCapturedIDNotCursor(t *testing.T) {
	// Open the confirm prompt on issue 0, then have a refetch
	// reorder the list (issue 1 now first). Pressing y must close
	// the originally-targeted issue, not whatever's currently at
	// m.visible[m.cursor].
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	originalFirstID := s.issues[0].ID
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.pendingTarget.ID != originalFirstID {
		t.Fatalf("setup: expected pendingTarget.ID=%q, got %q", originalFirstID, m.pendingTarget.ID)
	}

	// Simulate a refetch that reorders: original first issue now at index 1.
	reordered := []beads.Issue{s.issues[1], s.issues[0], s.issues[2]}
	model, _ = m.Update(fetchedMsg{preset: m.preset, issues: reordered})
	m = model.(Model)

	// y confirms — should still close the originally-targeted ID.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("y should dispatch a close")
	}
	if msg := cmd().(writeMsg); msg.id != originalFirstID {
		t.Errorf("closed wrong issue: got %q, want %q (cursor was at index 0 = %q after reorder)",
			msg.id, originalFirstID, reordered[0].ID)
	}
	if len(s.closed) != 1 || s.closed[0] != originalFirstID {
		t.Errorf("Close(%q) not dispatched; got %+v", originalFirstID, s.closed)
	}
}

func TestConfirmCloseCancelsIfTargetVanishes(t *testing.T) {
	// User opens the confirm prompt on an issue; a refetch removes
	// that issue entirely (closed elsewhere, deleted, filtered out).
	// Pressing y must NOT panic and must NOT close some other issue —
	// the prompt should cancel with a status banner.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)

	// refetch with the target removed
	model, _ = m.Update(fetchedMsg{preset: m.preset, issues: s.issues[1:]})
	m = model.(Model)

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd != nil {
		t.Error("y with vanished target should NOT dispatch a write")
	}
	if len(s.closed) != 0 {
		t.Errorf("Close should not have been called; got %+v", s.closed)
	}
	if !strings.Contains(m.status, "removed from the workspace") {
		t.Errorf("status should explain the cancellation; got %q", m.status)
	}
	if m.mode != modeList {
		t.Errorf("mode should return to list; got %v", m.mode)
	}
}

func TestQuickAdd_DispatchesCreateWithCursorRepoAndTypedTitle(t *testing.T) {
	// Pre-load the model with an issue carrying Repo="alpha" so the
	// quick-add inherits the cursor's repo. Pressing N opens the
	// prompt, the typed runes become the title, enter dispatches
	// Mutator.Create.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "alpha task", Repo: "alpha"},
	}}}
	m := applyMutatorFetched(New(s).WithMe("ev"), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = model.(Model)
	if m.mode != modeQuickAdd {
		t.Fatalf("N should enter modeQuickAdd; got %v", m.mode)
	}
	if m.pendingTarget.Repo != "alpha" {
		t.Errorf("quick-add should snapshot cursor's repo; got %q", m.pendingTarget.Repo)
	}

	for _, r := range "Fix the thing" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("enter should return to modeList; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("enter with non-empty title should dispatch a Create")
	}
	wm := cmd().(writeMsg)
	if wm.action != "create" || wm.id != "new-id" {
		t.Errorf("writeMsg action=%q id=%q, want create/new-id", wm.action, wm.id)
	}
	if len(s.created) != 1 || s.created[0] != (labelOp{"alpha", "Fix the thing"}) {
		t.Errorf("Mutator.Create not dispatched correctly; got %+v", s.created)
	}
	if len(s.createdAssignees) != 1 || s.createdAssignees[0] != "ev" {
		t.Errorf("Create assignee should be m.me; got %v", s.createdAssignees)
	}
}

func TestQuickAdd_RefusesWhenOwnerUnset(t *testing.T) {
	// Owner enforcement: with m.me empty, quick-add should NOT
	// dispatch a Create. The status banner names the fix (-me
	// flag) so a user surprised by the refusal can recover.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "alpha task", Repo: "alpha"},
	}}}
	m := applyMutatorFetched(New(s), s) // no WithMe → m.me == ""

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = model.(Model)
	for _, r := range "Orphan task" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd != nil {
		t.Errorf("quick-add with empty m.me should NOT dispatch; got cmd != nil")
	}
	if len(s.created) != 0 {
		t.Errorf("Create should not have been called; got %v", s.created)
	}
	if !strings.Contains(m.status, "no assignee") {
		t.Errorf("status should explain the refusal; got %q", m.status)
	}
}

func TestQuickAdd_EmptyTitleCancels(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd != nil {
		t.Error("empty title should not dispatch a Create")
	}
	if len(s.created) != 0 {
		t.Errorf("Create should not have been called; got %+v", s.created)
	}
	if !strings.Contains(m.status, "cancelled") {
		t.Errorf("status should explain the cancellation; got %q", m.status)
	}
}

func TestClose_OptimisticallyRemovesRowBeforeRefetch(t *testing.T) {
	// Closing a row must drop it from the visible list IMMEDIATELY —
	// without any fetchedMsg being delivered — so the UI doesn't wait
	// on the multi-second post-write refetch (would-you-kindly-6dis).
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	before := len(m.visible)
	if before == 0 {
		t.Fatal("precondition: need at least one visible row")
	}
	target := m.visible[0]

	// Close (a → y) and settle the write msg; deliberately drop the
	// returned fetch cmd so no refetch runs.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("y should dispatch a close write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)

	if len(m.visible) != before-1 {
		t.Fatalf("visible should shrink by 1 immediately after close; got %d, want %d", len(m.visible), before-1)
	}
	for _, i := range m.visible {
		if issueKey(i) == issueKey(target) {
			t.Errorf("closed row %s still visible before any refetch", target.ID)
		}
	}
}

func TestClose_ShowClosedFlipsStatusInPlaceThenUndoRestores(t *testing.T) {
	// Under showClosed, a closed row STAYS visible — the optimistic
	// update flips its Status to "closed" in place rather than dropping
	// it, and undo restores the original pre-close status in place.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	m.showClosed = true // closed rows remain on screen in this mode
	before := len(m.visible)
	target := m.visible[0]
	origStatus := target.Status

	find := func(m Model) (int, bool) {
		for i := range m.visible {
			if issueKey(m.visible[i]) == issueKey(target) {
				return i, true
			}
		}
		return -1, false
	}

	// Close (a → y) and settle the write; no refetch delivered.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	model, _ = m.Update(cmd())
	m = model.(Model)

	if len(m.visible) != before {
		t.Fatalf("showClosed: row should stay visible after close; got %d want %d", len(m.visible), before)
	}
	idx, ok := find(m)
	if !ok {
		t.Fatalf("closed row %s should remain visible under showClosed", target.ID)
	}
	if m.visible[idx].Status != "closed" {
		t.Errorf("status should flip to closed in place; got %q", m.visible[idx].Status)
	}

	// Undo (ctrl+z) and settle — original status restored in place.
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	m = model.(Model)
	model, _ = m.Update(cmd())
	m = model.(Model)
	idx, ok = find(m)
	if !ok {
		t.Fatalf("row %s should still be visible after undo under showClosed", target.ID)
	}
	if m.visible[idx].Status != origStatus {
		t.Errorf("undo should restore original status in place; got %q want %q", m.visible[idx].Status, origStatus)
	}
}
