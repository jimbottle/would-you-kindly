package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the clipboard yank family.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

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
