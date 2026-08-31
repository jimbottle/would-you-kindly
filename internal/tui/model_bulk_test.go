package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the multi-select write paths (bulk close / flag / assign / label /
// priority) plus undo and repeat-last-action.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestRepeat_WithoutPriorActionShowsHint(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	m = model.(Model)
	if !strings.Contains(m.status, "nothing to repeat") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestRepeat_AfterLabelAppliesToNextCursor(t *testing.T) {
	// Add 'needs-review' to a-1 via L, move cursor to a-2, press
	// '.' — the label should land on a-2 too.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1"},
		{ID: "a-2"},
	}}}
	m := applyMutatorFetched(New(s), s)

	// L → "needs-review" → enter (toggle-adds to a-1).
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = model.(Model)
	for _, r := range "needs-review" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("label enter should dispatch")
	}
	_ = cmd()

	// Move cursor to a-2.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)

	// '.' → re-applies AddLabel("needs-review") to a-2.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	if cmd == nil {
		t.Fatal(". should re-dispatch the label")
	}
	_ = cmd()
	if len(s.added) != 2 || s.added[1] != (labelOp{"a-2", "needs-review"}) {
		t.Errorf(". should add 'needs-review' to a-2; got %+v", s.added)
	}
}

func TestRepeat_AfterDeferAppliesSameWindowToNextRow(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1"},
		{ID: "a-2"},
	}}}
	m := applyMutatorFetched(New(s), s)

	// d → "+1w" → enter (defer a-1 by 1w).
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = model.(Model)
	for _, r := range "+1w" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	_ = cmd()

	// Move cursor to a-2 and repeat.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	if cmd == nil {
		t.Fatal(". should re-dispatch the defer")
	}
	_ = cmd()
	if len(s.deferred) != 2 || s.deferred[1] != (labelOp{"a-2", "+1w"}) {
		t.Errorf(". should defer a-2 by +1w; got %+v", s.deferred)
	}
}

func TestRepeat_AfterPriorityBumpReusesAbsoluteValue(t *testing.T) {
	// '+' on a P3 row sets P2; '.' on the next row should also
	// set P2 (absolute), not relative-bump from that row's own
	// priority.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 3},
		{ID: "a-2", Priority: 0},
	}}}
	m := applyMutatorFetched(New(s), s)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	m = model.(Model)
	_ = cmd()
	if len(s.priorities) != 1 || s.priorities[0] != (priorityOp{"a-1", 2}) {
		t.Fatalf("setup: + on P3 should land P2; got %+v", s.priorities)
	}

	// Move cursor, '.' → set P2 (absolute, captured from the
	// previous dispatch).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	if cmd == nil {
		t.Fatal(". should re-dispatch the priority set")
	}
	_ = cmd()
	if len(s.priorities) != 2 || s.priorities[1] != (priorityOp{"a-2", 2}) {
		t.Errorf(". should set a-2 to P2 (absolute); got %+v", s.priorities)
	}
}

func TestHandleBulkWriteResult_TotalSuccessUsesPastTenseVerb(t *testing.T) {
	// "close" → "closed", "defer" → "deferred", "flag" → "flagged".
	// A naive `action + "ed"` produced "closeed"/"defered"; this
	// test pins the per-action verb map so a regression surfaces.
	cases := []struct {
		action   string
		wantVerb string
	}{
		{"close", "closed"},
		{"defer", "deferred"},
		{"flag", "flagged"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			src := &stubSource{issues: sampleIssues()}
			m := applyFetched(New(src), src)
			model, _ := m.Update(bulkWriteMsg{action: tc.action, total: 3})
			m = model.(Model)
			if !strings.Contains(m.status, tc.wantVerb) {
				t.Errorf("status %q should contain %q", m.status, tc.wantVerb)
			}
			if !strings.Contains(m.status, "3 rows") {
				t.Errorf("status %q should report row count", m.status)
			}
		})
	}
}

func TestHandleBulkWriteResult_PartialFailureRestoresFailedMarks(t *testing.T) {
	// One target succeeded, one failed. The failed row's mark
	// should be restored so the user can retry without
	// re-marking the entire selection.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	// Dispatch site clears marks pre-emptively; simulate post-dispatch state.
	m.marked = nil

	model, _ := m.Update(bulkWriteMsg{
		action: "close",
		total:  2,
		failed: []beads.Issue{{ID: "a-2"}},
		errs:   []string{"a-2: bd refused"},
	})
	m = model.(Model)
	if !m.marked["a-2"] {
		t.Errorf("failed row's mark should be restored; got %v", m.marked)
	}
	if m.marked["a-1"] {
		t.Errorf("succeeded row's mark should NOT be restored; got %v", m.marked)
	}
	if !strings.Contains(m.status, "1 of 2") {
		t.Errorf("status should report partial failure; got %q", m.status)
	}
}

