package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestSanitizeInline_StripsAllControls(t *testing.T) {
	in := "pwn\x1b]52;c;ZWdpdA==\x07\x1b[31mred\x1b[0m\ttab\nnl"
	got := sanitizeInline(in)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, '\n') || strings.ContainsRune(got, '\t') || strings.ContainsRune(got, 0x07) {
		t.Errorf("inline must strip ESC/BEL/newline/tab; got %q", got)
	}
	// Printable remnants survive (defanged), e.g. the literal "[31m".
	if !strings.Contains(got, "red") || !strings.Contains(got, "[31m") {
		t.Errorf("printable text should remain; got %q", got)
	}
}

func TestSanitizeBlock_KeepsNewlineTabOnly(t *testing.T) {
	got := sanitizeBlock("a\x1bb\nc\td\x07e")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Errorf("block must strip ESC/BEL; got %q", got)
	}
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Errorf("block must keep newline/tab; got %q", got)
	}
}

func TestRender_NoEscapeSurvives(t *testing.T) {
	evil := "x\x1b]52;c;ZWdpdA==\x07\x1b[31m"
	src := &stubSource{issues: []beads.Issue{{
		ID: "a-1", Title: evil, Description: evil, Notes: evil, Status: "open",
		// roborev #1848: labels and repo/branch are untrusted bd content too.
		Labels: []string{evil, "human"}, Repo: evil, Branch: evil,
	}}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = model.(Model)
	m = applyFetched(m, src)
	if strings.ContainsRune(m.View(), 0x1b) {
		t.Error("list View leaked a raw ESC from a hostile title/repo/branch")
	}
	// Detail view (description + notes + labels rendered).
	dm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = dm.(Model)
	if strings.ContainsRune(m.View(), 0x1b) {
		t.Error("detail View leaked a raw ESC from a hostile description/notes/labels")
	}
}
