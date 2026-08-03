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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// This file holds shared fixtures (stubSource / stubMutator / sampleIssues) plus
// the tests that exercise the Model as a whole rather than one mode.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

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

	// Injectable failures so tests can drive the write-error paths
	// (e.g. the detail-view optimistic-mutation rollback). nil = succeed.
	reopenErr error
	noteErr   error
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
	if s.noteErr != nil {
		return s.noteErr
	}
	s.notes = append(s.notes, labelOp{i.ID, text})
	return nil
}

func (s *stubMutator) Create(_ context.Context, repo, title, assignee string) (string, error) {
	s.created = append(s.created, labelOp{repo, title})
	s.createdAssignees = append(s.createdAssignees, assignee)
	return "new-id", nil
}

func (s *stubMutator) Reopen(_ context.Context, i beads.Issue) error {
	if s.reopenErr != nil {
		return s.reopenErr
	}
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

func visibleIDs(issues []beads.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.ID)
	}
	return out
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

// --- Phase 2.B: write-action tests ---------------------------------

// applyMutatorFetched is the stubMutator equivalent of applyFetched.
func applyMutatorFetched(m Model, s *stubMutator) Model {
	model, _ := m.Update(fetchedMsg{preset: m.preset, issues: s.issues})
	return model.(Model)
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

// enterDetailWithMutator fetches the sample issues into a
// mutator-backed model and opens the cursor row's detail view.
func enterDetailWithMutator(t *testing.T, s *stubMutator) Model {
	t.Helper()
	m := applyMutatorFetched(New(s), s)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeDetail {
		t.Fatalf("setup: expected modeDetail, got %v", m.mode)
	}
	return m
}

func TestReadOnlySourceShowsHintInsteadOfWriting(t *testing.T) {
	// The plain stubSource does NOT implement Mutator; pressing write
	// keys should produce a "read-only" banner instead of crashing.
	s := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(s), s)

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
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

func TestSessionColumn_RendersShortSessionFromLabel(t *testing.T) {
	// An issue stamped by `wyk create` carries session:<id>; the column
	// shows the first colSession runes. An unstamped issue is blank.
	stamped := beads.Issue{ID: "a-1", Title: "stamped", Status: "open",
		Labels: []string{"src:agent", "session:abcdef0123456789"}}
	bare := beads.Issue{ID: "a-2", Title: "bare", Status: "open", Labels: []string{}}

	if got := sessionShort(stamped); got != "abcdef01" {
		t.Errorf("sessionShort = %q, want first 8 runes %q", got, "abcdef01")
	}
	if got := sessionShort(bare); got != "" {
		t.Errorf("sessionShort on an unstamped issue = %q, want empty", got)
	}

	src := &stubSource{issues: []beads.Issue{stamped, bare}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := stripANSI(m.View())
	if !strings.Contains(out, "Session") {
		t.Errorf("header should include the Session column:\n%s", out)
	}
	if !strings.Contains(out, "abcdef01") {
		t.Errorf("row should show the short session; got:\n%s", out)
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

func idsOfIssues(issues []beads.Issue) []string {
	out := make([]string, len(issues))
	for i, x := range issues {
		out[i] = x.ID
	}
	return out
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

func TestSubstringRuneIdxs(t *testing.T) {
	// The rune indices feeding the repo/branch/ID column highlight.
	cases := []struct {
		s, q string
		want []int
	}{
		{"android", "droid", []int{2, 3, 4, 5, 6}}, // case-sensitive position
		{"Android", "andro", []int{0, 1, 2, 3, 4}}, // case-insensitive
		{"ebay-watchlist-watch", "watch", []int{5, 6, 7, 8, 9}},
		{"android", "xyz", nil}, // no match
		{"android", "", nil},    // empty query
		{"", "android", nil},    // empty value
	}
	for _, c := range cases {
		got := substringRuneIdxs(c.s, c.q)
		if len(got) != len(c.want) {
			t.Errorf("substringRuneIdxs(%q,%q) = %v, want %v", c.s, c.q, got, c.want)
			continue
		}
		for k := range got {
			if got[k] != c.want[k] {
				t.Errorf("substringRuneIdxs(%q,%q) = %v, want %v", c.s, c.q, got, c.want)
				break
			}
		}
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

// drainCmd walks a tea.Cmd (and any nested tea.BatchMsg) so every
// inner func() runs and produces its side effects — used by the
// fsEventMsg suspension and tick tests to detect whether a fetchCmd
// is hiding inside a batch (the stub's synchronous Fetch bumps
// src.calls); a single non-batch cmd is just consumed. Each command
// runs in its own goroutine with a short deadline so a long-lived
// timer like tickCmd's tea.Tick(refreshInterval) — which would
// otherwise block the test for the full poll interval — is abandoned
// once it's clear it won't return promptly. The asserted side effects
// all complete well within the deadline.
func drainCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				drainCmd(c)
			}
		}
	case <-time.After(time.Second):
		// A slow command (e.g. tea.Tick on the poll interval) — its
		// side effects, if any, have already run synchronously before
		// the blocking wait; nothing left to drain.
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

func TestApplySort_SortByUpdatedNewestFirst(t *testing.T) {
	older := []beads.Issue{
		{ID: "a-1", UpdatedAt: mustParse("2026-01-01T00:00:00Z")},
		{ID: "a-2", UpdatedAt: mustParse("2026-03-01T00:00:00Z")},
		{ID: "a-3", UpdatedAt: mustParse("2026-02-01T00:00:00Z")},
	}
	applySort(older, sortUpdated, false, nil)
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

func TestFetchedMsg_ClearsCacheStaleAndPersistsSnapshot(t *testing.T) {
	// After the live fetch lands, the on-screen rows are no
	// longer stale, and the snapshot should land on disk so the
	// next launch can warm-start. Verify both.
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "last-fetch.json")
	src := &stubSource{}
	seed := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now().Add(-time.Hour),
		Issues:  []beads.Issue{{ID: "stale"}},
	}
	m := New(src).WithCacheSnapshot(seed, cachePath)
	if !m.cacheStale {
		t.Fatal("setup: WithCacheSnapshot didn't mark m.cacheStale")
	}

	fresh := []beads.Issue{{ID: "wyk-7", Title: "fresh"}}
	model, cmd := m.Update(fetchedMsg{preset: filter.PresetAll, issues: fresh})
	m = model.(Model)
	if m.cacheStale {
		t.Error("cacheStale should clear after a successful fetchedMsg")
	}
	if len(m.all) != 1 || m.all[0].ID != "wyk-7" {
		t.Errorf("m.all should reflect the fresh fetch; got %+v", m.all)
	}
	// The save is dispatched as a tea.Cmd so it runs off the
	// event loop; execute it inline here for test determinism.
	if cmd == nil {
		t.Fatal("expected a save cmd from successful fetchedMsg with cachePath set")
	}
	_ = cmd()

	// The persisted snapshot is the fresh data, not the seed.
	got, err := LoadCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCache after fetch: %v", err)
	}
	if len(got.Issues) != 1 || got.Issues[0].ID != "wyk-7" {
		t.Errorf("persisted snapshot should hold the fresh fetch; got %+v", got.Issues)
	}
}

// stubDepSource is a Source that also satisfies DepLister, returning
// canned dependency edges per issue ID so the topological deps-sort
// can be exercised without a real bd binary. edges maps an issue ID
// to the IDs it directly depends on; ListDeps shells those out as
// slim Issues (only the ID is load-bearing for the sort). failIDs
// forces an error for specific IDs so the error-degradation path is
// testable.
type stubDepSource struct {
	stubSource
	edges   map[string][]string
	failIDs map[string]bool
}

func (s *stubDepSource) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	if s.failIDs[id] {
		return nil, errors.New("boom")
	}
	var out []beads.Issue
	for _, dep := range s.edges[id] {
		out = append(out, beads.Issue{ID: dep})
	}
	return out, nil
}

// ListDependents is the reverse-edge twin of ListDeps so stubDepSource
// satisfies the (now two-method) DepLister interface; without it the
// deps sort silently falls back to the count proxy. It inverts the
// edges map: every node that lists id among its deps is a dependent.
func (s *stubDepSource) ListDependents(_ context.Context, id string) ([]beads.Issue, error) {
	if s.failIDs[id] {
		return nil, errors.New("boom")
	}
	var out []beads.Issue
	for from, deps := range s.edges {
		for _, dep := range deps {
			if dep == id {
				out = append(out, beads.Issue{ID: from})
			}
		}
	}
	return out, nil
}

// pressSortToDeps cycles the `s` key until the deps axis is active,
// matching how TestSortCycle drives the keymap. Returns the settled
// model (the last setSortKey's resolver Cmd is re-derived by the
// caller via maybeResolveDeps, so the Cmd dropped here is harmless).
func pressSortToDeps(m Model) Model {
	for m.sortBy != sortDeps {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = model.(Model)
	}
	return m
}

// resolveDepsForTest drives the deps sort to a fully-resolved state:
// it switches to the deps axis (which dispatches the resolver Cmd),
// runs that Cmd synchronously, and feeds the resulting
// depsResolvedMsg back through Update so m.depCache is populated and
// m.visible is the real topological order. Returns the settled model.
func resolveDepsForTest(t *testing.T, m Model) Model {
	t.Helper()
	m = pressSortToDeps(m)
	cmd := m.maybeResolveDeps()
	if cmd == nil {
		return m // nothing to resolve (already cached / no lister)
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	return updated.(Model)
}

// applyResolveCmd executes a (possibly batched) Cmd returned by
// Update and feeds any depsResolvedMsg back into the model, so a test
// can observe the topo order after an async resolution lands. Handles
// the tea.Batch wrapper the fetchedMsg handler emits (cacheCmd +
// depsCmd) and a bare resolve Cmd alike.
func applyResolveCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range v {
			m = applyResolveCmd(t, m, c)
		}
	case depsResolvedMsg:
		updated, _ := m.Update(v)
		m = updated.(Model)
	}
	return m
}

func TestStatusForAction_OmitsReopen(t *testing.T) {
	// Reopen is intentionally not patched — the reopened issue stays in
	// the open list, so refreshDepCachesFromList picks up bd's actual
	// new status rather than a guessed "open".
	if _, ok := statusForAction("reopen"); ok {
		t.Error("reopen should NOT be in statusForAction (covered by the list-refresh path)")
	}
	if st, ok := statusForAction("close"); !ok || st != "closed" {
		t.Errorf("close should map to closed; got %q,%v", st, ok)
	}
	if st, ok := statusForAction("defer"); !ok || st != "deferred" {
		t.Errorf("defer should map to deferred; got %q,%v", st, ok)
	}
}

// detailWithLinks returns a model in modeDetail viewing a-1, whose
// cached links are deps [a-2, a-3] then dependents [a-9].
func detailWithLinks(t *testing.T) Model {
	t.Helper()
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.mode = modeDetail
	m.detailIssue = beads.Issue{ID: "a-1", Title: "root"}
	m.depCache["a-1"] = []beads.Issue{{ID: "a-2", Title: "dep1", Status: "open"}, {ID: "a-3", Title: "dep2", Status: "open"}}
	m.dependentCache["a-1"] = []beads.Issue{{ID: "a-9", Title: "dependent", Status: "open"}}
	return m
}

func TestOpenDetailLink_PushesStackAndSwaps(t *testing.T) {
	m := detailWithLinks(t)
	m.detailLinkIdx = 1 // a-3
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.detailIssue.ID != "a-3" {
		t.Errorf("Enter should open the highlighted link; detailIssue=%q want a-3", m.detailIssue.ID)
	}
	if len(m.detailStack) != 1 || m.detailStack[0].ID != "a-1" {
		t.Errorf("the prior issue should be pushed; stack=%+v", m.detailStack)
	}
	if m.detailLinkIdx != -1 {
		t.Errorf("link selection should reset on open; got %d", m.detailLinkIdx)
	}
	if m.mode != modeDetail {
		t.Errorf("should stay in detail mode after drilling in; mode=%v", m.mode)
	}
}

func TestEnterWithNoSelection_BacksOut(t *testing.T) {
	// Enter with nothing highlighted preserves the old enter==back feel.
	m := detailWithLinks(t) // detailLinkIdx == -1
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.mode != modeList {
		t.Errorf("Enter with no link selected should back out to the list; mode=%v", m.mode)
	}
}

// stubDetailSource is a stubSource that also satisfies Detailer, so
// tests can observe the Detail() enrichment dispatch.
type stubDetailSource struct {
	stubSource
	detailed  []string
	detailErr error // when set, Detail returns this error
}

func (s *stubDetailSource) Detail(_ context.Context, i beads.Issue) (beads.Issue, error) {
	s.detailed = append(s.detailed, i.ID)
	if s.detailErr != nil {
		return beads.Issue{}, s.detailErr
	}
	i.Notes = "enriched"
	return i, nil
}

// stubMutatorDetailer is both a Mutator and a Detailer, so beginEdit
// (which requires a Mutator and fetches via Detailer) can be exercised
// with a Detail() that fails.
type stubMutatorDetailer struct {
	stubMutator
	detailErr error
}

func (s *stubMutatorDetailer) Detail(_ context.Context, i beads.Issue) (beads.Issue, error) {
	if s.detailErr != nil {
		return beads.Issue{}, s.detailErr
	}
	return i, nil
}

func TestPruneStaleMarks(t *testing.T) {
	// A mark whose issue vanished on a refetch must be dropped so a later
	// bulk op can't target a gone row (would-you-kindly-g00n).
	m := New(&stubSource{})
	m.all = []beads.Issue{{ID: "a-1"}, {ID: "a-2"}}
	m.marked = map[string]bool{"a-1": true, "a-2": true, "gone": true}
	m.pruneStaleMarks()
	if m.marked["gone"] {
		t.Error("stale mark 'gone' should be pruned after refetch")
	}
	if !m.marked["a-1"] || !m.marked["a-2"] {
		t.Error("marks for present rows must be kept")
	}
	if len(m.marked) != 2 {
		t.Errorf("want 2 marks after prune, got %d", len(m.marked))
	}
}

func TestRawWriteWarning(t *testing.T) {
	warns := [][]string{
		{"close", "a-1"},
		{"create", "--title", "x"},
		{"update", "a-1", "--status", "closed"},
		{"dep", "add", "a-1", "a-2"},
		{"label", "remove", "a-1", "human"},
		{"note", "a-1", "hi"},
		// Leading global flags must not suppress the warning (the
		// fail-silent gap roborev #1841 caught).
		{"-C", "/some/dir", "close", "a-1"},
		{"--dir", "/d", "dep", "add", "a-1", "a-2"},
		{"-C=/inline", "close", "a-1"},
	}
	for _, a := range warns {
		if rawWriteWarning(a) == "" {
			t.Errorf("rawWriteWarning(%v) = \"\", want a warning", a)
		}
	}
	quiet := [][]string{
		{"list"},
		{"ready"},
		{"query", "p0"},
		{"show", "a-1"},
		{"dep", "list", "a-1"},
		{"label"},            // bare label = list
		{"-C", "/d", "list"}, // leading flag + read verb stays quiet
		{"close", "a-1", "--dolt-auto-commit=on"}, // explicit: user owns it
		{"close", "a-1", "--dolt-auto-commit=off"},
		{},
	}
	for _, a := range quiet {
		if w := rawWriteWarning(a); w != "" {
			t.Errorf("rawWriteWarning(%v) = %q, want no warning", a, w)
		}
	}
}
