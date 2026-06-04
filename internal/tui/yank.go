package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/clipboard"
)

// This file holds the clipboard "yank" keystrokes (y / Y / * / M / _).
// They share a common shape: guard against an empty/out-of-range
// selection, build a payload, copy via the OSC 52 seam, and flash a
// status banner. Extracted from model.go to keep the clipboard
// concern in one place.

// handleYank copies the cursor issue's full ID to the system
// clipboard via OSC 52 and surfaces a status banner. The full ID
// (not the display-prefix-trimmed version) is what's useful for
// pasting into bd commands or chat — partial IDs would just
// silently fail elsewhere. Empty-list / past-end cursor states
// produce no-ops with a clear status, never a silent failure.
func (m Model) handleYank() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	id := m.visible[m.cursor].ID
	if err := clipboardCopy(id); err != nil {
		m.setStatus("yank failed: " + err.Error())
		// No auto-clear on failure — the user needs to see why.
		return m, nil
	}
	m.setStatus("copied " + id)
	return m, flashClearCmd(m.statusGen)
}

// handleYankRich copies "ID — title" (em-dash separator) so a
// reference pasted into a commit message or chat reads naturally
// without re-typing. Same clipboard path as handleYank; same
// guards and status banner shape.
func (m Model) handleYankRich() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	row := m.visible[m.cursor]
	payload := row.ID
	if title := strings.TrimSpace(row.Title); title != "" {
		payload = row.ID + " — " + title
	}
	if err := clipboardCopy(payload); err != nil {
		m.setStatus("yank failed: " + err.Error())
		return m, nil
	}
	m.setStatus("copied " + payload)
	return m, flashClearCmd(m.statusGen)
}

// handleYankAll copies every visible row's ID to the clipboard,
// newline-separated. "Visible" is post-filter, post-preset — the
// agent's mental model of "the set I'm currently looking at" —
// so the yanked payload matches what's on screen. Empty-list
// produces a no-op with a status banner, never a silent
// clipboard wipe.
func (m Model) handleYankAll() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	ids := make([]string, len(m.visible))
	for i, row := range m.visible {
		ids[i] = row.ID
	}
	payload := strings.Join(ids, "\n")
	if err := clipboardCopy(payload); err != nil {
		m.setStatus("yank failed: " + err.Error())
		return m, nil
	}
	m.setStatus(fmt.Sprintf("copied %d IDs", len(ids)))
	return m, flashClearCmd(m.statusGen)
}

// handleYankMarkdown copies the cursor row as a markdown task
// line: "- [ ] <ID> — <title>" for open rows, "- [x] ..." for
// closed. Whitespace-only titles fall back to bare-ID like
// handleYankRich. Same OSC 52 path as the other yank handlers.
func (m Model) handleYankMarkdown() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	row := m.visible[m.cursor]
	box := "[ ]"
	if row.Status == "closed" {
		box = "[x]"
	}
	payload := "- " + box + " " + row.ID
	if title := strings.TrimSpace(row.Title); title != "" {
		payload += " — " + title
	}
	if err := clipboardCopy(payload); err != nil {
		m.setStatus("yank failed: " + err.Error())
		return m, nil
	}
	m.setStatus("copied " + payload)
	return m, flashClearCmd(m.statusGen)
}

// handleYankAllMarkdown copies every visible row as a newline-
// joined markdown task list — multi-row sibling of
// handleYankMarkdown. Each row gets "- [ ]" or "- [x]" depending
// on Status, and a whitespace-only title falls back to bare ID
// (same fallback as the single-row variants).
func (m Model) handleYankAllMarkdown() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		m.setStatus("nothing to yank")
		return m, flashClearCmd(m.statusGen)
	}
	lines := make([]string, len(m.visible))
	for i, row := range m.visible {
		box := "[ ]"
		if row.Status == "closed" {
			box = "[x]"
		}
		line := "- " + box + " " + row.ID
		if title := strings.TrimSpace(row.Title); title != "" {
			line += " — " + title
		}
		lines[i] = line
	}
	payload := strings.Join(lines, "\n")
	if err := clipboardCopy(payload); err != nil {
		m.setStatus("yank failed: " + err.Error())
		return m, nil
	}
	m.setStatus(fmt.Sprintf("copied %d rows as markdown", len(lines)))
	return m, flashClearCmd(m.statusGen)
}

// clipboardCopy is the seam tests can swap to skip /dev/tty I/O.
// Production points at the real OSC 52 emitter.
var clipboardCopy = clipboard.Copy
