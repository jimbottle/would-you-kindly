package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/filters"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
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
	closed           []string
	reopened         []string
	deferred         []labelOp    // {id, when} for SetDefer
	priorities       []priorityOp // {id, priority} for SetPriority
	assignees        []labelOp    // {id, owner} for SetAssignee
	descriptions     []labelOp    // {id, body} for SetDescription
	issueTypes       []labelOp    // {id, type} for SetIssueType
	added            []labelOp
	removed          []labelOp
	notes            []labelOp // reuse the {id,label} shape for {id, text}
	created          []labelOp // {repo, title} for quick-add
	createdAssignees []string  // parallel slice to created, recording the assignee passed to Create
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
func (s *stubMutator) Create(_ context.Context, repo, title, assignee string) (string, error) {
	s.created = append(s.created, labelOp{repo, title})
	s.createdAssignees = append(s.createdAssignees, assignee)
	return "new-id", nil
}
func (s *stubMutator) Reopen(_ context.Context, i beads.Issue) error {
	s.reopened = append(s.reopened, i.ID)
	return nil
}
func (s *stubMutator) SetDefer(_ context.Context, i beads.Issue, when string) error {
	s.deferred = append(s.deferred, labelOp{i.ID, when})
	return nil
}
func (s *stubMutator) SetPriority(_ context.Context, i beads.Issue, p int) error {
	s.priorities = append(s.priorities, priorityOp{i.ID, p})
	return nil
}
func (s *stubMutator) SetAssignee(_ context.Context, i beads.Issue, assignee string) error {
	s.assignees = append(s.assignees, labelOp{i.ID, assignee})
	return nil
}
func (s *stubMutator) SetDescription(_ context.Context, i beads.Issue, body string) error {
	s.descriptions = append(s.descriptions, labelOp{i.ID, body})
	return nil
}
func (s *stubMutator) SetIssueType(_ context.Context, i beads.Issue, issueType string) error {
	s.issueTypes = append(s.issueTypes, labelOp{i.ID, issueType})
	return nil
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

func TestHumanBadge_AlwaysReadsHUMAN(t *testing.T) {
	// All human-labeled issues render the same plain HUMAN badge
	// regardless of src — the column is a yes/no signal, not a
	// three-way categorisation.
	cases := []beads.Issue{
		{Labels: []string{"human", "src:agent"}},
		{Labels: []string{"human", "src:human"}},
		{Labels: []string{"human"}},
	}
	for _, i := range cases {
		got := responsibilityBadgeFor(i)
		if !strings.Contains(got, "HUMAN") {
			t.Errorf("badge should read HUMAN for %v; got %q", i.Labels, got)
		}
		if strings.Contains(got, "←") || strings.Contains(got, "·") {
			t.Errorf("badge should be plain (no arrow/dot) for %v; got %q", i.Labels, got)
		}
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
	//
	// The "no-blank-on-switch" change keeps the OLD preset's rows
	// visible during the switch (so users don't see a wiped table
	// for the duration of bd's round-trip); the dropped-stale
	// invariant is about NEW data not overwriting NEWER state, so
	// we check that the stale fetch leaves m.all == the old rows
	// rather than asserting m.all is cleared.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	wantCount := len(m.all)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if !m.refreshing {
		t.Errorf("preset switch should set refreshing=true; got false")
	}

	// late fetch for the OLD preset arrives — must NOT clobber the
	// current preset's rows even though they're still the OLD data
	// on screen.
	stale := []beads.Issue{{ID: "stale-1", Title: "stale", Labels: []string{}}}
	model, _ = m.Update(fetchedMsg{preset: filter.PresetAll, issues: stale})
	m = model.(Model)
	if len(m.all) != wantCount {
		t.Errorf("stale fetch should have been dropped; m.all changed from %d to %d", wantCount, len(m.all))
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

func TestTerminalErrorBannerAppendsRetryHint(t *testing.T) {
	// Terminal errors (bd missing, no workspace) also suspend the
	// auto-refresh tick, so the small banner can't rely on the next
	// tick recovering. The recovery path is `r`, and the user needs
	// an explicit cue in the banner.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)

	out := m.View()
	if !strings.Contains(out, "press r to retry") {
		t.Errorf("terminal-error banner should append the retry hint; got:\n%s", out)
	}
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("terminal error should still leave the table visible; got:\n%s", out)
	}
}

func TestTransientErrorBannerOmitsRetryHint(t *testing.T) {
	// Transient errors recover on the next 10s tick on their own —
	// the explicit "press r to retry" hint is only needed for
	// terminal errors that suspend auto-refresh. Keep the banner
	// terse for the common flaky-bd case.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(fetchedMsg{preset: m.preset, err: errors.New("bd: transient flake")})
	m = model.(Model)

	out := m.View()
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("transient error should still surface as a banner; got:\n%s", out)
	}
	if strings.Contains(out, "press r to retry") {
		t.Errorf("transient banner should NOT include the retry hint (next tick recovers); got:\n%s", out)
	}
}

func TestTransientFetchErrorKeepsTableVisible(t *testing.T) {
	// The "no-blank-on-refresh" invariant: once we have data on
	// screen, a transient bd error during an auto-refresh tick
	// surfaces as a small banner — the table stays put. Pre-fix,
	// any non-nil m.lastErr caused viewList to replace the whole
	// table with a full-screen "press r to retry" stand-in, which
	// is the "TUI blanks on refresh" symptom the user reported.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	out := m.View()
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Fatalf("setup: initial view should show issue rows; got:\n%s", out)
	}

	// Simulate a flaky bd query: tick → fetch returns error.
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: errors.New("bd: transient flake")})
	m = model.(Model)

	out = m.View()
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("transient fetch error should leave the table visible; got:\n%s", out)
	}
	if strings.Contains(out, "press r to retry") {
		t.Errorf("transient fetch error should not show the full-screen retry hint; got:\n%s", out)
	}
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("transient fetch error should surface as a 'refresh failed' banner; got:\n%s", out)
	}
}

func TestRefreshKeyKeepsTableVisible(t *testing.T) {
	// Pressing `r` no longer blanks the screen: the table stays
	// up, a small ↻ refreshing hint appears in the status bar.
	// Replaces the previous "loading…" full-screen blank that
	// fired on every keypress of r.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(Model)

	if m.loading {
		t.Error("manual refresh should not set loading=true (blanks the view)")
	}
	if !m.refreshing {
		t.Error("manual refresh should set refreshing=true (status-bar indicator)")
	}
	out := m.View()
	if strings.Contains(out, "loading…") {
		t.Errorf("manual refresh view should not show full-screen loading…; got:\n%s", out)
	}
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("manual refresh view should still show issue rows; got:\n%s", out)
	}
	if !strings.Contains(out, "refreshing") {
		t.Errorf("manual refresh view should show the ↻ refreshing indicator; got:\n%s", out)
	}
}

