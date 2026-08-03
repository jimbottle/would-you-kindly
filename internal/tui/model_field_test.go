package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the field-setting prompts: defer, assign, label, priority, and type.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestPriorityCap_FiltersToCapAndBelow(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "P0", Priority: 0, Labels: []string{}},
		{ID: "a-2", Title: "P1", Priority: 1, Labels: []string{}},
		{ID: "a-3", Title: "P2", Priority: 2, Labels: []string{}},
		{ID: "a-4", Title: "P3", Priority: 3, Labels: []string{}},
		{ID: "a-5", Title: "P4", Priority: 4, Labels: []string{}},
	}}
	m := applyFetched(New(src), src)

	// Pressing 1 should cap to P0 only.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = model.(Model)
	if len(m.visible) != 1 || m.visible[0].Priority != 0 {
		t.Errorf("'1' should cap to P0; got %d rows", len(m.visible))
	}

	// Pressing 3 should expand to P0..P2.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = model.(Model)
	if len(m.visible) != 3 {
		t.Errorf("'3' should expand to P0..P2 (3 rows); got %d", len(m.visible))
	}

	// Pressing 0 should clear the cap.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	m = model.(Model)
	if len(m.visible) != 5 {
		t.Errorf("'0' should clear the cap (5 rows); got %d", len(m.visible))
	}
}

func TestLabel_AddsWhenAbsent(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = model.(Model)
	if m.mode != modeLabel {
		t.Fatalf("L should enter modeLabel; got %v", m.mode)
	}
	for _, r := range "blocked" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch AddLabel")
	}
	if msg := cmd().(writeMsg); msg.action != "label:blocked" {
		t.Errorf("writeMsg action = %q, want label:blocked", msg.action)
	}
	if len(s.added) != 1 || s.added[0] != (labelOp{"a-1", "blocked"}) {
		t.Errorf("AddLabel should land 'blocked' on a-1; got %+v", s.added)
	}
}

func TestLabel_RemovesWhenPresent(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent", "needs-review"}},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = model.(Model)
	for _, r := range "needs-review" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch RemoveLabel")
	}
	if msg := cmd().(writeMsg); msg.action != "unlabel:needs-review" {
		t.Errorf("writeMsg action = %q, want unlabel:needs-review", msg.action)
	}
	if len(s.removed) != 1 || s.removed[0] != (labelOp{"a-1", "needs-review"}) {
		t.Errorf("RemoveLabel should target 'needs-review'; got %+v", s.removed)
	}
}

func TestLabel_BulkIsAddOnly(t *testing.T) {
	// Bulk path always adds (matches H's bulk semantics) — a row
	// that already has the label is a no-op, a row missing it
	// gets it added. No bulk-remove path exists.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}},
		{ID: "a-2", Labels: []string{"src:agent", "needs-review"}},
	}}}
	m := applyMutatorFetched(New(s), s)

	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = model.(Model)
	for _, r := range "needs-review" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch bulk AddLabel")
	}
	_ = cmd()
	// a-1 gets the label added; a-2 was already labeled, no-op.
	if len(s.added) != 1 || s.added[0] != (labelOp{"a-1", "needs-review"}) {
		t.Errorf("bulk path should add only to missing rows; got %+v", s.added)
	}
	if len(s.removed) != 0 {
		t.Errorf("bulk path must not remove anything; got %+v", s.removed)
	}
}

func TestLabel_ReadOnlyShowsHint(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("L on read-only source should NOT enter modeLabel; got %v", m.mode)
	}
	if !strings.Contains(m.status, "read-only") {
		t.Errorf("status should explain read-only; got %q", m.status)
	}
}