func TestHandleBulkWriteResult_TotalFailureRestoresAllMarks(t *testing.T) {
	// Every target failed. The user lost the selection at
	// dispatch time; the handler should rebuild it so retrying is
	// one keystroke (c) away.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.marked = nil

	failed := []beads.Issue{{ID: "a-1"}, {ID: "a-2"}}
	model, cmd := m.Update(bulkWriteMsg{
		action: "close",
		total:  2,
		failed: failed,
		errs:   []string{"a-1: oops", "a-2: oops"},
	})
	m = model.(Model)
	if cmd != nil {
		// Total failure path explicitly returns nil so the banner
		// stays sticky. A non-nil cmd here would refetch + clear
		// the banner — exactly the wrong UX for a total miss.
		t.Errorf("total failure should NOT trigger a refetch; got cmd != nil")
	}
	if !m.marked["a-1"] || !m.marked["a-2"] {
		t.Errorf("all marks should be restored on total failure; got %v", m.marked)
	}
	if !strings.Contains(m.status, "failed for all 2") {
		t.Errorf("status should explain total failure; got %q", m.status)
	}
}

func TestMark_TogglesAndClearsOnEsc(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// Mark cursor row.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = model.(Model)
	if !m.marked[m.visible[0].ID] {
		t.Errorf("v should mark cursor row; got %v", m.marked)
	}

	// Move down + mark a second row.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = model.(Model)
	if len(m.marked) != 2 {
		t.Errorf("expected 2 marks; got %d", len(m.marked))
	}

	// Toggle off the second row.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = model.(Model)
	if len(m.marked) != 1 {
		t.Errorf("v should unmark; got %d", len(m.marked))
	}

	// esc clears all marks.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.marked != nil {
		t.Errorf("esc should clear marks; got %v", m.marked)
	}
}

func TestBulkClose_DispatchesAcrossAllMarked(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// Mark first two rows.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = model.(Model)
	if len(m.marked) != 2 {
		t.Fatalf("setup: expected 2 marks; got %d", len(m.marked))
	}

	// 'a' should enter confirm with the bulk prompt.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	if m.mode != modeConfirmClose {
		t.Fatalf("c should enter modeConfirmClose; got %v", m.mode)
	}

	// 'y' dispatches bulkWriteMsg-producing cmd.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("y should dispatch a bulk close")
	}
	if m.marked != nil {
		t.Errorf("marks should be consumed by the dispatch; got %v", m.marked)
	}
	msg := cmd().(bulkWriteMsg)
	if msg.action != "close" || msg.total != 2 {
		t.Errorf("bulkWriteMsg action=%q total=%d, want close/2", msg.action, msg.total)
	}
	if len(s.closed) != 2 || s.closed[0] != "a-1" || s.closed[1] != "a-2" {
		t.Errorf("expected both rows closed in visible order; got %v", s.closed)
	}
}

func TestBulkFlag_AddsHumanToMarked(t *testing.T) {
	// Mix: a-1 already human, a-2 not. Bulk flag should ADD only
	// to a-2 (idempotent on the already-flagged row).
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// Mark first two rows (a-1 human, a-2 not).
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	if cmd == nil {
		t.Fatal("H with marks should dispatch a bulk flag")
	}
	_ = cmd()
	if len(s.added) != 1 || s.added[0] != (labelOp{"a-2", "human"}) {
		t.Errorf("bulk flag should add human only to a-2 (a-1 already flagged); got %+v", s.added)
	}
}