func TestSwitchPresetKeepsRowsAndShowsRefreshIndicator(t *testing.T) {
	// Switching presets no longer blanks the table — the previous
	// rows stay on screen until the new fetch returns, with a
	// subtle "↻ refreshing" hint in the status bar. The cursor
	// still resets to 0 so the user lands at the top of the new
	// view as soon as data arrives.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	preCount := len(m.all)
	if preCount == 0 {
		t.Fatal("setup: sampleIssues should yield at least one row")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)

	if len(m.all) != preCount {
		t.Errorf("switchPreset must NOT clear all (blanks the screen); got all=%d, want %d", len(m.all), preCount)
	}
	if m.cursor != 0 {
		t.Errorf("switchPreset should reset cursor; got %d", m.cursor)
	}
	if m.loading {
		t.Error("switchPreset should NOT set loading=true (that's the full-screen blank path)")
	}
	if !m.refreshing {
		t.Error("switchPreset should set refreshing=true")
	}
	out := m.View()
	if strings.Contains(out, "loading…") {
		t.Errorf("view should not render the full-screen loading indicator on a preset switch:\n%s", out)
	}
	if !strings.Contains(out, "refreshing") {
		t.Errorf("view should render the refresh indicator in the status bar:\n%s", out)
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

// withFlashClearDelay shortens flashClearDelay for the duration of
// a test so synchronously invoking the auto-clear cmd doesn't
// block on tea.Tick's underlying time.NewTimer (~4s of dead air
// per test that drains the batched cmd). Returns the restore
// function; callers defer it.
func withFlashClearDelay(t *testing.T, d time.Duration) func() {
	t.Helper()
	prev := flashClearDelay
	flashClearDelay = d
	return func() { flashClearDelay = prev }
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
	if !strings.Contains(m.status, "no owner") {
		t.Errorf("status should explain the refusal; got %q", m.status)
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

// manyIssues builds n stub issues with IDs that satisfy the
// cross-workspace leak guard (prefix `a-`) so tests around viewport
// scrolling don't need to wrestle with foreign-prefix drops.
func manyIssues(n int) []beads.Issue {
	out := make([]beads.Issue, n)
	for i := 0; i < n; i++ {
		out[i] = beads.Issue{
			ID:     fmt.Sprintf("a-%d", i+1),
			Title:  fmt.Sprintf("row %d", i+1),
			Status: "open",
			Labels: []string{},
		}
	}
	return out
}

func TestStickyHeader_HeaderAndAllRowsFitWithoutScroll(t *testing.T) {
	// 5 rows + terminal large enough to show everything → no
	// "↑/↓ more" hints, scroll stays at 0.
	src := &stubSource{issues: manyIssues(5)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.scroll != 0 {
		t.Errorf("scroll should be 0 when everything fits; got %d", m.scroll)
	}
	out := m.View()
	for _, want := range []string{"row 1", "row 5", "ID", "Status"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in view; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more above") || strings.Contains(out, "more below") {
		t.Errorf("no scroll-hint expected when everything fits; got:\n%s", out)
	}
}

func TestStickyHeader_BodyCappedToTerminalHeight(t *testing.T) {
	// 30 rows + cramped terminal → viewport shows a small window;
	// the column header MUST still appear in the rendered output.
	// This is the core fix: pre-72y the terminal scrolled the
	// header off the top.
	src := &stubSource{issues: manyIssues(30)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if !strings.Contains(out, "Status") {
		t.Errorf("header row should remain visible at the top of every paint; got:\n%s", out)
	}
	if !strings.Contains(out, "row 1") {
		t.Errorf("cursor row should be visible (cursor=0, row 1); got:\n%s", out)
	}
	// Some row beyond what fits in the body should NOT be in the
	// rendered output — proving the body is capped, not dumped.
	if strings.Contains(out, "row 30") {
		t.Errorf("row 30 should be off-screen in a cramped terminal; got:\n%s", out)
	}
	if !strings.Contains(out, "more below") {
		t.Errorf("expected '↓ N more below' hint when rows are clipped; got:\n%s", out)
	}
}

func TestStickyHeader_CursorScrollFollowsDown(t *testing.T) {
	// Press j past the bottom of the viewport — scroll must
	// advance so the cursor row stays visible, and the "↑ more
	// above" hint must appear.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 15; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	if m.cursor != 15 {
		t.Fatalf("cursor expected at 15 after 15 j's; got %d", m.cursor)
	}
	if m.scroll == 0 {
		t.Errorf("scroll should have advanced past 0; got %d", m.scroll)
	}
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the rendered window [%d, %d)", m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
	out := m.View()
	if !strings.Contains(out, "more above") {
		t.Errorf("expected '↑ N more above' hint after scrolling down; got:\n%s", out)
	}
}

func TestStickyHeader_TopAndBottomKeysAdjustScroll(t *testing.T) {
	// G jumps to the last row → scroll lands so the last row is
	// visible. g jumps back to the top → scroll = 0.
	src := &stubSource{issues: manyIssues(25)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = model.(Model)
	if m.cursor != 24 {
		t.Errorf("G expected to land on the last row (24); got %d", m.cursor)
	}
	if m.cursor < m.scroll {
		t.Errorf("G left the cursor above the scroll window: cursor=%d scroll=%d", m.cursor, m.scroll)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("g expected to land on row 0; got %d", m.cursor)
	}
	if m.scroll != 0 {
		t.Errorf("g should pull scroll to 0; got %d", m.scroll)
	}
}

func TestStickyHeader_WindowResizeClampsScroll(t *testing.T) {
	// User scrolls down, then resizes the terminal taller. The
	// scroll offset should re-clamp so we don't leave blank rows
	// past the end of the data.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 10})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 18; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	beforeScroll := m.scroll
	if beforeScroll == 0 {
		t.Fatal("setup: scroll should be > 0 after pressing j 18 times")
	}
	model, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = model.(Model)
	if m.scroll != 0 {
		t.Errorf("after resizing tall enough to show everything, scroll should clamp to 0; got %d (cursor=%d)", m.scroll, m.cursor)
	}
}

func TestStickyHeader_CursorStaysInViewWhenStatusBannerAppears(t *testing.T) {
	// Regression for the chrome-shrink-mid-update case: a write
	// failure sets m.status (no refetch), which grows chromeExtra()
	// by 1 and shrinks bodyHeight() by 1. If scroll isn't
	// re-clamped at that point, a cursor sitting at the bottom of
	// a long, scrolled list falls just outside the now-smaller
	// rendered window — the highlighted row briefly disappears.
	src := &stubSource{issues: manyIssues(40)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	// Drive the cursor down so it sits at the bottom of the
	// rendered window.
	for i := 0; i < 25; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	bodyBefore := m.bodyHeight()
	// Simulate a write failure: writeMsg with err non-nil → handleWriteResult
	// sets m.status and returns without a refetch.
	model, _ = m.Update(writeMsg{action: "close", id: m.visible[m.cursor].ID, err: errors.New("bd: simulated")})
	m = model.(Model)

	if m.status == "" {
		t.Fatal("setup: expected m.status to be set by the failure")
	}
	bodyAfter := m.bodyHeight()
	if bodyAfter >= bodyBefore {
		t.Fatalf("setup: expected bodyHeight to shrink with the new banner (was %d, now %d)", bodyBefore, bodyAfter)
	}
	// The actual invariant: cursor must still be inside the
	// rendered window after the chrome grew.
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the rendered window [%d, %d) after status banner appeared",
			m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
	// And the view must actually contain the cursor row.
	out := m.View()
	cursorRow := fmt.Sprintf("row %d", m.cursor+1)
	if !strings.Contains(out, cursorRow) {
		t.Errorf("cursor row %q missing from view (transient clip):\n%s", cursorRow, out)
	}
}

func TestStickyHeader_CursorStaysInViewWhenModalOpens(t *testing.T) {
	// Same invariant for the modal-entry path: opening modeFilter
	// (or any modal) adds 2 lines of chrome. The re-clamp call in
	// the entry handler must keep the cursor on-screen.
	src := &stubSource{issues: manyIssues(40)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 25; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	bodyBefore := m.bodyHeight()
	// Press '/' to open the fuzzy-filter prompt (modeFilter → +2 chrome).
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.mode != modeFilter {
		t.Fatalf("setup: expected modeFilter, got %v", m.mode)
	}
	bodyAfter := m.bodyHeight()
	if bodyAfter >= bodyBefore {
		t.Fatalf("setup: expected bodyHeight to shrink when modal opens (was %d, now %d)", bodyBefore, bodyAfter)
	}
	if m.cursor < m.scroll || m.cursor >= m.scroll+m.bodyHeight() {
		t.Errorf("cursor (%d) escaped the viewport [%d, %d) when modal opened",
			m.cursor, m.scroll, m.scroll+m.bodyHeight())
	}
}

func TestColumnOrder_OwnerIsSecondFromLeft_LegacyHumanRenameCheck(t *testing.T) {
	// Header pin: in multi-repo mode the responsibility column
	// header is now "owner" (renamed from "human" so the column
	// can carry AGENT badges too). "owner" must appear before
	// "wyk" so the responsibility signal stays second-from-left.
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "row in alpha"},
		{ID: "beta-9", Repo: "beta", Title: "row in beta"},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	oi := strings.Index(out, "owner")
	wi := strings.Index(out, "wyk")
	if oi < 0 || wi < 0 {
		t.Fatalf("expected both 'owner' and 'wyk' headers in view; got:\n%s", out)
	}
	if oi > wi {
		t.Errorf("'owner' header should appear before 'wyk' header in the column row; got owner at %d, wyk at %d", oi, wi)
	}
}

func TestTitleTruncation_NarrowTerminalEllipsizesTitle(t *testing.T) {
	// On a narrow pane the title used to spill past the right
	// edge. With titleBudget capping the column, long titles get
	// the ellipsis treatment; details still live behind enter.
	longTitle := "Pivot to eBay OAuth + Trading API (Chrome Custom Tabs for auth) — replaces WebView-only sign-in"
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: longTitle, Status: "open", Labels: []string{}},
	}}
	m := New(src)
	// Narrow pane: 80 columns. Multi-repo chrome eats ~80; the
	// budget floor (20) kicks in, so the title is heavily clipped.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if strings.Contains(out, longTitle) {
		t.Errorf("expected long title to be truncated on a 80-col pane; got full title:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis after a clipped title; got:\n%s", out)
	}
}

func TestTitleTruncation_WideTerminalShowsFullTitle(t *testing.T) {
	// Sanity check: with plenty of room the title is rendered
	// verbatim. titleBudget should NOT collapse content that fits.
	title := "Decide uninstall feedback form provider"
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: title, Status: "open", Labels: []string{}},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 300, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if !strings.Contains(out, title) {
		t.Errorf("wide pane should show the full title; got:\n%s", out)
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

func TestResponsibilityBadge_AgentTaskNotHumanFlagged(t *testing.T) {
	// New AGENT branch: src:agent + NOT human → an AGENT badge.
	// This is the inbox row case — the agent's responsibility is
	// to act on these rather than note them.
	agentInbox := beads.Issue{Labels: []string{"src:agent"}}
	got := responsibilityBadgeFor(agentInbox)
	if got == "" {
		t.Fatalf("src:agent + NOT human should produce a badge; got empty")
	}
	if !strings.Contains(got, "AGENT") {
		t.Errorf("expected AGENT badge for inbox row; got %q", got)
	}
	if strings.Contains(got, "HUMAN") {
		t.Errorf("AGENT badge should not contain HUMAN; got %q", got)
	}
}

func TestResponsibilityBadge_HumanLabelTrumpsAgentSource(t *testing.T) {
	// An issue carrying both `human` and `src:agent` is in the
	// human's lap; the badge must read HUMAN (the agent's
	// hand-back arrow variant), NOT AGENT, even though src:agent
	// is also set.
	bounced := beads.Issue{Labels: []string{"human", "src:agent"}}
	got := responsibilityBadgeFor(bounced)
	if !strings.Contains(got, "HUMAN") {
		t.Errorf("human label should produce a HUMAN badge regardless of source; got %q", got)
	}
	if strings.Contains(got, "AGENT") {
		t.Errorf("AGENT must not appear when human label is set; got %q", got)
	}
}

func TestResponsibilityBadge_BlankForOwnerlessRows(t *testing.T) {
	// No human label, no src:agent → no responsibility signal
	// applies. Column renders blank.
	orphan := beads.Issue{Labels: []string{"src:human"}}
	if got := responsibilityBadgeFor(orphan); got != "" {
		t.Errorf("src:human without human label should produce no badge; got %q", got)
	}
	bare := beads.Issue{Labels: nil}
	if got := responsibilityBadgeFor(bare); got != "" {
		t.Errorf("a label-less row should produce no badge; got %q", got)
	}
}

func TestColumnOrder_OwnerHeaderIsSecondFromLeft(t *testing.T) {
	// The column header renamed from 'human' to 'owner' to reflect
	// the broader responsibility framing. Header must still appear
	// before 'wyk' (second-from-left position invariant).
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "x"},
		{ID: "beta-9", Repo: "beta", Title: "y"},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	oi := strings.Index(out, "owner")
	wi := strings.Index(out, "wyk")
	if oi < 0 {
		t.Errorf("'owner' header missing from view:\n%s", out)
	}
	if oi > wi {
		t.Errorf("'owner' should appear before 'wyk'; got owner=%d wyk=%d", oi, wi)
	}
}

func TestResponsibilityBadge_HumanBlockForBlockedAgentTask(t *testing.T) {
	// src:agent + NOT human + BlockedByHuman → HUMAN-BLOCK badge.
	// The flag is set post-Fetch by markBlockedByHuman; this test
	// pins the badge-rendering side of the contract.
	blocked := beads.Issue{
		Labels:          []string{"src:agent"},
		BlockedByHuman:  true,
		DependencyCount: 1,
	}
	got := responsibilityBadgeFor(blocked)
	if !strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("BlockedByHuman should produce HUMAN-BLOCK badge; got %q", got)
	}
	// Must NOT also produce the plain AGENT label — those are
	// mutually exclusive states for the column.
	if strings.Contains(got, "AGENT") {
		t.Errorf("HUMAN-BLOCK badge should not also say AGENT; got %q", got)
	}
}

func TestResponsibilityBadge_HumanBlockOnlyWhenFlagSet(t *testing.T) {
	// An agent task with deps but no BlockedByHuman flag set
	// stays plain AGENT. The flag is set explicitly by the
	// dep-lookup pass; without it (lookup failed, blocker not
	// in current fetch, etc.) we don't speculate.
	deps := beads.Issue{
		Labels:          []string{"src:agent"},
		DependencyCount: 1,
		BlockedByHuman:  false,
	}
	got := responsibilityBadgeFor(deps)
	if !strings.Contains(got, "AGENT") {
		t.Errorf("agent task with deps but flag unset stays AGENT; got %q", got)
	}
	if strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("HUMAN-BLOCK must require the explicit flag; got %q", got)
	}
}

func TestFlashAutoClear_ScheduledByWriteSuccess(t *testing.T) {
	// handleWriteResult should set m.status AND return a
	// flashClearCmd. Drain the batch; the inner clear cmd should
	// produce a flashClearMsg tagged with the active statusGen.
	// (The actual clear happens when Update receives that msg —
	// we exercise that separately below.)
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	preGen := m.statusGen

	model, cmd := m.Update(writeMsg{action: "close", id: "wyk-42"})
	m = model.(Model)
	if m.statusGen <= preGen {
		t.Errorf("setStatus should bump statusGen; was %d, now %d", preGen, m.statusGen)
	}

	// Drain the batched cmds; one of the inner clears should be a
	// flashClearMsg with our gen.
	saw := false
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if bm, ok := msg.(tea.BatchMsg); ok {
				for _, inner := range bm {
					if inner == nil {
						continue
					}
					if fc, ok := inner().(flashClearMsg); ok && fc.gen == m.statusGen {
						saw = true
					}
				}
			}
		}
	}
	if !saw {
		t.Errorf("expected a flashClearMsg tagged with statusGen=%d among batched cmds", m.statusGen)
	}
}