func TestAssign_DispatchesSetAssigneeWithTypedValue(t *testing.T) {
	// Owner differs from Assignee on purpose: the prompt submits
	// SetAssignee, so it must seed with Assignee — seeding Owner
	// (who filed) would make a bare "confirm" overwrite the
	// assignee with the filer.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "filer", Assignee: "alice", Title: "rotate"},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	if m.mode != modeAssign {
		t.Fatalf("O should enter modeAssign; got %v", m.mode)
	}
	// Prompt should be pre-seeded with the current assignee so the
	// common "confirm/typo-fix" cases are one keystroke.
	if m.input.Value() != "alice" {
		t.Errorf("prompt should seed with current assignee; got %q", m.input.Value())
	}
	// Clear and retype bob.
	for range "alice" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = model.(Model)
	}
	for _, r := range "bob" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("enter should dispatch SetAssignee")
	}
	if msg := cmd().(writeMsg); msg.action != "assign" || msg.id != "a-1" {
		t.Errorf("writeMsg action=%q id=%q; want assign/a-1", msg.action, msg.id)
	}
	if len(s.assignees) != 1 || s.assignees[0] != (labelOp{"a-1", "bob"}) {
		t.Errorf("SetAssignee should land with the typed value; got %+v", s.assignees)
	}
}

func TestAssign_EmptyValueClearsAssignee(t *testing.T) {
	// Empty value is honored as a deliberate clear (bd accepts
	// --assignee ""). The QuickAdd require-owner rule only
	// governs creation; a pre-existing row CAN be unassigned.
	// Assignee (not Owner) so the prompt actually opens seeded and
	// the backspace loop below clears real content — with only
	// Owner set the prompt would open empty and the clear would be
	// a no-op.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Assignee: "alice"},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	for range "alice" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("empty value is a deliberate clear and should still dispatch")
	}
	_ = cmd()
	if len(s.assignees) != 1 || s.assignees[0].label != "" {
		t.Errorf("empty submission should clear owner; got %+v", s.assignees)
	}
}

func TestAssign_CancelsWhenTargetVanishes(t *testing.T) {
	// Mirror the close/note/defer pattern: a concurrent refetch
	// that drops the targeted row should produce the friendly
	// "removed from the workspace by a refresh" cancellation
	// instead of dispatching a stale-ID SetAssignee.
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice"},
	}}}
	m := applyMutatorFetched(New(s), s)
	// Enter the prompt — pendingTarget is captured here.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	// Simulate a refetch that drops a-1 entirely.
	model, _ = m.Update(fetchedMsg{preset: m.preset, issues: nil})
	m = model.(Model)
	// Submit a new value — guard should refuse to dispatch.
	for _, r := range "bob" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		// flashClearCmd is fine; only a write cmd would record.
		_ = cmd()
	}
	if len(s.assignees) != 0 {
		t.Errorf("stale target should NOT dispatch SetAssignee; got %+v", s.assignees)
	}
}

func TestAssign_ReadOnlyShowsHint(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src) // not a Mutator

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("O on read-only source should NOT enter modeAssign; got %v", m.mode)
	}
	if !strings.Contains(m.status, "read-only") {
		t.Errorf("status should explain the read-only hint; got %q", m.status)
	}
}

func TestAssign_BulkAppliesToAllMarked(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice"},
		{ID: "a-2", Owner: "bob"},
	}}}
	m := applyMutatorFetched(New(s), s)
	// Mark both rows.
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	// O → assign carol to both.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	for _, r := range "carol" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch bulk SetAssignee")
	}
	_ = cmd()
	if len(s.assignees) != 2 || s.assignees[0].label != "carol" || s.assignees[1].label != "carol" {
		t.Errorf("bulk owner change should land 'carol' on both rows; got %+v", s.assignees)
	}
}