func TestUndo_ReopensLastClosed(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// Close issue 0 (a → confirm with y) and drive the writeMsg
	// so m.lastClosed gets populated by handleWriteResult.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatalf("y should dispatch a close write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	if m.lastClosed.ID == "" {
		t.Fatalf("lastClosed should be populated after a successful close; status=%q", m.status)
	}
	closedID := m.lastClosed.ID

	// Press u → dispatch reopen; drive the cmd; assert mutator
	// recorded the call and lastClosed was cleared.
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatalf("u should dispatch a reopen write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	if len(s.reopened) != 1 || s.reopened[0] != closedID {
		t.Errorf("Reopen(%q) not dispatched; got %v", closedID, s.reopened)
	}
	if m.lastClosed.ID != "" {
		t.Errorf("lastClosed should be cleared after reopen; got %q", m.lastClosed.ID)
	}
	if !strings.Contains(m.status, "reopened") {
		t.Errorf("status should announce reopen; got %q", m.status)
	}
}

func TestUndo_CtrlZReopensLastClosed(t *testing.T) {
	// ctrl+z is the standard-undo alias on the same binding as `u`.
	// It must reopen the most-recently-closed issue exactly like `u`.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// Close issue 0 (a → confirm y) and settle the write so lastClosed populates.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatalf("y should dispatch a close write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	closedID := m.lastClosed.ID
	if closedID == "" {
		t.Fatalf("lastClosed should be populated after a successful close; status=%q", m.status)
	}

	// Press ctrl+z → dispatch reopen; drive the cmd; assert it landed.
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	m = model.(Model)
	if cmd == nil {
		t.Fatalf("ctrl+z should dispatch a reopen write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	if len(s.reopened) != 1 || s.reopened[0] != closedID {
		t.Errorf("Reopen(%q) not dispatched by ctrl+z; got %v", closedID, s.reopened)
	}
	if m.lastClosed.ID != "" {
		t.Errorf("lastClosed should be cleared after reopen; got %q", m.lastClosed.ID)
	}
}

func TestUndo_NoLastClosedShowsStatus(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = model.(Model)
	if len(s.reopened) != 0 {
		t.Errorf("u with no lastClosed should NOT dispatch a reopen; got %v", s.reopened)
	}
	if !strings.Contains(m.status, "nothing to undo") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestUndo_OptimisticallyRestoresRowBeforeRefetch(t *testing.T) {
	// ctrl+z must restore the just-closed row to the visible list
	// immediately, again without a refetch.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	before := len(m.visible)
	target := m.visible[0]

	// Close and settle.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	model, _ = m.Update(cmd())
	m = model.(Model)
	if len(m.visible) != before-1 {
		t.Fatalf("precondition: close should have removed the row; got %d", len(m.visible))
	}

	// Undo (ctrl+z) and settle — row back without a refetch.
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("ctrl+z should dispatch a reopen write")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)

	if len(m.visible) != before {
		t.Fatalf("undo should restore the row immediately; got %d, want %d", len(m.visible), before)
	}
	found := false
	for _, i := range m.visible {
		if issueKey(i) == issueKey(target) {
			found = true
		}
	}
	if !found {
		t.Errorf("reopened row %s not restored to the visible list", target.ID)
	}
}

func TestBulkClose_OptimisticallyDropsRowsBeforeRefetch(t *testing.T) {
	// A multi-select close drops every succeeded row from the visible
	// list immediately, mirroring the single-target path.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	before := len(m.visible)
	if before < 2 {
		t.Fatalf("need >=2 visible rows; got %d", before)
	}
	r0, r1 := m.visible[0], m.visible[1]

	model, _ := m.handleBulkWriteResult(bulkWriteMsg{
		action:    "close",
		total:     2,
		succeeded: []beads.Issue{r0, r1},
	})
	m = model.(Model)

	if len(m.visible) != before-2 {
		t.Fatalf("bulk close should drop 2 rows immediately; got %d want %d", len(m.visible), before-2)
	}
	for _, i := range m.visible {
		if issueKey(i) == issueKey(r0) || issueKey(i) == issueKey(r1) {
			t.Errorf("bulk-closed row %s still visible before any refetch", i.ID)
		}
	}
}

func TestBulkClose_PatchesDepCacheStatus(t *testing.T) {
	// A multi-select close must patch every succeeded target's cached
	// (status), mirroring the single-target path — otherwise bulk-closed
	// issues drop out of the open list and their stale "open" lingers
	// under dependents forever (would-you-kindly-1ym, bulk gap).
	src := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(src), src)
	m.depCache["a-1"] = []beads.Issue{
		{ID: "b-2", Status: "open"},
		{ID: "b-3", Status: "open"},
	}
	m.dependentCache["a-9"] = []beads.Issue{{ID: "b-2", Status: "open"}}

	model, _ := m.Update(bulkWriteMsg{
		action:    "close",
		total:     2,
		succeeded: []beads.Issue{{ID: "b-2"}, {ID: "b-3"}},
	})
	m = model.(Model)

	if m.depCache["a-1"][0].Status != "closed" || m.depCache["a-1"][1].Status != "closed" {
		t.Errorf("bulk close should patch all succeeded dep rows; got %+v", m.depCache["a-1"])
	}
	if m.dependentCache["a-9"][0].Status != "closed" {
		t.Errorf("bulk close should patch dependent rows too; got %+v", m.dependentCache["a-9"])
	}
}