func TestFlashAutoClear_StaleClearDoesNotWipeNewStatus(t *testing.T) {
	// A clear from gen=1 must not wipe a status whose gen is now
	// 2 (the user did another action before the timer fired).
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.setStatus("first")
	firstGen := m.statusGen
	m.setStatus("second")
	if m.statusGen == firstGen {
		t.Fatal("setStatus should bump statusGen on every call")
	}

	// Stale clear from gen=firstGen arrives — must be ignored.
	model, _ := m.Update(flashClearMsg{gen: firstGen})
	m = model.(Model)
	if m.status != "second" {
		t.Errorf("stale clear wiped the active status; want %q, got %q", "second", m.status)
	}

	// Current clear (gen=current) DOES wipe.
	model, _ = m.Update(flashClearMsg{gen: m.statusGen})
	m = model.(Model)
	if m.status != "" {
		t.Errorf("current-gen clear should wipe; got %q", m.status)
	}
}

func TestEmptyState_HumanPresetCelebrates(t *testing.T) {
	// `h` preset with no human-flagged issues should celebrate,
	// not just say "no matches" (the dull default).
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "no human label here", Labels: []string{"src:agent"}},
	}}
	m := applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	// Pretend the human-preset fetch came back empty.
	model, _ = m.Update(fetchedMsg{preset: m.preset, issues: []beads.Issue{}})
	m = model.(Model)
	out := m.View()
	if !strings.Contains(out, "no human-flagged issues") {
		t.Errorf("human-preset empty state should be celebratory; got:\n%s", out)
	}
}

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

func TestFilterChip_RendersWhenActiveOnlyOnNonDefaultPresetOrCap(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// Default state — no chip line.
	if got := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); got != "" {
		t.Errorf("default preset + no cap should produce no chip; got %q", got)
	}

	// After a priority cap — chip appears.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = model.(Model)
	if got := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); !strings.Contains(got, "P1") {
		t.Errorf("expected ≤P1 chip after pressing '2'; got %q", got)
	}

	// After preset switch + cap — both chips appear.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed)
	if !strings.Contains(chips, "human") || !strings.Contains(chips, "P1") {
		t.Errorf("expected both human + P1 chips; got %q", chips)
	}
}

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

	// Press s four more times → updated → repo → id → none.
	for i := 0; i < 4; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = model.(Model)
	}
	if m.sortBy != sortNone {
		t.Errorf("cycle should return to sortNone after 5 presses; got %v", m.sortBy)
	}
}

// stubClosedToggler wraps stubSource with a SetIncludeClosed
// recorder so the C-key test can assert the toggle flowed all the
// way through model → ClosedToggler.
type stubClosedToggler struct {
	stubSource
	includeClosed bool
}

func (s *stubClosedToggler) SetIncludeClosed(v bool) { s.includeClosed = v }

func TestShowClosed_TogglesStateAndRefetches(t *testing.T) {
	src := &stubClosedToggler{stubSource: stubSource{issues: sampleIssues()}}
	m := New(src)
	// Seed initial rows without applyFetched (which wants the
	// concrete *stubSource).
	model, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = model.(Model)

	if m.showClosed {
		t.Fatalf("showClosed should start false")
	}
	callsBefore := src.calls

	// Press C → flips the flag on both model and source, triggers
	// a refetch cmd.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	m = model.(Model)
	if !m.showClosed {
		t.Errorf("expected model.showClosed=true after C")
	}
	if !src.includeClosed {
		t.Errorf("expected source.IncludeClosed=true after C")
	}
	if cmd == nil {
		t.Errorf("expected a refetch cmd after C")
	} else if msg := cmd(); msg != nil {
		// Drive the returned cmd so the stub's Fetch counter ticks.
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	if src.calls <= callsBefore {
		t.Errorf("expected Fetch to be re-issued after C; calls=%d before=%d", src.calls, callsBefore)
	}

	// Chip strip should now include the +closed pill.
	chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed)
	if !strings.Contains(chips, "+closed") {
		t.Errorf("expected +closed chip; got %q", chips)
	}

	// Press C again → flips back off.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	m = model.(Model)
	if m.showClosed || src.includeClosed {
		t.Errorf("second C should toggle off; model=%v source=%v", m.showClosed, src.includeClosed)
	}
}

func TestColumnsOverlay_TogglesHidesColumnFromHeader(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	// Force a known terminal width so titleBudget is non-zero and
	// renderHeader has all columns laid out.
	m.width = 200

	// Sanity: type column is in the header by default.
	if !strings.Contains(m.renderHeader(), " T ") {
		t.Fatalf("baseline header should include the T column")
	}

	// Open overlay with `o`.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	if m.mode != modeColumns {
		t.Fatalf("expected modeColumns after pressing o; got %v", m.mode)
	}

	// Press 5 — toggleableColumns[4] is "type" (owner=1, wyk=2,
	// repo=3, branch=4, type=5). Multi-only entries are inert in
	// single-repo mode but still occupy a slot — so the test
	// relies on the registry order, not on a runtime filter.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = model.(Model)
	if !m.colsHidden[colIDType] {
		t.Errorf("expected colIDType hidden after pressing 5; got hidden=%v", m.colsHidden)
	}

	// Press esc to close the overlay.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("expected modeList after esc; got %v", m.mode)
	}

	// Header no longer renders the T column.
	if strings.Contains(m.renderHeader(), " T ") {
		t.Errorf("expected T column hidden from header; got %q", m.renderHeader())
	}
}

func TestColumnsOverlay_PersistsHiddenColumnsToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.json")

	src := &stubSource{issues: sampleIssues()}
	m := New(src).WithHiddenColumns(map[string]bool{}, path)
	m = applyFetched(m, src)

	// Open, toggle the owner column (slot 1), close.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = model.(Model)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Load straight from disk to confirm the save happened.
	cfg, err := uiconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.HiddenColumns) != 1 || cfg.HiddenColumns[0] != colIDOwner {
		t.Errorf("HiddenColumns = %v, want [%q]", cfg.HiddenColumns, colIDOwner)
	}
}

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

