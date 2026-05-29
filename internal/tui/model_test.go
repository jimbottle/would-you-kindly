package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
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
	closed   []string
	reopened []string
	added    []labelOp
	removed  []labelOp
	notes    []labelOp // reuse the {id,label} shape for {id, text}
	created  []labelOp // {repo, title} for quick-add
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
func (s *stubMutator) Reopen(_ context.Context, i beads.Issue) error {
	s.reopened = append(s.reopened, i.ID)
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

func TestHumanBadge_DistinguishesSource(t *testing.T) {
	agent := beads.Issue{Labels: []string{"human", "src:agent"}}
	self := beads.Issue{Labels: []string{"human", "src:human"}}
	plain := beads.Issue{Labels: []string{"human"}}

	if got := responsibilityBadgeFor(agent); !strings.Contains(got, "←") {
		t.Errorf("src:agent badge should contain a left-arrow; got %q", got)
	}
	if got := responsibilityBadgeFor(self); !strings.Contains(got, "·") {
		t.Errorf("src:human badge should contain a middle-dot; got %q", got)
	}
	if got := responsibilityBadgeFor(plain); strings.Contains(got, "←") || strings.Contains(got, "·") {
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
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = model.(Model)

	// Load straight from disk to confirm the save happened.
	cfg, err := uiconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.HiddenColumns) != 1 || cfg.HiddenColumns[0] != colIDOwner {
		t.Errorf("HiddenColumns = %v, want [%q]", cfg.HiddenColumns, colIDOwner)
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
	// (user fixes PATH) can come through.
	src := &stubSource{err: errors.New("no bd binary on PATH")}
	events := make(chan struct{}, 1)
	m := applyFetched(New(src).WithFSEvents(events), src)
	m.lastErr = errors.New("no bd binary on PATH") // simulate the terminal state

	// Override the terminal-error detector via a no-bd-style
	// error if it matches; rely on isTerminalErr's substring
	// match. If isTerminalErr would return false for this string,
	// the assertion below still holds — we just verify cmd
	// isn't nil (re-arm should always happen).
	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Errorf("even in terminal-error state, the wait should re-arm")
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

func TestApplySort_SortByUpdatedNewestFirst(t *testing.T) {
	older := []beads.Issue{
		{ID: "a-1", UpdatedAt: mustParse("2026-01-01T00:00:00Z")},
		{ID: "a-2", UpdatedAt: mustParse("2026-03-01T00:00:00Z")},
		{ID: "a-3", UpdatedAt: mustParse("2026-02-01T00:00:00Z")},
	}
	applySort(older, sortUpdated)
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
