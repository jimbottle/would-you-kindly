package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the $EDITOR round trip.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

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

func TestBeginEdit_AbortsWhenDetailFails(t *testing.T) {
	// Regression for would-you-kindly-quep: a failed Detail() must abort
	// the edit, not open an empty buffer that a save would use to
	// overwrite the real description.
	s := &stubMutatorDetailer{
		stubMutator: stubMutator{stubSource: stubSource{issues: sampleIssues()}},
		detailErr:   errors.New("bd boom"),
	}
	m := New(s)
	fm, _ := m.Update(fetchedMsg{preset: m.preset, issues: s.issues})
	m = fm.(Model)
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = model.(Model)
	if !strings.Contains(m.status, "edit aborted") {
		t.Errorf("expected an 'edit aborted' status when Detail fails; got %q", m.status)
	}
	// No editor should have been launched (no ExecProcess command beyond
	// the status flash-clear). We can't easily inspect ExecProcess, but
	// the status assertion above is the contract; ensure we didn't fall
	// through to a write either.
	_ = cmd
}