func TestEmptyMatchCopy_PresetSpecificHints(t *testing.T) {
	// Pin each preset's first-line meaning AND the second-line
	// recovery hint so a future drift gets caught. Empty view is
	// where new users land most often — the copy is part of the
	// product experience.
	cases := []struct {
		name        string
		preset      filter.Preset
		query       string
		wantContent string
		wantHint    string
	}{
		{"human celebrates", filter.PresetHuman, "", "no human-flagged issues", "Tab cycles"},
		{"ready explains state", filter.PresetReady, "", "no ready work", "Tab to cycle"},
		{"mine nudges at -me", filter.PresetMine, "", "nothing assigned to you", "-me"},
		{"blocked is positive", filter.PresetBlocked, "", "no blocked issues", "Tab cycles"},
		{"default mentions closed", filter.PresetAll, "", "no issues match", "C includes closed"},
		{"query miss explains escape", filter.PresetAll, "rotate", `no matches for "rotate"`, "clear the fuzzy filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emptyMatchCopy(tc.preset, tc.query)
			if !strings.Contains(got, tc.wantContent) {
				t.Errorf("first line should contain %q; got %q", tc.wantContent, got)
			}
			if !strings.Contains(got, tc.wantHint) {
				t.Errorf("recovery hint should contain %q; got %q", tc.wantHint, got)
			}
			// Two-line shape: every preset gets a hint on a
			// second line.
			if !strings.Contains(got, "\n") {
				t.Errorf("copy should be 2 lines (content + hint); got %q", got)
			}
		})
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

func TestHighlightRunes_StylesMatchedRunesOnly(t *testing.T) {
	// lipgloss strips escapes in non-TTY environments (go test),
	// so we can't grep for SGR codes directly. Instead, render
	// each expected fragment with the same style and assert the
	// output contains those exact rendered bytes — same logic
	// the function uses, so the test pins the per-rune
	// segmentation regardless of color profile.
	got := highlightRunes("hello", []int{0, 4}, fuzzyMatchStyle)
	wantH := fuzzyMatchStyle.Render("h")
	wantO := fuzzyMatchStyle.Render("o")
	if !strings.Contains(got, wantH) {
		t.Errorf("expected styled 'h' in output; got %q", got)
	}
	if !strings.Contains(got, wantO) {
		t.Errorf("expected styled 'o' in output; got %q", got)
	}
	// The plain middle 'ell' should pass through verbatim.
	if !strings.Contains(got, "ell") {
		t.Errorf("unmatched runes should pass through verbatim; got %q", got)
	}
}

func TestHighlightRunes_OutOfRangeIndicesDropped(t *testing.T) {
	// Match indices past the end of s (e.g., truncated title) are
	// silently skipped — no panic, no trailing ANSI noise.
	got := highlightRunes("hi", []int{0, 5}, fuzzyMatchStyle)
	if !strings.Contains(got, "i") {
		t.Errorf("expected the unmatched tail to render; got %q", got)
	}
}

func TestRecomputeVisible_PopulatesTitleMatchesOnFilter(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate password"},
		{ID: "a-2", Title: "deploy preview"},
	}}
	m := applyFetched(New(src), src)
	if len(m.titleMatches) != 0 {
		t.Fatalf("titleMatches should start empty; got %v", m.titleMatches)
	}

	// Type a query → titleMatches should fill for matched rows.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "rot" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if len(m.titleMatches) == 0 {
		t.Errorf("titleMatches should populate after a filter; got %v", m.titleMatches)
	}
	if idxs, ok := m.titleMatches["a-1"]; !ok || len(idxs) == 0 {
		t.Errorf("rotated row should have non-empty match indices; got %v", idxs)
	}

	// Clearing the filter should drop titleMatches so a future
	// non-filtered paint doesn't render stale highlights.
	// Pressing `/` re-seeds the input with the prior query, so to
	// land on an empty filter we set m.query directly + re-run
	// recomputeVisible — mirrors what the model does on every
	// query change.
	m.query = ""
	m.recomputeVisible()
	if m.titleMatches != nil {
		t.Errorf("titleMatches should be nil after clearing filter; got %v", m.titleMatches)
	}
}

func TestFSEventMsg_DispatchesNonNilCmd(t *testing.T) {
	// A watcher tick should batch a refetch + wait re-arm. We
	// can't easily inspect a tea.Batch's contents from a unit
	// test (it returns a BatchMsg the runtime then re-dispatches),
	// so the contract test is just: a non-nil cmd came back.
	// The fetchCmd path itself is exhaustively covered by every
	// other write-handler test.
	src := &stubSource{issues: sampleIssues()}
	events := make(chan struct{}, 1)
	m := applyFetched(New(src).WithFSEvents(events), src)

	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Errorf("fsEventMsg should produce a batched refetch+rearm cmd; got nil")
	}
}

func TestFSEventMsg_SuspendedOnTerminalError(t *testing.T) {
	// Terminal-error suspension (no bd / no workspace) still
	// applies — refetching when there's no source to query just
	// wastes work. The wait still re-arms so a later recovery
	// (user fixes PATH) can come through. We distinguish the
	// suspended branch from the active branch by the side effect
	// the inner fetchCmd would produce: src.calls ticks if and
	// only if a refetch was batched. The wait callback reads from
	// a pre-loaded channel so neither branch blocks the test.
	src := &stubSource{err: beads.ErrBDNotFound}
	events := make(chan struct{}, 1)
	events <- struct{}{} // pre-load so waitFSEvent returns immediately
	m := applyFetched(New(src).WithFSEvents(events), src)
	m.lastErr = beads.ErrBDNotFound // isTerminalErr definitely matches this

	callsBefore := src.calls
	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Fatalf("even in terminal-error state, the wait should re-arm")
	}
	drainCmd(cmd)
	if src.calls != callsBefore {
		t.Errorf("terminal-error suspension should NOT refetch; calls=%d before=%d", src.calls, callsBefore)
	}
}

// drainCmd walks a tea.Cmd (and any nested tea.BatchMsg) so every
// inner func() runs and produces its side effects. Used by the
// fsEventMsg suspension test to detect whether a fetchCmd is
// hiding inside a batch; a single non-batch cmd is just consumed.
func drainCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drainCmd(c)
		}
	}
}

func TestRenderStatsLine_CountsHumanAndMine(t *testing.T) {
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"human"}, Owner: "ev"},
		{ID: "a-2", Labels: []string{"human", "src:agent"}, Owner: "ev"},
		{ID: "a-3", Labels: []string{"src:agent"}, Owner: "other"},
		{ID: "a-4", Owner: "ev"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src).WithMe("ev"), src)

	got := m.renderStatsLine()
	// 2 human (a-1, a-2), 3 mine (a-1, a-2, a-4 owned by ev).
	if !strings.Contains(got, "2 human") {
		t.Errorf("expected '2 human' in stats; got %q", got)
	}
	if !strings.Contains(got, "3 mine") {
		t.Errorf("expected '3 mine' in stats; got %q", got)
	}
}

func TestRenderStatsLine_EmptyWhenNoSignals(t *testing.T) {
	// No identity AND no human-labeled issues → no stats line at
	// all. A bare "· 0 human · 0 mine" suffix would be visual
	// chrome the user can't act on; the empty string keeps the
	// status bar clean for read-only or unconfigured runs.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, Owner: "other"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src), src) // me unset

	if got := m.renderStatsLine(); got != "" {
		t.Errorf("expected empty stats line; got %q", got)
	}
}

func TestRenderStatsLine_MineSlotShowsZeroWhenMeSet(t *testing.T) {
	// With an identity wired up but zero owned rows, we still
	// render "0 mine" so the user can tell their identity made it
	// through. Silent omission would look like a config bug.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, Owner: "other"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src).WithMe("ev"), src)

	got := m.renderStatsLine()
	if !strings.Contains(got, "0 mine") {
		t.Errorf("expected '0 mine' when me set + zero owned; got %q", got)
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

	// 'c' should enter confirm with the bulk prompt.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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

// editTempFile creates a temp file containing body and returns
// its path; the test owns cleanup via t.Cleanup.
func editTempFile(t *testing.T, body string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "wyk-edit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestEditFinished_TrailingNewlineFromEditorIsNotAChange(t *testing.T) {
	// Most editors (vi/vim included, the documented fallback)
	// append a trailing '\n' when saving a body that lacked one.
	// Open-and-quit-without-edit must NOT dispatch SetDescription
	// or the stored body silently accumulates whitespace over
	// repeated edits.
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Description: "no trailing newline"},
	}}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "no trailing newline\n") // editor added '\n'

	model, _ := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "a-1"},
		path:         path,
		originalBody: "no trailing newline",
	})
	m = model.(Model)
	if len(s.descriptions) != 0 {
		t.Errorf("editor-added trailing newline should NOT dispatch; got %+v", s.descriptions)
	}
	if !strings.Contains(m.status, "no change") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestEditFinished_DispatchSendsTrimmedBody(t *testing.T) {
	// Real change with extra trailing newlines: we send the
	// trimmed body so a downstream `bd update --description-file`
	// doesn't store the editor's trailing whitespace.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Description: "old body"},
	}}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "new body\n\n\n")

	_, cmd := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "a-1"},
		path:         path,
		originalBody: "old body",
	})
	if cmd == nil {
		t.Fatal("real change should dispatch")
	}
	_ = cmd()
	if len(s.descriptions) != 1 || s.descriptions[0].label != "new body" {
		t.Errorf("dispatched body should be trimmed; got %+v", s.descriptions)
	}
}

func TestBeginEdit_ReadOnlyShowsHint(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src) // not a Mutator

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = model.(Model)
	if !strings.Contains(m.status, "read-only") {
		t.Errorf("status should announce read-only; got %q", m.status)
	}
}

func TestBeginEdit_EmptyListIsNoop(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: nil}}
	m := applyMutatorFetched(New(s), s)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = model.(Model)
	if !strings.Contains(m.status, "nothing to edit") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestEditFinished_DispatchesSetDescriptionOnChange(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Description: "old body"},
	}}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "NEW BODY")

	_, cmd := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "a-1"},
		path:         path,
		originalBody: "old body",
	})
	if cmd == nil {
		t.Fatal("a real body change should dispatch SetDescription")
	}
	if msg := cmd().(writeMsg); msg.action != "edit" || msg.id != "a-1" {
		t.Errorf("writeMsg action=%q id=%q; want edit/a-1", msg.action, msg.id)
	}
	if len(s.descriptions) != 1 || s.descriptions[0] != (labelOp{"a-1", "NEW BODY"}) {
		t.Errorf("SetDescription should land the typed body; got %+v", s.descriptions)
	}
}

func TestEditFinished_NoChangeIsNoOp(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Description: "same body"},
	}}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "same body")

	model, _ := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "a-1"},
		path:         path,
		originalBody: "same body",
	})
	m = model.(Model)
	if len(s.descriptions) != 0 {
		t.Errorf("no-change should NOT dispatch SetDescription; got %+v", s.descriptions)
	}
	if !strings.Contains(m.status, "no change") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestEditFinished_EditorErrorSurfaces(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "anything")

	model, _ := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "a-1"},
		path:         path,
		originalBody: "anything",
		err:          errors.New("editor exit 1"),
	})
	m = model.(Model)
	if len(s.descriptions) != 0 {
		t.Errorf("editor error should NOT dispatch SetDescription; got %+v", s.descriptions)
	}
	if !strings.Contains(m.status, "aborted") {
		t.Errorf("status should announce the abort; got %q", m.status)
	}
}

