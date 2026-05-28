package tui

import (
	"context"
	"errors"
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

// stubMutator records every write the TUI dispatches. Used by the
// write-action tests to assert the correct issue and operation made
// it through.
type stubMutator struct {
	stubSource
	closed  []string
	added   []labelOp
	removed []labelOp
	notes   []labelOp // reuse the {id,label} shape for {id, text}
	created []labelOp // {repo, title} for quick-add
}

type labelOp struct{ id, label string }

func (s *stubMutator) Close(_ context.Context, i beads.Issue) error {
	s.closed = append(s.closed, i.ID)
	return nil
}
func (s *stubMutator) AddLabel(_ context.Context, i beads.Issue, label string) error {
	s.added = append(s.added, labelOp{i.ID, label})
	return nil
}
func (s *stubMutator) RemoveLabel(_ context.Context, i beads.Issue, label string) error {
	s.removed = append(s.removed, labelOp{i.ID, label})
	return nil
}
func (s *stubMutator) Note(_ context.Context, i beads.Issue, text string) error {
	s.notes = append(s.notes, labelOp{i.ID, text})
	return nil
}
func (s *stubMutator) Create(_ context.Context, repo, title string) (string, error) {
	s.created = append(s.created, labelOp{repo, title})
	return "new-id", nil
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

func TestFuzzyFilterDoesNotBleedAcrossTitleDescBoundary(t *testing.T) {
	// Title and description are scored independently. A query that
	// would only match as a subsequence spanning the boundary
	// (e.g. "ad" against {title: "cat", desc: "dog"} — `a` in
	// "cat", `d` in "dog") must NOT match.
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "cat", Description: "dog", Labels: nil},
		{ID: "a-2", Title: "rotate password", Description: "step",
			Labels: []string{"human"}},
	}}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "ad" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	for _, i := range m.visible {
		if i.ID == "a-1" {
			t.Errorf("'ad' should NOT cross-field-match a-1 {cat, dog}; visible: %+v",
				visibleIDs(m.visible))
		}
	}
}

func visibleIDs(issues []beads.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.ID)
	}
	return out
}

func TestFuzzyFilterMatchesSubsequence(t *testing.T) {
	// sahilm/fuzzy ranks by subsequence score, so a query that's
	// NOT a substring but IS a subsequence still matches. This is
	// the capability the brief's "fuzzy text filter" called for and
	// the old strings.Contains implementation couldn't deliver.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	// "rpw" is not a substring of any issue but IS a subsequence of
	// "rotate password" (r-o-t-a-te-P-asswo-W → r-p-w).
	for _, r := range "rpw" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	if len(m.visible) == 0 {
		t.Fatal("fuzzy filter should find 'rotate password' for query 'rpw'")
	}
	if m.visible[0].ID != "a-1" {
		t.Errorf("best fuzzy match should be a-1 (rotate password); got %q", m.visible[0].ID)
	}
}

func TestDisplayID_TrimsCommonPrefix(t *testing.T) {
	// Single-repo: all IDs share `would-you-kindly-`. displayID
	// strips it down to the suffix.
	src := &stubSource{issues: []beads.Issue{
		{ID: "would-you-kindly-2oa", Title: "a"},
		{ID: "would-you-kindly-1ej", Title: "b"},
		{ID: "would-you-kindly-ma5", Title: "c"},
	}}
	m := applyFetched(New(src), src)
	if got := m.displayID(m.all[0]); got != "2oa" {
		t.Errorf("displayID single-repo: got %q, want %q", got, "2oa")
	}
	if m.commonPrefix != "would-you-kindly-" {
		t.Errorf("commonPrefix: got %q, want would-you-kindly-", m.commonPrefix)
	}
}

func TestDisplayID_MultiRepoStripsPerRowRepo(t *testing.T) {
	// Multi-repo: each issue carries its own Repo and the trim is
	// per-row, not from a shared prefix.
	m := Model{
		all: []beads.Issue{
			{ID: "alpha-1", Repo: "alpha", Title: "a"},
			{ID: "beta-9", Repo: "beta", Title: "b"},
		},
	}
	if got := m.displayID(m.all[0]); got != "1" {
		t.Errorf("alpha-1 → %q, want %q", got, "1")
	}
	if got := m.displayID(m.all[1]); got != "9" {
		t.Errorf("beta-9 → %q, want %q", got, "9")
	}
}