func TestDefer_DispatchesSetDeferWithTypedValue(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = model.(Model)
	if m.mode != modeDefer {
		t.Fatalf("d should enter modeDefer; got %v", m.mode)
	}
	if m.pendingTarget.ID != s.issues[0].ID {
		t.Errorf("defer should snapshot the cursor row; got %q", m.pendingTarget.ID)
	}

	for _, r := range "+1w" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("enter should return to modeList; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("enter with non-empty value should dispatch SetDefer")
	}
	wm := cmd().(writeMsg)
	if wm.action != "defer" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg action=%q id=%q, want defer/%q", wm.action, wm.id, s.issues[0].ID)
	}
	if len(s.deferred) != 1 || s.deferred[0] != (labelOp{s.issues[0].ID, "+1w"}) {
		t.Errorf("SetDefer not dispatched correctly; got %+v", s.deferred)
	}
}

func TestDefer_EmptyValueCancels(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = model.(Model)
	// Press enter immediately → empty value cancels.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if len(s.deferred) != 0 {
		t.Errorf("empty value should not dispatch; got %v", s.deferred)
	}
	if !strings.Contains(m.status, "defer cancelled") {
		t.Errorf("status should explain cancellation; got %q", m.status)
	}
}

func TestTypeCycle_RotatesThroughKnownTypes(t *testing.T) {
	// Pin the rotation contract: T starts at task → bug → feature → ...
	// and unknown / empty types fall through to "task" (the safe start).
	cases := []struct {
		cur, want string
	}{
		{"task", "bug"},
		{"bug", "feature"},
		{"feature", "chore"},
		{"chore", "epic"},
		{"epic", "decision"},
		{"decision", "spike"},
		{"spike", "story"},
		{"story", "milestone"},
		{"milestone", "task"}, // wraps
		{"", "task"},          // empty → safe start
		{"bogus", "task"},     // unknown → safe start
	}
	for _, tc := range cases {
		if got := nextIssueType(tc.cur); got != tc.want {
			t.Errorf("nextIssueType(%q) = %q, want %q", tc.cur, got, tc.want)
		}
	}
}

func TestTypeCycle_DispatchesSetIssueType(t *testing.T) {
	src := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate", Status: "open", IssueType: "task"},
	}}}
	m := applyFetched(New(&src.stubSource).WithMe("ev"), &src.stubSource)
	// Manually set the mutator (applyFetched uses the read-only Source path).
	m.src = src

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("expected a write cmd from T; got nil")
	}
	// Run the cmd to dispatch the write through the stub.
	cmd()

	if len(src.issueTypes) != 1 {
		t.Fatalf("SetIssueType called %d times, want 1; recorded=%v", len(src.issueTypes), src.issueTypes)
	}
	got := src.issueTypes[0]
	if got.id != "a-1" || got.label != "bug" {
		t.Errorf("SetIssueType{%q, %q}, want {a-1, bug}", got.id, got.label)
	}
	// Repeat state should record the kind+arg for '.' replay.
	if m.lastAction.kind != "type" || m.lastAction.arg != "bug" {
		t.Errorf("lastAction = {%q, %q}, want {type, bug}", m.lastAction.kind, m.lastAction.arg)
	}
}

func TestTypeCycle_RepeatReappliesStoredType(t *testing.T) {
	// Press T, then press .; the second write must replay the
	// stored type verbatim (not re-cycle from the row's current
	// type), matching how priority replay re-applies the stored
	// value. Guards the case "type" branch in handleRepeat.
	src := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate", Status: "open", IssueType: "task"},
	}}}
	m := applyFetched(New(&src.stubSource).WithMe("ev"), &src.stubSource)
	m.src = src

	// First press: T cycles task → bug.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("T returned nil cmd")
	}
	cmd()

	// Second press: . replays the stored type ("bug") against the
	// same row. handleRepeat looks at the cursor row's current
	// IssueType for context but feeds `arg` to SetIssueType, so
	// even though the stub doesn't refetch, the dispatched value
	// must still be "bug".
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	_ = model
	if cmd == nil {
		t.Fatal(". returned nil cmd — repeat case 'type' likely missing")
	}
	cmd()

	if len(src.issueTypes) != 2 {
		t.Fatalf("SetIssueType called %d times, want 2; recorded=%v", len(src.issueTypes), src.issueTypes)
	}
	if src.issueTypes[1].label != "bug" {
		t.Errorf("repeat call SetIssueType[1] = %q, want %q (stored type replayed)", src.issueTypes[1].label, "bug")
	}
}