func TestEditFinished_CancelsWhenTargetVanishes(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	// applyMutatorFetched gives m a populated m.all; deliberately
	// pick an ID NOT in the issue list so issueExists() misses.
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	path := editTempFile(t, "different body")

	model, _ := m.Update(editFinishedMsg{
		target:       beads.Issue{ID: "ghost-99"},
		path:         path,
		originalBody: "old body",
	})
	m = model.(Model)
	if len(s.descriptions) != 0 {
		t.Errorf("vanished target should NOT dispatch SetDescription; got %+v", s.descriptions)
	}
	if !strings.Contains(m.status, "removed by a refresh") {
		t.Errorf("status should announce the cancellation; got %q", m.status)
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
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice", Title: "rotate"},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	m = model.(Model)
	if m.mode != modeAssign {
		t.Fatalf("O should enter modeAssign; got %v", m.mode)
	}
	// Prompt should be pre-seeded with the current owner so the
	// common "confirm/typo-fix" cases are one keystroke.
	if m.input.Value() != "alice" {
		t.Errorf("prompt should seed with current owner; got %q", m.input.Value())
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

func TestAssign_EmptyValueClearsOwner(t *testing.T) {
	// Empty value is honored as a deliberate clear (bd accepts
	// --assignee ""). The QuickAdd require-owner rule only
	// governs creation; a pre-existing row CAN be unassigned.
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice"},
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

func TestUndo_ReopensLastClosed(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)

	// Close issue 0 (c → confirm with y) and drive the writeMsg
	// so m.lastClosed gets populated by handleWriteResult.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
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

func TestYankRich_CopiesIDDashTitle(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate password"},
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	m = model.(Model)
	if copied != "a-1 — rotate password" {
		t.Errorf("Y should yank 'ID — title'; got %q", copied)
	}
	if !strings.Contains(m.status, "a-1 — rotate password") {
		t.Errorf("status should echo the copied payload; got %q", m.status)
	}
}

func TestYankRich_EmptyTitleFallsBackToBareID(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "   "}, // whitespace-only title
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	_ = model
	if copied != "a-1" {
		t.Errorf("whitespace-only title should fall back to bare ID; got %q", copied)
	}
}

func TestYankAll_CopiesEveryVisibleID(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "first"},
		{ID: "a-2", Title: "second"},
		{ID: "a-3", Title: "third"},
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'*'}})
	m = model.(Model)
	want := "a-1\na-2\na-3"
	if copied != want {
		t.Errorf("* should yank newline-joined IDs; got %q, want %q", copied, want)
	}
	if !strings.Contains(m.status, "3 IDs") {
		t.Errorf("status should report the count; got %q", m.status)
	}
}

func TestYankAll_EmptyListSetsStatusInstead(t *testing.T) {
	src := &stubSource{issues: nil}
	m := applyFetched(New(src), src)

	called := false
	orig := clipboardCopy
	clipboardCopy = func(s string) error { called = true; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'*'}})
	m = model.(Model)
	if called {
		t.Error("empty list should NOT touch the clipboard")
	}
	if !strings.Contains(m.status, "nothing to yank") {
		t.Errorf("status should explain the no-op; got %q", m.status)
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

func TestYankMarkdown_OpenRowEmitsUncheckedBox(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate", Status: "open"},
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	_ = model
	want := "- [ ] a-1 — rotate"
	if copied != want {
		t.Errorf("M open: got %q, want %q", copied, want)
	}
}

func TestYankMarkdown_ClosedRowEmitsCheckedBox(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "done thing", Status: "closed"},
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	_ = model
	want := "- [x] a-1 — done thing"
	if copied != want {
		t.Errorf("M closed: got %q, want %q", copied, want)
	}
}

func TestYankMarkdown_EmptyTitleFallsBackToBareID(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "   ", Status: "open"},
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	_ = model
	if copied != "- [ ] a-1" {
		t.Errorf("whitespace-only title should drop the dash-title; got %q", copied)
	}
}

func TestYankAllMarkdown_MixesOpenAndClosed(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "first", Status: "open"},
		{ID: "a-2", Title: "done", Status: "closed"},
		{ID: "a-3", Title: "  ", Status: "open"}, // whitespace title → bare ID
	}}
	m := applyFetched(New(src), src)

	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}})
	m = model.(Model)
	want := "- [ ] a-1 — first\n- [x] a-2 — done\n- [ ] a-3"
	if copied != want {
		t.Errorf("_ yank markdown mismatch\n  got:  %q\n  want: %q", copied, want)
	}
	if !strings.Contains(m.status, "3 rows") {
		t.Errorf("status should report 3-row count; got %q", m.status)
	}
}

func TestYankAllMarkdown_EmptyListNoOp(t *testing.T) {
	src := &stubSource{issues: nil}
	m := applyFetched(New(src), src)

	called := false
	orig := clipboardCopy
	clipboardCopy = func(s string) error { called = true; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'_'}})
	m = model.(Model)
	if called {
		t.Error("empty list must not touch the clipboard")
	}
	if !strings.Contains(m.status, "nothing to yank") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestYank_CopiesCursorIssueID(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// Swap the clipboard seam so the test doesn't touch /dev/tty.
	var copied string
	orig := clipboardCopy
	clipboardCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if copied == "" {
		t.Fatalf("expected clipboardCopy to be called")
	}
	if copied != m.visible[0].ID {
		t.Errorf("copied = %q, want cursor ID %q", copied, m.visible[0].ID)
	}
	if !strings.Contains(m.status, "copied") || !strings.Contains(m.status, copied) {
		t.Errorf("status banner should announce the copy; got %q", m.status)
	}
}

func TestYank_EmptyListSetsStatusInstead(t *testing.T) {
	src := &stubSource{issues: nil}
	m := applyFetched(New(src), src)

	called := false
	orig := clipboardCopy
	clipboardCopy = func(s string) error { called = true; return nil }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if called {
		t.Errorf("clipboardCopy should NOT be called on empty list")
	}
	if !strings.Contains(m.status, "nothing to yank") {
		t.Errorf("status should explain the no-op; got %q", m.status)
	}
}

func TestYank_FailureSurfacesError(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	orig := clipboardCopy
	clipboardCopy = func(s string) error { return errors.New("/dev/tty: permission denied") }
	t.Cleanup(func() { clipboardCopy = orig })

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = model.(Model)
	if !strings.Contains(m.status, "yank failed") {
		t.Errorf("status should announce failure; got %q", m.status)
	}
	if !strings.Contains(m.status, "permission denied") {
		t.Errorf("status should include the underlying error; got %q", m.status)
	}
}

func TestColumnsOverlay_MultiOnlySlotInertInSingleRepo(t *testing.T) {
	// In single-repo mode the wyk/repo/branch slots (2-4) are
	// inert — pressing them shouldn't toggle anything. The slot
	// numbering still has to match the registry order so the
	// keystroke means the same column whether wyk launches into
	// single- or multi-repo mode.
	src := &stubSource{issues: sampleIssues()} // sampleIssues has no Repo → single-repo
	m := applyFetched(New(src), src)
	if m.isMultiRepo() {
		t.Fatalf("test premise: sampleIssues should produce a single-repo view")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = model.(Model)
	// Slot 2 → wyk (multi-only). Slot 3 → repo. Slot 4 → branch.
	for _, r := range []rune{'2', '3', '4'} {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	if len(m.colsHidden) != 0 {
		t.Errorf("multi-only slot toggles should be no-ops in single-repo mode; got %v", m.colsHidden)
	}

	// Confirm the registry order — slot 2 still names wyk after
	// the no-op, so a user who learns the mapping in one mode
	// keeps it in the other.
	if toggleableColumns[1].ID != colIDWyk {
		t.Errorf("slot 2 should be wyk (multi-only); got %q", toggleableColumns[1].ID)
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

func TestShellFields(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`query "p0"`, []string{"query", "p0"}},
		{`create -t "fix the thing"`, []string{"create", "-t", "fix the thing"}},
		{`'single quoted'`, []string{"single quoted"}},
		{`mixed "double" 'single'`, []string{"mixed", "double", "single"}},
		{`  multiple   spaces  `, []string{"multiple", "spaces"}},
		{``, nil},
		// Empty quoted arg — `bd update <id> --desc ""` clears a
		// field. Without the started-flag preservation, the
		// empty token silently disappeared and bd received the
		// flag with no value.
		{`a ""`, []string{"a", ""}},
		{`--desc '' next`, []string{"--desc", "", "next"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := shellFields(tc.in)
			if len(got) != len(tc.want) {
				t.Errorf("shellFields(%q) = %v, want %v", tc.in, got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("shellFields(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
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

func TestCommandPalette_AssignWithValueDispatchesDirectly(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice"},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "assign bob" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal(":assign <value> should dispatch directly")
	}
	_ = cmd()
	if len(s.assignees) != 1 || s.assignees[0] != (labelOp{"a-1", "bob"}) {
		t.Errorf(":assign should land 'bob' on a-1; got %+v", s.assignees)
	}
}

func TestCommandPalette_PriorityAbsoluteSetsValue(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 3},
	}}}
	m := applyMutatorFetched(New(s), s)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "priority 0" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal(":priority should dispatch")
	}
	_ = cmd()
	if len(s.priorities) != 1 || s.priorities[0] != (priorityOp{"a-1", 0}) {
		t.Errorf(":priority 0 should set P0 absolute; got %+v", s.priorities)
	}
}

func TestCommandPalette_PriorityOutOfRangeIsUsageError(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	cases := []string{"priority 5", "priority -1", "priority foo", "priority"}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
				{ID: "a-1", Priority: 2},
			}}}
			m := applyMutatorFetched(New(s), s)

			model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
			m = model.(Model)
			for _, r := range sub {
				model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				m = model.(Model)
			}
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = model.(Model)
			if len(s.priorities) != 0 {
				t.Errorf("%q should NOT dispatch; got %+v", sub, s.priorities)
			}
			if !strings.Contains(m.status, ":priority") {
				t.Errorf("status should announce usage; got %q", m.status)
			}
		})
	}
}