func TestBulkWrite_NonStatusActionLeavesCache(t *testing.T) {
	// A non-status bulk action (assign) must not touch cached statuses.
	src := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(src), src)
	m.depCache["a-1"] = []beads.Issue{{ID: "b-2", Status: "open"}}

	model, _ := m.Update(bulkWriteMsg{action: "assign", total: 1, succeeded: []beads.Issue{{ID: "b-2"}}})
	m = model.(Model)
	if m.depCache["a-1"][0].Status != "open" {
		t.Errorf("a non-status bulk action must leave cached status alone; got %q", m.depCache["a-1"][0].Status)
	}
}

// stubBulkMutator is a stubMutator that also implements BulkCloser,
// recording each CloseMany batch. failIDs makes those IDs report a
// failure so the partial-failure banner / mark-restore path runs.
type stubBulkMutator struct {
	stubMutator
	batches [][]string
	failIDs map[string]bool
}

func (s *stubBulkMutator) CloseMany(_ context.Context, issues []beads.Issue) []BulkFailure {
	var ids []string
	var failed []BulkFailure
	for _, i := range issues {
		ids = append(ids, i.ID)
		if s.failIDs[i.ID] {
			failed = append(failed, BulkFailure{Issue: i, Err: errors.New("boom")})
		} else {
			s.closed = append(s.closed, i.ID)
		}
	}
	s.batches = append(s.batches, ids)
	return failed
}

func TestBulkClose_UsesOneBatchWhenMutatorIsABulkCloser(t *testing.T) {
	// would-you-kindly-cexj: closing N marked rows must be one
	// CloseMany call, not N Close calls.
	s := &stubBulkMutator{stubMutator: stubMutator{stubSource: stubSource{issues: sampleIssues()}}}
	m := applyMutatorFetched(New(s), &s.stubMutator)
	m.src = s
	for _, k := range []rune{'v', 'j', 'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	if len(m.marked) != 3 {
		t.Fatalf("setup: expected 3 marks; got %d", len(m.marked))
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("y should dispatch the bulk close")
	}
	msg := cmd().(bulkWriteMsg)
	if len(s.batches) != 1 || len(s.batches[0]) != 3 {
		t.Fatalf("expected one CloseMany batch of 3; got %v", s.batches)
	}
	if msg.total != 3 || len(msg.succeeded) != 3 || len(msg.failed) != 0 {
		t.Errorf("bulkWriteMsg total=%d ok=%d failed=%d", msg.total, len(msg.succeeded), len(msg.failed))
	}
	model, _ = m.Update(msg)
	m = model.(Model)
	if !strings.Contains(m.status, "closed 3") {
		t.Errorf("banner should report 3 closed; got %q", m.status)
	}
}

func TestBulkClose_BatchPartialFailureRestoresMarks(t *testing.T) {
	s := &stubBulkMutator{
		stubMutator: stubMutator{stubSource: stubSource{issues: sampleIssues()}},
		failIDs:     map[string]bool{"a-2": true},
	}
	m := applyMutatorFetched(New(s), &s.stubMutator)
	m.src = s
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	msg := cmd().(bulkWriteMsg)
	if len(msg.failed) != 1 || msg.failed[0].ID != "a-2" || len(msg.succeeded) != 1 {
		t.Fatalf("expected a-2 to fail and a-1 to succeed; failed=%v ok=%v", msg.failed, msg.succeeded)
	}
	model, _ = m.Update(msg)
	m = model.(Model)
	if !m.marked[issueKey(beads.Issue{ID: "a-2"})] {
		t.Errorf("the failed row should get its mark back for retry; marked=%v", m.marked)
	}
	if !strings.Contains(m.status, "1 failed") {
		t.Errorf("banner should mention the failure; got %q", m.status)
	}
}