func TestHumanBadge_DistinguishesSource(t *testing.T) {
	agent := beads.Issue{Labels: []string{"human", "src:agent"}}
	self := beads.Issue{Labels: []string{"human", "src:human"}}
	plain := beads.Issue{Labels: []string{"human"}}

	if got := humanBadgeFor(agent); !strings.Contains(got, "←") {
		t.Errorf("src:agent badge should contain a left-arrow; got %q", got)
	}
	if got := humanBadgeFor(self); !strings.Contains(got, "·") {
		t.Errorf("src:human badge should contain a middle-dot; got %q", got)
	}
	if got := humanBadgeFor(plain); strings.Contains(got, "←") || strings.Contains(got, "·") {
		t.Errorf("unlabeled badge should be plain HUMAN; got %q", got)
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

func TestInitialPaintShowsLoading(t *testing.T) {
	// Before the first fetchedMsg arrives, the view must say "loading…"
	// rather than render the "no issues — bd returned an empty list"
	// empty state, which is indistinguishable from a slow first fetch.
	src := &stubSource{issues: sampleIssues()}
	m := New(src)
	if !m.loading {
		t.Fatal("New(...) should construct a Model in loading state")
	}
	out := m.View()
	if !strings.Contains(out, "loading…") {
		t.Errorf("initial paint should render loading indicator; got:\n%s", out)
	}
	if strings.Contains(out, "no issues") {
		t.Error("initial paint should NOT render the empty-list state before the first fetch")
	}
}

// --- Phase 2.B: write-action tests ---------------------------------

// applyMutatorFetched is the stubMutator equivalent of applyFetched.
func applyMutatorFetched(m Model, s *stubMutator) Model {
	model, _ := m.Update(fetchedMsg{preset: m.preset, issues: s.issues})
	return model.(Model)
}

func TestClose_RequiresConfirmationAndDispatches(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// `c` enters confirm mode but does NOT dispatch yet
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = model.(Model)
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

func TestNote_PromptsAndDispatchesOnEnter(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	if m.mode != modeNote {
		t.Fatalf("`n` should enter modeNote; got %v", m.mode)
	}

	// type a note
	for _, r := range "rotated 2026-05-28" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}

	// enter dispatches the write and exits modeNote
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("enter should return to list mode; got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("enter with non-empty note should dispatch a write")
	}
	wm := cmd().(writeMsg)
	if wm.action != "note" || wm.id != s.issues[0].ID {
		t.Errorf("writeMsg: action=%q id=%q", wm.action, wm.id)
	}
	if len(s.notes) != 1 || s.notes[0] != (labelOp{s.issues[0].ID, "rotated 2026-05-28"}) {
		t.Errorf("Note not dispatched correctly; got %+v", s.notes)
	}
}

func TestNote_EmptyInputCancels(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = model.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

func TestWriteResult_SuccessTriggersRefetchAndSetsBanner(t *testing.T) {
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
	_ = cmd() // exercise the fetch
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
	if cmd != nil {
		t.Error("failed write should NOT refetch")
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

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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

func TestQuickAdd_DispatchesCreateWithCursorRepoAndTypedTitle(t *testing.T) {
	// Pre-load the model with an issue carrying Repo="alpha" so the
	// quick-add inherits the cursor's repo. Pressing N opens the
	// prompt, the typed runes become the title, enter dispatches
	// Mutator.Create.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "alpha task", Repo: "alpha"},
	}}}
	m := applyMutatorFetched(New(s), s)

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

func TestReadOnlySourceShowsHintInsteadOfWriting(t *testing.T) {
	// The plain stubSource does NOT implement Mutator; pressing write
	// keys should produce a "read-only" banner instead of crashing.
	s := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(s), s)

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = model.(Model)
	if cmd != nil {
		t.Error("read-only `c` must not dispatch a command")
	}
	if !strings.Contains(m.status, "read-only") {
		t.Errorf("read-only hint missing; got %q", m.status)
	}
}

func TestRecoveryFromTerminalErrorReArmsTickChain(t *testing.T) {
	// Rare interleaving: tick fires after a refresh-restart but before
	// the fetch returns. The tick sees the still-terminal error and
	// retires the chain. When the fetch eventually returns success,
	// nothing is alive to drive auto-refresh — unless fetchedMsg
	// detects the recovery and re-arms.
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)
	// initial tick self-suspends
	model, _ = m.Update(tickMsg{gen: m.tickGen})
	m = model.(Model)
	// user hits r → new chain at higher gen
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(Model)
	// tick(refresh) fires before fetch returns and self-suspends again
	model, _ = m.Update(tickMsg{gen: m.tickGen})
	m = model.(Model)
	preGen := m.tickGen

	// fetch eventually succeeds: recovery must re-arm a tick chain
	model, cmd := m.Update(fetchedMsg{preset: m.preset, issues: sampleIssues()})
	m = model.(Model)
	if m.tickGen <= preGen {
		t.Errorf("recovery should bump tickGen (was %d, now %d)", preGen, m.tickGen)
	}
	if cmd == nil {
		t.Fatal("recovery from terminal error should produce a tickCmd")
	}
	// Don't invoke cmd() — it's a tea.Tick that would block for the
	// full refresh interval. The bumped tickGen and non-nil cmd are
	// sufficient evidence; tickCmd's own behavior is exercised
	// elsewhere.
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

func TestTrunc_RuneAware(t *testing.T) {
	// Width semantics in the TUI are visual, not byte: a column
	// width of N should hold N characters regardless of whether
	// each is one byte or four. Pre-fix trunc sliced with s[:n-1]
	// which could split a multi-byte rune mid-codepoint and emit
	// invalid UTF-8 before the ellipsis. Pin the contract on a few
	// concrete inputs so a future "performance" refactor back to
	// byte semantics fails here loudly.
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short-ascii-untouched", "abc", 5, "abc"},
		{"long-ascii-truncated", "abcdefgh", 5, "abcd…"},
		{"zero-width-empty", "anything", 0, ""},
		{"one-width-single-rune", "abc", 1, "a"},
		// Multi-byte content — café is 5 bytes (é = 2 bytes), 4
		// runes. Cap at 3: pre-fix, byte-trunc gave "ca" + "…" =
		// 5 bytes ("ca…") OR worse split inside é. Post-fix:
		// "ca…" (3 runes, valid UTF-8).
		{"multibyte-stays-valid", "café", 3, "ca…"},
		// A name made entirely of multi-byte runes; truncation
		// must not split any of them.
		{"all-multibyte", "αβγδ", 3, "αβ…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trunc(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("trunc(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}