func TestCommandPalette_LabelWithValueTogglesOnRow(t *testing.T) {
	// Already-labeled row → remove; missing-labeled row → add.
	t.Run("removes when present", func(t *testing.T) {
		s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
			{ID: "a-1", Labels: []string{"needs-review"}},
		}}}
		m := applyMutatorFetched(New(s), s)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "label needs-review" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal(":label should dispatch RemoveLabel")
		}
		_ = cmd()
		if len(s.removed) != 1 || s.removed[0].label != "needs-review" {
			t.Errorf(":label should remove existing; got %+v", s.removed)
		}
	})

	t.Run("adds when absent", func(t *testing.T) {
		s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
			{ID: "a-1"},
		}}}
		m := applyMutatorFetched(New(s), s)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "label blocked" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal(":label should dispatch AddLabel")
		}
		_ = cmd()
		if len(s.added) != 1 || s.added[0].label != "blocked" {
			t.Errorf(":label should add missing; got %+v", s.added)
		}
	})
}

func TestCommandPalette_AssignBareOpensModeAssign(t *testing.T) {
	// ':assign' with no value should mode-handoff into the same
	// prompt 'O' opens — pin the mode transition because the
	// updateCommand → dispatchCommand → beginAssign chain has
	// historically been the kind of interaction that silently
	// drifted.
	src := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Owner: "alice"},
	}}}
	m := applyMutatorFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "assign" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeAssign {
		t.Errorf("bare :assign should hand off to modeAssign; got %v", m.mode)
	}
}

func TestCommandPalette_LabelBareOpensModeLabel(t *testing.T) {
	src := &stubMutator{stubSource: stubSource{issues: []beads.Issue{{ID: "a-1"}}}}
	m := applyMutatorFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "label" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeLabel {
		t.Errorf("bare :label should hand off to modeLabel; got %v", m.mode)
	}
}

func TestCommandPalette_ReadOnlySurfacesHint(t *testing.T) {
	// Read-only source: each of :assign, :priority, :label must
	// surface the 'read-only mode' status and dispatch nothing.
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	cases := []string{"assign bob", "priority 0", "label needs-review"}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			src := &stubSource{issues: sampleIssues()} // NOT a Mutator
			m := applyFetched(New(src), src)
			model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
			m = model.(Model)
			for _, r := range sub {
				model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				m = model.(Model)
			}
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = model.(Model)
			if !strings.Contains(m.status, "read-only") {
				t.Errorf("status should announce read-only; got %q", m.status)
			}
		})
	}
}

func TestCommandPalette_BulkAssignAppliesToAllMarked(t *testing.T) {
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
	// :assign carol via palette.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "assign carol" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal(":assign with marks should dispatch a bulk write")
	}
	_ = cmd()
	if len(s.assignees) != 2 || s.assignees[0].label != "carol" || s.assignees[1].label != "carol" {
		t.Errorf("bulk :assign should land 'carol' on both rows; got %+v", s.assignees)
	}
}

func TestCommandPalette_BulkPriorityAppliesToAllMarked(t *testing.T) {
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 3},
		{ID: "a-2", Priority: 2},
	}}}
	m := applyMutatorFetched(New(s), s)
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "priority 0" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal(":priority with marks should dispatch a bulk write")
	}
	_ = cmd()
	if len(s.priorities) != 2 || s.priorities[0] != (priorityOp{"a-1", 0}) || s.priorities[1] != (priorityOp{"a-2", 0}) {
		t.Errorf("bulk :priority 0 should set both rows to P0; got %+v", s.priorities)
	}
}

func TestCommandPalette_BulkLabelIsAddOnlyAndIdempotent(t *testing.T) {
	// Mirror the keyboard L's bulk semantics: rows missing the
	// label get it added; rows that already have it are
	// silently skipped (no AddLabel call dispatched).
	s := &stubMutator{stubSource: stubSource{issues: []beads.Issue{
		{ID: "a-1"}, // missing the label
		{ID: "a-2", Labels: []string{"needs-review"}}, // already has it
	}}}
	m := applyMutatorFetched(New(s), s)
	for _, k := range []rune{'v', 'j', 'v'} {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = model.(Model)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "label needs-review" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal(":label with marks should dispatch a bulk write")
	}
	_ = cmd()
	if len(s.added) != 1 || s.added[0] != (labelOp{"a-1", "needs-review"}) {
		t.Errorf("bulk :label should add only to a-1; got %+v", s.added)
	}
	if len(s.removed) != 0 {
		t.Errorf("bulk :label must NOT remove anything; got %+v", s.removed)
	}
}

func TestCommandPalette_OpensAndDispatches(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	// Press ':' → modeCommand.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	if m.mode != modeCommand {
		t.Fatalf(": should enter modeCommand; got %v", m.mode)
	}

	// Type "preset human" + enter → switchPreset(human).
	for _, r := range "preset human" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.preset != filter.PresetHuman {
		t.Errorf(":preset human should switch preset; got %v", m.preset)
	}
}

func TestCommandPalette_UnknownCommandSurfacesStatus(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "wat" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if !strings.Contains(m.status, "unknown command") {
		t.Errorf("status should explain the unknown command; got %q", m.status)
	}
}

// stubRawBD wraps stubSource with a recorder so :bd dispatch can
// be asserted without standing up a real bd binary.
type stubRawBD struct {
	stubSource
	calls  []string // each formatted as "repo|arg arg arg"
	out    []byte
	rawErr error
}

func (s *stubRawBD) RawBD(_ context.Context, repo string, args []string) ([]byte, error) {
	s.calls = append(s.calls, repo+"|"+strings.Join(args, " "))
	return s.out, s.rawErr
}

func TestCommandPalette_BDDispatchesAndOpensOutputOverlay(t *testing.T) {
	src := &stubRawBD{
		stubSource: stubSource{issues: sampleIssues()},
		out:        []byte("ready: 2 issues\n"),
	}
	m := New(src)
	mod, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "bd ready" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("enter should dispatch the bd invocation")
	}
	msg := cmd()
	model, _ = m.Update(msg)
	m = model.(Model)
	if m.mode != modeOutput {
		t.Errorf("expected modeOutput; got %v", m.mode)
	}
	if !strings.Contains(m.outputText, "ready: 2 issues") {
		t.Errorf("overlay body should contain stdout; got %q", m.outputText)
	}
	if len(src.calls) != 1 || src.calls[0] != "|ready" {
		t.Errorf(":bd ready should call RawBD([], ['ready']); got %v", src.calls)
	}
}

func TestCommandPalette_BDEmptyArgsIsUsageError(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubRawBD{stubSource: stubSource{issues: sampleIssues()}}
	m := New(src)
	mod, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "bd" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if len(src.calls) != 0 {
		t.Errorf("bare :bd should NOT dispatch; got %v", src.calls)
	}
	if !strings.Contains(m.status, "args required") {
		t.Errorf("status should explain the usage; got %q", m.status)
	}
}

func TestCommandPalette_BDOutputFooterShowsScrollPercent(t *testing.T) {
	// Pin the overflow / no-overflow branches of viewOutput's
	// footer. Short output → no percent prefix; long output that
	// exceeds the viewport height → percent prefix appears.
	t.Run("no overflow", func(t *testing.T) {
		src := &stubRawBD{
			stubSource: stubSource{issues: sampleIssues()},
			out:        []byte("just one line\n"),
		}
		m := New(src)
		mod, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
		m = mod.(Model)
		mod, _ = m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
		m = mod.(Model)
		mod, _ = m.Update(rawBDMsg{args: "ready", out: src.out})
		m = mod.(Model)
		out := m.viewOutput()
		if strings.Contains(out, "%") {
			t.Errorf("short output should NOT show scroll percent; got %q", out)
		}
	})

	t.Run("overflow shows percent", func(t *testing.T) {
		// 100 lines into a viewport of height ~8 (40 - chrome)
		// guarantees overflow.
		var body strings.Builder
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&body, "line %d\n", i)
		}
		src := &stubRawBD{
			stubSource: stubSource{issues: sampleIssues()},
			out:        []byte(body.String()),
		}
		m := New(src)
		mod, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
		m = mod.(Model)
		mod, _ = m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
		m = mod.(Model)
		mod, _ = m.Update(rawBDMsg{args: "ready", out: src.out})
		m = mod.(Model)
		out := m.viewOutput()
		if !strings.Contains(out, "%") {
			t.Errorf("long output should surface scroll percent; got %q", out)
		}
	})
}

func TestCommandPalette_BDOutputUsesViewport(t *testing.T) {
	// Long bd output should land in the viewport so the overlay
	// scrolls instead of overflowing into terminal scroll
	// (which loses the header + footer). Pin both that the
	// viewport receives the captured body and that the rendered
	// output contains it after a small WindowSizeMsg.
	src := &stubRawBD{
		stubSource: stubSource{issues: sampleIssues()},
		out:        []byte("line1\nline2\nline3\n"),
	}
	m := New(src)
	mod, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mod.(Model)
	mod, _ = m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)

	mod, _ = m.Update(rawBDMsg{args: "ready", out: src.out})
	m = mod.(Model)
	if m.mode != modeOutput {
		t.Fatalf("expected modeOutput; got %v", m.mode)
	}
	if !strings.Contains(m.outputVP.View(), "line1") {
		t.Errorf("viewport should contain the captured stdout; got %q", m.outputVP.View())
	}
	out := m.viewOutput()
	if !strings.Contains(out, "bd output") {
		t.Errorf("rendered overlay should contain header; got %q", out)
	}
	if !strings.Contains(out, "line1") {
		t.Errorf("rendered overlay should contain body line; got %q", out)
	}
}

func TestCommandPalette_BDErrorRendersBracketedErrorLine(t *testing.T) {
	// The error branch of rawBDMsg appends "[error] <msg>" to
	// the overlay body — uncovered before. Pin both halves
	// (captured stdout AND the error) so a future refactor that
	// drops the error line gets caught.
	src := &stubRawBD{
		stubSource: stubSource{issues: sampleIssues()},
		out:        []byte("partial output\n"),
		rawErr:     errors.New("bd exited 1"),
	}
	m := New(src)
	mod, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "bd ready" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("enter should dispatch the bd invocation")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)

	if !strings.Contains(m.outputText, "partial output") {
		t.Errorf("overlay should include captured stdout; got %q", m.outputText)
	}
	if !strings.Contains(m.outputText, "[error]") {
		t.Errorf("overlay should include the [error] tag on bd failure; got %q", m.outputText)
	}
	if !strings.Contains(m.outputText, "bd exited 1") {
		t.Errorf("overlay should include the error message; got %q", m.outputText)
	}
}