func TestBumpPriority_PlusBumpsMoreUrgent(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
		{ID: "a-2", Priority: 1},
	}}}
	m := applyMutatorFetched(New(s), s)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	if cmd == nil {
		t.Fatal("+ should dispatch a SetPriority write")
	}
	_ = cmd()
	if len(s.priorities) != 1 || s.priorities[0] != (priorityOp{"a-1", 1}) {
		t.Errorf("+ on P2 should write P1; got %+v", s.priorities)
	}
}

func TestBumpPriority_MinusBumpsLessUrgent(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
	}}}
	m := applyMutatorFetched(New(s), s)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	if cmd == nil {
		t.Fatal("- should dispatch a SetPriority write")
	}
	_ = cmd()
	if len(s.priorities) != 1 || s.priorities[0] != (priorityOp{"a-1", 3}) {
		t.Errorf("- on P2 should write P3; got %+v", s.priorities)
	}
}

func TestBumpPriority_ClampsAtEdges(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	t.Run("plus at P0 is a no-op", func(t *testing.T) {
		s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
			{ID: "a-1", Priority: 0},
		}}}
		m := applyMutatorFetched(New(s), s)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
		m = model.(Model)
		if len(s.priorities) != 0 {
			t.Errorf("+ on P0 should be a no-op; got %+v", s.priorities)
		}
		if !strings.Contains(m.status, "already at P0") {
			t.Errorf("status should explain the no-op; got %q", m.status)
		}
	})

	t.Run("minus at P4 is a no-op", func(t *testing.T) {
		s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
			{ID: "a-1", Priority: 4},
		}}}
		m := applyMutatorFetched(New(s), s)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
		m = model.(Model)
		if len(s.priorities) != 0 {
			t.Errorf("- on P4 should be a no-op; got %+v", s.priorities)
		}
		if !strings.Contains(m.status, "already at P4") {
			t.Errorf("status should explain the no-op; got %q", m.status)
		}
	})
}

func TestBumpPriority_BulkBannerReportsReprioritized(t *testing.T) {
	// Regression for the MED finding on job 1316: the bulk path
	// used to pass action="flag" so the banner read "flagged N
	// rows" for a priority change. Pin "reprioritized" so the
	// label can't silently regress.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
		{ID: "a-2", Priority: 3},
	}}}
	m := applyMutatorFetched(New(s), s)
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	if cmd == nil {
		t.Fatal("+ with marks should dispatch a bulk write")
	}
	resultMsg := cmd()
	model, _ := m.Update(resultMsg)
	m = model.(Model)
	if !strings.Contains(m.status, "reprioritized") {
		t.Errorf("bulk + banner should say 'reprioritized'; got %q", m.status)
	}
	if strings.Contains(m.status, "flagged") {
		t.Errorf("bulk + banner must not say 'flagged'; got %q", m.status)
	}
}

func TestBumpPriority_BulkAppliesToAllMarked(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
		{ID: "a-2", Priority: 3},
	}}}
	m := applyMutatorFetched(New(s), s)

	// Mark both rows.
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	if len(m.marked) != 2 {
		t.Fatalf("setup: expected 2 marks; got %d", len(m.marked))
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	if cmd == nil {
		t.Fatal("+ with marks should dispatch bulk SetPriority")
	}
	_ = cmd()
	if len(s.priorities) != 2 {
		t.Fatalf("expected both rows bumped; got %+v", s.priorities)
	}
	// a-1 P2→P1, a-2 P3→P2 (each nudged by -1).
	if s.priorities[0] != (priorityOp{"a-1", 1}) || s.priorities[1] != (priorityOp{"a-2", 2}) {
		t.Errorf("bulk + should nudge each row by -1; got %+v", s.priorities)
	}
}
