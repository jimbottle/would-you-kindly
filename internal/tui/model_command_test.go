package tui

import (
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
	"github.com/jimbottle/would-you-kindly/internal/filters"
)

// This file holds the `:` command palette and saved filter aliases.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

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