func TestCommandPalette_BDDoesNotYankFromDetailMode(t *testing.T) {
	// Regression: a slow `:bd` completing while the user has
	// navigated into the detail view (or any non-list/command
	// mode) should NOT silently switch them to the output
	// overlay. Status banner names the recovery.
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubRawBD{stubSource: stubSource{issues: sampleIssues()}, out: []byte("done\n")}
	m := New(src)
	mod, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)
	// Simulate: user pressed enter to open detail view.
	m.mode = modeDetail

	model, _ := m.Update(rawBDMsg{args: "ready", out: src.out})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Errorf("rawBDMsg arriving in modeDetail should not switch modes; got %v", m.mode)
	}
	if !strings.Contains(m.status, "bd output discarded") {
		t.Errorf("status should announce the discarded output; got %q", m.status)
	}
	if m.outputText != "" {
		t.Errorf("outputText should be cleared when result is discarded; got %q", m.outputText)
	}
}

func TestCommandPalette_OutputOverlayClosesOnEsc(t *testing.T) {
	src := &stubRawBD{stubSource: stubSource{issues: sampleIssues()}, out: []byte("hi\n")}
	m := New(src)
	mod, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = mod.(Model)
	model, _ := m.Update(rawBDMsg{args: "ready", out: src.out})
	m = model.(Model)
	if m.mode != modeOutput {
		t.Fatalf("setup: expected modeOutput; got %v", m.mode)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("esc should return to modeList; got %v", m.mode)
	}
	if m.outputText != "" {
		t.Errorf("outputText should be cleared; got %q", m.outputText)
	}
}

func TestCommandPalette_EscRestoresFilterPrompt(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	if m.input.Prompt != ":" {
		t.Errorf("setup: expected ':' prompt; got %q", m.input.Prompt)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("esc should return to modeList; got %v", m.mode)
	}
	// Pressing / next should land in the filter prompt — its
	// label/placeholder must be the fuzzy-filter ones again, not
	// the ":" leftover.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	if m.input.Prompt != "/ " {
		t.Errorf("/ after :-cancel should restore filter prompt; got %q", m.input.Prompt)
	}
}

func TestCommandPalette_FilterSaveGuards(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	cases := []struct {
		name     string
		cmd      string
		query    string
		wantText string
	}{
		{"missing name", "filter save", "rotate", "missing alias name"},
		{"no active query", "filter save myalias", "", "no active query to save"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &stubSource{issues: sampleIssues()}
			m := applyFetched(New(src), src)
			m.query = tc.query

			model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
			m = model.(Model)
			for _, r := range tc.cmd {
				model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				m = model.(Model)
			}
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = model.(Model)
			if !strings.Contains(m.status, tc.wantText) {
				t.Errorf("status %q should contain %q", m.status, tc.wantText)
			}
		})
	}
}

func TestCommandPalette_SortReverseAndAxes(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Priority: 2},
		{ID: "a-2", Priority: 0},
		{ID: "a-3", Priority: 1},
	}}

	t.Run("sort priority sets axis", func(t *testing.T) {
		m := applyFetched(New(src), src)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "sort priority" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(Model)
		if m.sortBy != sortPriority {
			t.Errorf(":sort priority should set sortPriority; got %v", m.sortBy)
		}
	})

	t.Run("bare sort is usage error", func(t *testing.T) {
		m := applyFetched(New(src), src)
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "sort" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(Model)
		if !strings.Contains(m.status, "axis required") {
			t.Errorf("bare :sort should surface a usage error; got %q", m.status)
		}
		if m.sortBy != sortNone {
			t.Errorf("bare :sort should NOT change axis; got %v", m.sortBy)
		}
	})

	t.Run("reverse flips active direction", func(t *testing.T) {
		m := applyFetched(New(src), src)
		// First set an axis via :sort.
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "sort priority" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(Model)
		// Then :reverse.
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		m = model.(Model)
		for _, r := range "reverse" {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(Model)
		if !m.sortDesc {
			t.Errorf(":reverse should set sortDesc=true; got %v", m.sortDesc)
		}
	})
}

func TestCommandPalette_FilterListShowsSorted(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	aliases := filters.Aliases{Aliases: map[string]string{
		"zeta":  "z",
		"alpha": "a",
	}}
	m := applyFetched(New(src).WithFilterAliases(aliases), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter list" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeOutput {
		t.Fatalf(":filter list should enter modeOutput; got %v", m.mode)
	}
	// Alphabetical: alpha before zeta.
	alphaIdx := strings.Index(m.outputText, "@alpha")
	zetaIdx := strings.Index(m.outputText, "@zeta")
	if alphaIdx < 0 || zetaIdx < 0 {
		t.Errorf("both aliases should appear; got %q", m.outputText)
	}
	if alphaIdx > zetaIdx {
		t.Errorf("aliases should be sorted alphabetically; got %q", m.outputText)
	}
}

func TestCommandPalette_FilterListEmptyShowsHint(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src).WithFilterAliases(filters.Aliases{}), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter list" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode == modeOutput {
		t.Errorf("empty alias list should NOT open the overlay; got modeOutput")
	}
	if !strings.Contains(m.status, "no aliases saved") {
		t.Errorf("status should explain the empty list; got %q", m.status)
	}
}

func TestCommandPalette_FilterRemoveDeletesAndPersists(t *testing.T) {
	// Persist to a tempdir so the test doesn't touch the user's
	// real filters.json. filters.Save resolves DefaultPath at
	// the call site.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	src := &stubSource{issues: sampleIssues()}
	aliases := filters.Aliases{Aliases: map[string]string{"blocked": "status=blocked"}}
	m := applyFetched(New(src).WithFilterAliases(aliases), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter remove blocked" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if _, ok := m.filterAliases.Aliases["blocked"]; ok {
		t.Errorf("alias should be removed from in-memory map; got %v", m.filterAliases.Aliases)
	}
	// Confirm persistence: load from disk.
	path, _ := filters.DefaultPath()
	a, err := filters.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := a.Aliases["blocked"]; ok {
		t.Errorf("alias should be removed from disk; got %v", a.Aliases)
	}
}

func TestCommandPalette_FilterRemovePreservesInMemoryOnPersistFailure(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	// Point XDG at a tempdir but THEN make the wyk subdir
	// read-only so filters.Save fails with a real permission
	// error. The persist-failure path should leave
	// m.filterAliases untouched so :filter list still shows the
	// alias and the on-disk state matches the in-memory view.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wykDir := filepath.Join(dir, "wyk")
	if err := os.MkdirAll(wykDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wykDir, "filters.json"), []byte(`{"version":1,"aliases":{"blocked":"status=blocked"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wykDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(wykDir, 0o755) })

	src := &stubSource{issues: sampleIssues()}
	aliases := filters.Aliases{Version: 1, Aliases: map[string]string{"blocked": "status=blocked"}}
	m := applyFetched(New(src).WithFilterAliases(aliases), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter remove blocked" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	if !strings.Contains(m.status, "failed") {
		t.Errorf("status should announce failure; got %q", m.status)
	}
	if _, ok := m.filterAliases.Aliases["blocked"]; !ok {
		t.Errorf("in-memory map should still contain the alias on persist failure; got %v", m.filterAliases.Aliases)
	}
}

func TestCommandPalette_FilterRemoveMissingNameUsage(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src).WithFilterAliases(filters.Aliases{Aliases: map[string]string{"x": "y"}}), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter remove" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if !strings.Contains(m.status, "missing alias name") {
		t.Errorf("status should explain usage; got %q", m.status)
	}
	if _, ok := m.filterAliases.Aliases["x"]; !ok {
		t.Errorf("missing-name remove should not touch existing aliases; got %v", m.filterAliases.Aliases)
	}
}

func TestCommandPalette_FilterSavePersistsAlias(t *testing.T) {
	// :filter save <name> should persist m.query as @name. Point
	// XDG at a tempdir so the test doesn't touch the user's
	// config — Save resolves DefaultPath() at dispatch time.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.query = "rotate"

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	m = model.(Model)
	for _, r := range "filter save myrot" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	// Read the file back through filters.Load to confirm.
	path, _ := filters.DefaultPath()
	a, err := filters.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a.Aliases["myrot"] != "rotate" {
		t.Errorf("expected myrot → rotate; got %v", a.Aliases)
	}
}

func TestFilterAlias_ExpandsAtNameToStoredQuery(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "rotate password"},
		{ID: "a-2", Title: "deploy preview"},
	}}
	aliases := filters.Aliases{Aliases: map[string]string{
		"rot": "rotate",
	}}
	m := applyFetched(New(src).WithFilterAliases(aliases), src)

	// Open / prompt and type "@rot" then enter.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "@rot" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.query != "rotate" {
		t.Errorf("@rot should expand to 'rotate'; got %q", m.query)
	}
	if len(m.visible) != 1 || m.visible[0].ID != "a-1" {
		t.Errorf("expanded query should match a-1 only; got %d rows", len(m.visible))
	}
}

func TestFilterAlias_MissSurfacesStatusBanner(t *testing.T) {
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src).WithFilterAliases(filters.Aliases{}), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = model.(Model)
	for _, r := range "@nope" {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.query != "@nope" {
		t.Errorf("miss should keep raw query; got %q", m.query)
	}
	if !strings.Contains(m.status, "no filter alias for @nope") {
		t.Errorf("status should explain the miss; got %q", m.status)
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

func TestApplySort_SortByUpdatedNewestFirst(t *testing.T) {
	older := []beads.Issue{
		{ID: "a-1", UpdatedAt: mustParse("2026-01-01T00:00:00Z")},
		{ID: "a-2", UpdatedAt: mustParse("2026-03-01T00:00:00Z")},
		{ID: "a-3", UpdatedAt: mustParse("2026-02-01T00:00:00Z")},
	}
	applySort(older, sortUpdated, false)
	if older[0].ID != "a-2" || older[1].ID != "a-3" || older[2].ID != "a-1" {
		t.Errorf("updated sort should be newest-first; got order %s %s %s",
			older[0].ID, older[1].ID, older[2].ID)
	}
}

func mustParse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestSetPriorityCap_ResetsCursorAndReclampsScroll(t *testing.T) {
	// With a long list and the cursor parked deep in it, applying
	// a priority cap should pull the cursor back to row 0 and
	// re-clamp scroll. Without these, a regression could leave
	// the cursor pointing past the now-shorter visible slice.
	issues := make([]beads.Issue, 40)
	for i := range issues {
		issues[i] = beads.Issue{
			ID:       fmt.Sprintf("a-%d", i+1),
			Title:    fmt.Sprintf("row %d", i+1),
			Priority: 3, // all P3 so the cap to P1 will yield zero rows
		}
	}
	// Add one P0 so the cap=1 path has something to show.
	issues[0].Priority = 0
	src := &stubSource{issues: issues}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	// Drive cursor down so it's scrolled into the middle.
	for i := 0; i < 20; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	if m.cursor == 0 {
		t.Fatal("setup: cursor should be > 0 before applying the cap")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("setPriorityCap should reset cursor to 0; got %d", m.cursor)
	}
	if m.scroll > 0 && m.scroll >= len(m.visible) {
		t.Errorf("scroll left past the now-shorter visible slice; scroll=%d visible=%d",
			m.scroll, len(m.visible))
	}
}

func TestSetSortKey_ResetsCursorAndReclampsScroll(t *testing.T) {
	// Same shape as the priority test but for the sort cycle:
	// pressing s while the cursor is parked deep must pull it
	// back to 0 and re-clamp scroll.
	issues := make([]beads.Issue, 40)
	for i := range issues {
		issues[i] = beads.Issue{
			ID:       fmt.Sprintf("a-%d", i+1),
			Title:    fmt.Sprintf("row %d", i+1),
			Priority: i % 4,
		}
	}
	src := &stubSource{issues: issues}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 14})
	m = model.(Model)
	m = applyFetched(m, src)
	for i := 0; i < 20; i++ {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = model.(Model)
	}
	if m.cursor == 0 {
		t.Fatal("setup: cursor should be > 0 before pressing s")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("setSortKey should reset cursor to 0; got %d", m.cursor)
	}
	if m.scroll > 0 && m.scroll >= len(m.visible) {
		t.Errorf("scroll left past the visible slice; scroll=%d visible=%d",
			m.scroll, len(m.visible))
	}
}

func TestRenderHeader_DecoratesActiveSortColumn(t *testing.T) {
	// Sort by priority should put ↑ next to P; sort by updated
	// should put ↓ next to Updated. sortNone leaves the header
	// arrow-free.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	if got := m.renderHeader(); strings.ContainsAny(got, "↑↓") {
		t.Errorf("sortNone should not decorate any column; got:\n%s", got)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if got := m.renderHeader(); !strings.Contains(got, "P↑") {
		t.Errorf("sortPriority should decorate the P column with ↑; got:\n%s", got)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if got := m.renderHeader(); !strings.Contains(got, "Updated↓") {
		t.Errorf("sortUpdated should decorate the Updated column with ↓; got:\n%s", got)
	}
}

func TestRenderHeader_DecoratedColumnsStayWithinTheirWidth(t *testing.T) {
	// Regression for the 1259 LOW: "Updated↓" used to overflow
	// colUpdated=7 and push Title one column right of the data
	// rows. Pin the invariant: under every sort state, every
	// decorated column header renders at exactly its configured
	// width — never more.
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "x"},
		{ID: "beta-9", Repo: "beta", Title: "y"},
	}}
	m := applyFetched(New(src), src)

	cases := []struct {
		label string
		sort  sortKey
	}{
		{"none", sortNone},
		{"priority", sortPriority},
		{"updated", sortUpdated}, // the regression case
		{"repo", sortRepo},
		{"id", sortID},
	}
	// Anchor against an absolute expectation derived from the
	// column-width constants. Self-referential baselines (using
	// sortNone as the source of truth for the other cases) would
	// silently mask a layout bug that shifted the baseline itself
	// — every case here, including sortNone, is validated against
	// the same expected rune-column.
	const sep = 2 // two-space separator after each column
	expectedTitleRune := 2 /* leading cursor */ +
		colResp + sep +
		colWyk + sep +
		colRepo + sep +
		colBranch + sep +
		colID + sep +
		colType + sep +
		colStatus + sep +
		colPrio + sep +
		colUpdated + sep

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			m.sortBy = c.sort
			out := stripANSI(m.renderHeader())
			byteAt := strings.Index(out, "Title")
			if byteAt < 0 {
				t.Fatalf("Title not found in header:\n%s", out)
			}
			// strings.Index returns a byte offset, but multi-byte
			// arrows (↑↓) would inflate it relative to visual
			// columns. Measure rune-count to get the true column
			// position.
			runesAt := utf8.RuneCountInString(out[:byteAt])
			if runesAt != expectedTitleRune {
				t.Errorf("sort=%s: Title at rune-col %d, want %d (header overflow!)",
					c.label, runesAt, expectedTitleRune)
			}
		})
	}
}

// stripANSI removes ANSI SGR escape sequences (\033[...m) — the
// color/style sequences lipgloss emits — so visual widths can be
// compared. It's narrow on purpose: only handles SGR (ending in
// 'm'), which is all lipgloss produces in this codebase. A
// truncated/malformed ESC sequence with no terminating 'm' would
// be consumed greedily; in practice that doesn't happen for
// our inputs, so the simple implementation is fine.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip until 'm'
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // skip the 'm' itself
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestMouseWheel_ScrollsCursorUpAndDown(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.cursor != 0 {
		t.Fatalf("setup: cursor should start at 0; got %d", m.cursor)
	}

	// Wheel down → cursor++.
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 1 {
		t.Errorf("wheel down should advance cursor; got %d, want 1", m.cursor)
	}

	// Wheel up → cursor--.
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("wheel up should retreat cursor; got %d, want 0", m.cursor)
	}

	// Wheel up at row 0 is a no-op (clamps).
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("wheel up at row 0 should clamp; got %d", m.cursor)
	}
}

func TestMouseLeftClick_LandsCursorOnTargetRow(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// rowsStartY for default state: 1 (title) + 1 (blank) + 1 (header) = 3
	// Click on Y=5 → target row = 0 + (5 - 3) = 2.
	rowY := m.rowsStartY() + 2
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: rowY,
	})
	m = model.(Model)
	if m.cursor != 2 {
		t.Errorf("left-click at row offset 2 should land cursor on row 2; got %d", m.cursor)
	}
}

func TestMouseLeftClick_OutsideTableIsNoOp(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// Click on Y=0 (the title line). Should NOT change cursor.
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 0,
	})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("click in header area should be a no-op; cursor moved to %d", m.cursor)
	}

	// Click far below the last row → clamped (target out of range, no-op).
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 999,
	})
	m = model.(Model)
	if m.cursor != 0 {
		t.Errorf("click past end of list should be a no-op; cursor moved to %d", m.cursor)
	}
}

func TestMouseLeftClick_OnMoreBelowHintIsNoOp(t *testing.T) {
	// When the row window is smaller than len(visible), viewList
	// renders a "↓ N more below" hint line just past the last row.
	// Clicking that hint used to map to the next out-of-window row
	// (target = scroll + rowY, which is a valid index whenever the
	// view is partially scrolled), producing a surprising downward
	// cursor jump. The clamp now treats such clicks as no-ops.
	src := &stubSource{issues: manyIssues(50)}
	m := New(src)
	// Constrain height so bodyHeight is small and the hint line
	// actually renders.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m = model.(Model)
	m = applyFetched(m, src)
	if m.bodyHeight() >= len(m.visible) {
		t.Fatalf("test premise: bodyHeight (%d) should be < visible (%d) so a hint line renders", m.bodyHeight(), len(m.visible))
	}

	preCursor := m.cursor
	// Click one cell past the body — the "↓ N more below" line.
	hintY := m.rowsStartY() + m.bodyHeight()
	model, _ = m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: hintY,
	})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("click on more-below hint should be a no-op; cursor moved %d → %d", preCursor, m.cursor)
	}
}

func TestMouse_IgnoredOutsideListMode(t *testing.T) {
	// Detail / help / modal modes own the canvas; mouse should
	// not steal focus and reset the list cursor.
	src := &stubSource{issues: manyIssues(20)}
	m := applyFetched(New(src), src)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = model.(Model)
	if m.mode != modeHelp {
		t.Fatalf("setup: expected modeHelp; got %v", m.mode)
	}
	preCursor := m.cursor
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("mouse should be ignored in modeHelp; cursor changed from %d to %d", preCursor, m.cursor)
	}
}

func TestDetailView_MouseWheelScrollsViewport(t *testing.T) {
	// modeDetail wires mouse wheel events to detailVP so a long
	// description doesn't force the user to reach for the
	// keyboard. The cursor in modeList must NOT move on the
	// wheel event — it's owned by the viewport while the detail
	// view is open.
	src := &stubSource{issues: manyIssues(20)}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)

	// Open the cursor row's detail view.
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("enter should open modeDetail; got %v", m.mode)
	}
	preCursor := m.cursor
	preYOff := m.detailVP.YOffset

	// Wheel-down should leave the list cursor alone and forward
	// to the viewport (which may or may not actually scroll
	// depending on body length, but the routing alone is what we
	// care about).
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(Model)
	if m.cursor != preCursor {
		t.Errorf("mouse wheel in detail view must not move the list cursor; was %d, now %d", preCursor, m.cursor)
	}
	// detailVP.Update doesn't guarantee a YOffset change for a
	// short body, but the routing should at least not panic and
	// should consume the event silently.
	_ = preYOff
}

func TestRowsStartY_AccountsForChipStrip(t *testing.T) {
	src := &stubSource{issues: manyIssues(20)}
	m := applyFetched(New(src), src)
	baseY := m.rowsStartY()

	// Activate a priority cap → chip strip appears, rowsStartY
	// shifts down by one.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = model.(Model)
	if m.rowsStartY() != baseY+1 {
		t.Errorf("chip strip should bump rowsStartY by 1; was %d, now %d", baseY, m.rowsStartY())
	}
}
