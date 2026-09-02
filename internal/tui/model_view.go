package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
)

// The list surface: View and its chrome, the help/columns
// overlays, row/header rendering, and the width/geometry
// helpers they share.

// openHelp captures the current mode and switches to modeHelp; the
// help overlay's dismiss handler restores the captured mode.
func (m Model) openHelp() (tea.Model, tea.Cmd) {
	m.helpReturnMode = m.mode
	m.mode = modeHelp
	return m, nil
}

func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	if keyHit(msg, m.keys.Mouse) {
		// The help overlay is where the binding is advertised — and
		// copying a keybinding line out of it is a plausible
		// click-drag case — so the toggle works here too.
		return m.toggleMouse()
	}
	switch msg.String() {
	case "esc", "?", "q":
		m.mode = m.helpReturnMode
	}
	return m, nil
}

// updateColumns handles input in the column-visibility overlay.
// Numbers 1-N toggle columns in the toggleableColumns registry
// order; multi-only columns silently no-op while single-repo. esc
// or `o` closes the overlay and persists the new state if a
// uiConfigPath is set. Persistence failure is surfaced as a status
// banner but doesn't block closing — the toggle is still in effect
// for the current session, only the next launch loses it.
func (m Model) updateColumns(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		// Persist before quitting so toggles made in the overlay
		// aren't silently lost when the user closes via ctrl+c
		// instead of esc. Best-effort; we're exiting anyway, so a
		// save error has nowhere useful to surface.
		_ = m.persistColumns()
		return m.quitNow()
	}
	switch msg.String() {
	case "esc", "o", "q":
		if err := m.persistColumns(); err != nil {
			m.setStatus("ui.json save failed: " + err.Error())
		}
		m.mode = modeList
		return m, nil
	case "p":
		// Not a column, but the overlay is the natural home for
		// display toggles: flip the opt-in P-column emphasis. Persisted
		// on overlay close like the column toggles.
		m.priorityEmphasis = !m.priorityEmphasis
		return m, nil
	}
	// Digit toggles. Build the index from the rune so non-digit
	// keystrokes inside the overlay (typing junk by accident) are
	// ignored rather than triggering an out-of-range slot.
	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < len(toggleableColumns) {
				col := toggleableColumns[idx]
				if col.MultiOnly && !m.isMultiRepo() {
					return m, nil // overlay shows the note; treat as no-op
				}
				if m.colsHidden == nil {
					m.colsHidden = map[string]bool{}
				}
				m.colsHidden[col.ID] = !m.colsHidden[col.ID]
			}
		}
	}
	return m, nil
}

// persistColumns serialises m.colsHidden back to ui.json via the
// uiconfig package. Returns nil (best-effort) when no path is set
// — tests and embedded uses can run without a real config file.
func (m Model) persistColumns() error {
	if m.uiConfigPath == "" {
		return nil
	}
	hidden := make([]string, 0, len(m.colsHidden))
	// Walk toggleableColumns rather than ranging the map so the
	// on-disk list order matches the overlay order — easier to
	// hand-edit when a user opens ui.json directly.
	for _, c := range toggleableColumns {
		if m.colsHidden[c.ID] {
			hidden = append(hidden, c.ID)
		}
	}
	return uiconfig.Save(m.uiConfigPath, uiconfig.Config{
		Version:          uiconfig.CurrentVersion,
		HiddenColumns:    hidden,
		PriorityEmphasis: m.priorityEmphasis,
	})
}

// View dispatches to the per-mode renderer.
func (m Model) View() string {
	if m.splitView() {
		return m.viewSplit()
	}
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeConfirmClose, modeDefer, modeNote:
		// These prompts can be opened from either view. When they
		// were opened from the detail view, render them overlaid on
		// it; otherwise they belong to the list (the default below).
		if m.promptReturn == modeDetail {
			return m.viewDetail()
		}
		return m.viewList()
	case modeHelp:
		return m.viewHelp()
	case modeColumns:
		return m.viewColumns()
	case modeOutput:
		return m.viewOutput()
	default:
		return m.viewList()
	}
}

// viewColumns renders the column-visibility overlay. Each
// toggleable column appears as a numbered row with a [x]/[ ]
// checkbox; the number is the toggle key. Multi-only columns
// render greyed out in single-repo mode so a user toggling 4
// (Branch) sees why nothing happens.
func (m Model) viewColumns() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("Columns"))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render(fmt.Sprintf("Press 1-%d to toggle. ID, P, and Title are always shown.", len(toggleableColumns))))
	b.WriteString("\n\n")
	multi := m.isMultiRepo()
	for i, col := range toggleableColumns {
		check := "[ ]"
		if !m.colsHidden[col.ID] {
			check = "[x]"
		}
		line := fmt.Sprintf("  %d. %s  %s", i+1, check, col.Label)
		if col.MultiOnly && !multi {
			line += "  " + helpStyle.Render("(multi-repo only)")
			b.WriteString(helpStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	pchk := "[ ]"
	if m.priorityEmphasis {
		pchk = "[x]"
	}
	fmt.Fprintf(&b, "  p. %s  Priority emphasis  ", pchk)
	b.WriteString(helpStyle.Render("(colour-code the P column: P0 loud, P3–P4 dim)"))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("esc / o / q to close (saves to ~/.config/wyk/ui.json)"))
	return b.String()
}

// viewHelp renders the keybinding overlay, grouped so the writes and
// navigation sections don't blur into each other. Source of truth is
// the keymap itself — no copy/paste of help strings.
func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("Keys"))
	b.WriteString("\n")

	// Source the grouping from DocsKeymap so this overlay and the
	// `wyk help --markdown` reference can never silently drift.
	// Any keymap addition lands in both by adding a single entry
	// in keymap.go.
	for _, g := range DocsKeymap() {
		b.WriteString("\n")
		b.WriteString(detailLabelStyle.Render(g.Title))
		b.WriteString("\n")
		for _, kb := range g.Bindings {
			h := kb.Help()
			fmt.Fprintf(&b, "  %-6s  %s\n", h.Key, h.Desc)
		}
	}
	b.WriteString("\n")
	b.WriteString(detailLabelStyle.Render("Notes"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  IDs are shown in full (e.g. \"" + exampleFullID(m) + "ma5.2.1\"), the way\n"))
	b.WriteString(helpStyle.Render("  bd and your agents refer to them, so a quoted ID matches a row\n"))
	b.WriteString(helpStyle.Render("  on sight. Press ⏎ to expand a row; y yanks the ID.\n"))
	b.WriteString(helpStyle.Render("  Mouse: in the list, the wheel scrolls and a click selects a row.\n"))
	b.WriteString(helpStyle.Render("  Split layout (140×36+): the pane follows the cursor; ⏎ focuses it,\n"))
	b.WriteString(helpStyle.Render("  esc returns, p hides/shows it. The wheel scrolls the pane it's over.\n"))
	b.WriteString(helpStyle.Render("  The detail view releases the mouse automatically so click-drag\n"))
	b.WriteString(helpStyle.Render("  selects text; m toggles capture everywhere (shift/option-drag\n"))
	b.WriteString(helpStyle.Render("  also reaches native selection while captured).\n"))
	b.WriteString(helpStyle.Render("  Yank (y) uses OSC 52 so the copy reaches your local clipboard\n"))
	b.WriteString(helpStyle.Render("  even over SSH; in tmux, enable `set -g allow-passthrough on`.\n"))
	b.WriteString("\n")

	// Status column legend — each row renders the value with its
	// actual style (so the user sees the color/strike treatment
	// used in the table) followed by a plain-text gloss. The
	// `wip` row is intentional: bd's underlying status is
	// `in_progress`, but the Status column abbreviates it.
	b.WriteString(detailLabelStyle.Render("Status column"))
	b.WriteString("\n")
	legend := []struct {
		display string
		raw     string // input to statusStyleFor (uses the real bd value)
		gloss   string
	}{
		{"open", "open", "available for work"},
		{"wip", "in_progress", "in progress (abbreviated in the table)"},
		{"hooked", "hooked", "attached to an agent's hook — another agent is on it"},
		{"blocked", "blocked", "has an open dependency or is human-blocked"},
		{"deferred", "deferred", "hidden from `bd ready` until a date (set via `d`)"},
		{"pinned", "pinned", "persistent; stays open indefinitely"},
		{"closed", "closed", "done; strikethrough"},
	}
	for _, e := range legend {
		styled := statusStyleFor(e.raw).Render(fmt.Sprintf("%-9s", e.display))
		fmt.Fprintf(&b, "  %s  %s\n", styled, helpStyle.Render(e.gloss))
	}
	b.WriteString("\n")

	// Owner column legend — the four "whose move is it" badges, rendered
	// with their real table styles. This is the product's central
	// concept and was previously only explained in the README, leaving a
	// HUMAN-BLOCK / AGENT-HANDOFF badge undecodable in-app
	// (would-you-kindly-5hy8).
	b.WriteString(detailLabelStyle.Render("Owner column"))
	b.WriteString("\n")
	ownerLegend := []struct {
		styled string
		plain  string
		gloss  string
	}{
		{humanBadge.Render("HUMAN"), "HUMAN", "your move — a human must act (the `human` label)"},
		{agentBadge.Render("AGENT"), "AGENT", "the agent's move (the default — no human label)"},
		{humanBlockBadge.Render("HUMAN-BLOCK"), "HUMAN-BLOCK", "agent task blocked by a human-flagged dependency"},
		{agentHandoffBadge.Render("AGENT-HANDOFF"), "AGENT-HANDOFF", "another agent owns it; a human coordinates — don't touch"},
	}
	for _, e := range ownerLegend {
		pad := strings.Repeat(" ", max(0, len("AGENT-HANDOFF")-len(e.plain)))
		fmt.Fprintf(&b, "  %s%s  %s\n", e.styled, pad, helpStyle.Render(e.gloss))
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("esc / ? / q to close"))
	return b.String()
}

// exampleFullID returns a workspace-prefix string suitable for the
// help text — uses the model's commonPrefix (single-repo) or the
// first multi-repo row's Repo prefix if available. Falls back to a
// generic placeholder if nothing's loaded yet.
func exampleFullID(m Model) string {
	if m.commonPrefix != "" {
		return m.commonPrefix
	}
	for _, i := range m.all {
		if i.Repo != "" {
			return i.Repo + "-"
		}
	}
	return "<workspace>-"
}

func (m Model) viewList() string {
	var b strings.Builder

	// Size each column to its content for this paint, then recompute
	// which columns must auto-hide to fit the width. Both are seeded on
	// the local m so renderHeader/renderRow/titleBudget/computeAutoHidden
	// (all below) read them for this paint only.
	m.cw = m.computeColWidths(m.visible)
	m.autoHidden = m.computeAutoHidden()

	b.WriteString(m.listTopChrome())
	b.WriteString(m.listBody())
	b.WriteString(m.listBottomChrome())
	return b.String()
}

// listTopChrome is everything above the table: title, setup hint, the
// filter-chip strip, and the blank separator. Shared by the stacked
// and split layouts so rowsStartY's click math holds for both.
func (m Model) listTopChrome() string {
	var b strings.Builder
	b.WriteString(renderListTitle())
	b.WriteString("\n")
	if m.setupHint != "" {
		b.WriteString(setupHintStyle.Render(m.setupHint))
		b.WriteString("\n")
	}
	// Filter chip strip — preset always shown; priority chip only
	// when the user has set a cap. Renders blank for the default
	// state (preset=all, no priority) so a fresh view stays
	// chrome-free.
	if chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); chips != "" {
		b.WriteString(chips)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// listBody is the stacked layout's table region (or its loading /
// error / empty stand-in). Rows end in a newline; the stand-ins don't
// — the bottom chrome's leading newline supplies the separator.
func (m Model) listBody() string {
	var b strings.Builder
	// Render the table whenever we have data. Transient states
	// (a flaky fetch error, an in-flight refresh) become banners
	// at the bottom instead of taking over the whole view — the
	// user always sees the most recent rows. Only the very first
	// paint, before any data has arrived, shows the full-screen
	// "loading…" / error stand-in.
	switch {
	case len(m.all) > 0:
		b.WriteString(m.renderHeader())
		b.WriteByte('\n')
		if len(m.visible) == 0 {
			// Preset-aware empty copy. The default "no rows for
			// this filter" line is honest but uninspiring; the
			// human preset specifically gets a celebratory line
			// since "nothing for me to do" is a great state.
			b.WriteString(emptyStyle.Render(emptyMatchCopy(m.preset, m.query)))
		} else {
			// Sticky-header viewport: pick a window around the
			// cursor instead of dumping every row and letting the
			// terminal scroll the header off the top. bodyHeight
			// for rendering uses the same computation as
			// ensureCursorVisible — when they agree, the cursor
			// can never be outside the rendered window.
			h := m.bodyHeight()
			start := m.scroll
			end := start + h
			if end > len(m.visible) {
				end = len(m.visible)
			}
			if start > end {
				start = end
			}
			for i := start; i < end; i++ {
				b.WriteString(m.renderRow(m.visible[i], i == m.cursor))
				b.WriteByte('\n')
			}
			// "+N more above/below" hints when the window doesn't
			// show everything. Subtle, single line each; only the
			// non-zero side renders so a fully-visible list stays
			// chrome-free.
			if start > 0 {
				b.WriteString(emptyStyle.Render(fmt.Sprintf("  ↑ %d more above", start)))
				b.WriteByte('\n')
			}
			if end < len(m.visible) {
				b.WriteString(emptyStyle.Render(fmt.Sprintf("  ↓ %d more below", len(m.visible)-end)))
				b.WriteByte('\n')
			}
		}
	case m.lastErr != nil:
		b.WriteString(errorStyle.Render(friendlyError(m.lastErr)))
		b.WriteString("\n\n")
		b.WriteString(emptyStyle.Render("press r to retry, q to quit"))
	case m.loading:
		b.WriteString(m.spinner.View())
		b.WriteString(emptyStyle.Render(" loading…"))
	case m.preset != filter.PresetAll:
		// Non-default preset with zero rows AND zero matches —
		// celebrate / explain per preset rather than rendering
		// the first-run copy that assumes bd is fresh.
		b.WriteString(emptyStyle.Render(emptyMatchCopy(m.preset, m.query)))
	default:
		b.WriteString(emptyStyle.Render(firstRunEmptyCopy()))
	}
	return b.String()
}

// listBottomChrome is everything below the table: the modal prompts,
// the transient banners, and the status bar with its key grid. Shared
// by the stacked and split layouts; chromeExtra budgets for it.
func (m Model) listBottomChrome() string {
	var b strings.Builder
	// modal prompts live just above the status bar
	switch {
	case m.usesTextInput():
		b.WriteString("\n")
		b.WriteString(m.input.View())
	case m.mode == modeNote:
		b.WriteString("\n")
		b.WriteString(m.noteArea.View())
	case m.mode == modeConfirmClose:
		// Render the captured ID, not the cursor's current target —
		// a refetch may have shifted things since the prompt opened.
		// Bulk path: pendingTarget.ID is "" and the prompt counts
		// the marked rows; single path: prompt shows the ID.
		b.WriteString("\n")
		if n := len(m.marked); n > 0 {
			b.WriteString(confirmStyle.Render(
				fmt.Sprintf("close %d marked rows? [y/N]", n)))
		} else if m.pendingTarget.ID != "" {
			b.WriteString(confirmStyle.Render(
				fmt.Sprintf("close %s? [y/N]", m.pendingTarget.ID)))
		}
	}

	// transient-fetch-error banner: when we have stale data on
	// screen but the most recent refresh errored, surface it as a
	// one-line banner instead of replacing the table. Without
	// this, a flaky bd query during an auto-refresh tick would
	// wipe the visible rows until the next tick recovered — the
	// "screen blanks on refresh" symptom.
	//
	// Terminal errors (bd missing, no workspace) also suspend the
	// auto-refresh tick, so the user needs an explicit cue to
	// press r and re-arm — append the retry hint in that case so
	// the recovery path stays discoverable. Transient errors
	// don't need it: the next 10s tick will retry on its own.
	if m.lastErr != nil && len(m.all) > 0 {
		msg := "refresh failed: " + friendlyError(m.lastErr)
		if isTerminalErr(m.lastErr) {
			msg += " — press r to retry"
		}
		b.WriteString("\n")
		b.WriteString(fetchErrorStyle.Render(msg))
	}

	// fetch-error banner: per-sub Fetch failures from a multi-repo
	// source. Surfaces above the transient status banner so it isn't
	// overwritten by write feedback. Re-rendered every paint from
	// m.fetchErrors so it tracks the latest fetch. Bounded by
	// m.width so several repos with long names can't wrap.
	if len(m.fetchErrors) > 0 {
		b.WriteString("\n")
		b.WriteString(fetchErrorStyle.Render(renderFetchErrorBanner(m.fetchErrors, m.width)))
	}

	// status banner (transient write feedback) above the status bar
	if m.status != "" {
		b.WriteString("\n")
		b.WriteString(statusBannerStyle.Render(m.status))
	}

	// update nudge: read from the updater cache at startup, shown
	// just above the status bar so the upgrade path is in view
	// without competing with the more dynamic status/fetch-error
	// lines above. Same amber-italic styling as setupHint — these
	// are both "thing you should do" prompts.
	if m.updateNudge != "" {
		b.WriteString("\n")
		b.WriteString(setupHintStyle.Render(m.updateNudge))
	}

	b.WriteString("\n")
	b.WriteString(m.statusBar())
	return b.String()
}

// Column widths for the list view. Kept as constants so the header
// row and the data rows stay aligned without duplicating numbers.
// The Repo and Branch columns are only rendered in multi-repo mode
// (when at least one fetched Issue carries a populated Repo field).
//
// colID is only the FLOOR for the ID column now — displayID renders
// the full `<workspace>-<suffix>` ID (agents quote IDs in full, so a
// trimmed suffix can't be matched against what they say), and
// computeColWidths sizes the column to the widest ID in view, capped
// by maxIDWidth. The const survives as the narrow-terminal floor.
const (
	colResp    = 15 // responsibility column: " AGENT ", " HUMAN ", " HUMAN-BLOCK ", " AGENT-HANDOFF ", or blank. 15 = " AGENT-HANDOFF " visual width (Padding(0,1) + 13-char content), the widest variant. Shorter badges get trailing whitespace. Placed second-from-left to put the most important "whose move is it" signal where the eye lands first.
	colRepo    = 18
	colBranch  = 10
	colID      = 12
	idHardMax  = 40 // absolute ceiling for the content-sized ID column: fits the longest real workspace-prefixed ID seen in practice ("louisville-open-data-expenditure-bot-4jm") without letting a pathological one run away with the row. See maxIDWidth.
	colType    = 4
	colStatus  = 8
	colPrio    = 9 // wide enough for the "Priority" header plus its sort-arrow ("Priority↑" = 9), like colUpdated holds "Updated↓". Values ("P0".."P4") are 2 chars and left-align in the slack.
	colUpdated = 8 // 8 chars so the "Updated↓" sort-arrow decoration fits without overflowing into the Title column. relTime values ("4h ago", "2 weeks", etc.) are all ≤ 7 chars so the extra slack is harmless when no sort is active.
	colSession = 8 // first 8 chars of the Claude session UUID that filed the issue (via `wyk create`); enough to recognise/disambiguate sessions at a glance. Header "Session" is 7.
)

// reviewMarkGlyph is the per-row provenance marker prefixed (with a trailing
// space) to review-sourced (label=roborev) titles. Its display width is
// MEASURED via dispWidth at the call site, not assumed — ◆ is East-Asian
// *ambiguous*, so it isn't reliably one cell. See renderRow.
const reviewMarkGlyph = "◆"

// colWidths holds the per-paint display width of each fixed column,
// sized to the wider of the header and the widest value in the current
// list (clamped to sane bounds) so columns scale to content instead of
// using one fixed pad. Title is the flex column and isn't tracked here.
type colWidths struct {
	owner, repo, branch, id, typ, status, prio, updated, session int
}

// computeColWidths sizes each fixed column to max(header, widest value)
// over rows, clamped to [headerWidth, max]. Sortable columns reserve a
// column for the sort arrow so the width doesn't jump when the sort is
// toggled. With no rows every column falls back to its header width.
// The per-column maxima reuse the col* consts as the upper bound, so a
// pathological value truncates rather than blowing out the row.
//
// rows is the whole filtered list (m.visible), not just the on-screen
// viewport: sizing over the full list keeps column widths stable while
// you scroll instead of jittering as a wider value enters/leaves view.
// The cost is one O(len(rows)) pass of cheap rune-width measurements per
// paint, negligible for realistic backlogs.
func (m Model) computeColWidths(rows []beads.Issue) colWidths {
	const (
		hOwner, hRepo, hBranch, hID = 5, 4, 6, 2 // "Owner" "Repo" "Branch" "ID"
		hType, hStatus, hSess       = 4, 6, 7    // "Type" "Status" "Session"
	)
	w := colWidths{
		owner:   hOwner,
		repo:    hRepo + 1, // sortable → reserve the arrow cell
		branch:  hBranch,
		id:      hID + 1, // sortable
		typ:     hType,
		status:  hStatus,
		prio:    colPrio,    // header-dominated; const already fits "Priority↑"
		updated: colUpdated, // header-dominated; const already fits "Updated↓"
		session: hSess,
	}
	for _, i := range rows {
		w.owner = max(w.owner, lipgloss.Width(responsibilityBadgeFor(i)))
		w.repo = max(w.repo, lipgloss.Width(i.Repo))
		w.branch = max(w.branch, lipgloss.Width(i.Branch))
		w.id = max(w.id, lipgloss.Width(m.displayID(i)))
		w.typ = max(w.typ, lipgloss.Width(abbrevType(i.IssueType)))
		w.status = max(w.status, lipgloss.Width(m.statusCell(i)))
		w.session = max(w.session, lipgloss.Width(sessionShort(i)))
		// prio ("P0".."P4") and updated (relTime) values are always
		// narrower than their headers, so they stay header-driven.
	}
	w.owner = min(w.owner, colResp)
	w.repo = min(w.repo, colRepo)
	w.branch = min(w.branch, colBranch+6) // a little more room than the old fixed 10 for "docs/…" branches
	w.id = min(w.id, m.maxIDWidth())
	w.typ = min(w.typ, colType+2)
	w.status = min(w.status, colStatus+1)
	w.session = min(w.session, colSession)
	return w
}

// maxIDWidth is the ceiling computeColWidths clamps the ID column to.
// Full bd IDs are `<workspace>-<suffix>` and workspaces get long names
// (`louisville-open-data-expenditure-bot-4jm` is 40 cells), so a flat
// constant either truncates real IDs or starves the flex Title column
// on a narrow terminal. Instead the ceiling scales with the terminal:
// at most a third of the width, never below colID+4 (the old fixed
// cap, so a narrow terminal is no worse off than before) and never
// above idHardMax. The column still sizes to CONTENT first — this only
// bounds how much a pathological ID may take (would-you-kindly-rvv9).
func (m Model) maxIDWidth() int {
	if m.width <= 0 {
		return idHardMax // pre-first-WindowSizeMsg: don't truncate blind
	}
	return min(idHardMax, max(colID+4, m.width/3))
}

// isMultiRepo reports whether the current list has any issue with
// a populated Repo field. The Repo/Branch columns are gated on this
// — they render whenever the source decorates issues. In practice
// every BDSource path now sets a Name (which Fetch uses to populate
// Repo), so this is effectively always true and the columns are
// always on. The gate stays as a safety net: a Source that
// intentionally returns undecorated issues (a stub in tests, or a
// future read-only adapter) still gets the compact layout.
func (m Model) isMultiRepo() bool {
	for _, i := range m.all {
		if i.Repo != "" {
			return true
		}
	}
	return false
}

// displayID returns the ID exactly as bd — and every agent — refers
// to it: in full, prefix and all.
//
// This column used to strip the repeated workspace prefix
// (`<issue.Repo>-`, or the longest common prefix of m.all) to save
// width. That optimized the wrong thing: the ID column's whole job is
// letting you match a row against an ID someone quoted at you, and an
// agent says "would-you-kindly-l51f", never "l51f". A trimmed suffix
// — or worse, an ellipsized `workspace-cust…` — forces the user to
// expand rows one by one to find the issue the agent meant
// (would-you-kindly-rvv9). Width is the cheaper thing to spend:
// computeColWidths sizes this column to the widest ID actually in
// view, so nothing is truncated until the terminal is genuinely too
// narrow.
//
// Kept as a method (rather than inlining i.ID at the call sites) so
// the column keeps its single formatting seam.
func (m Model) displayID(i beads.Issue) string {
	return i.ID
}

// sessionLabelPrefix is the bd label namespace recording which Claude
// session filed an issue (`session:<id>`), stamped by `wyk create`. The
// CLI side (cmd/wyk.sessionLabelPrefix) writes the same prefix — keep
// the two in sync.
const sessionLabelPrefix = "session:"

// sessionShort returns the first colSession runes of the session ID
// recorded on the issue's `session:` label, for the Session column. An
// issue filed without `wyk create` (no label) renders blank.
func sessionShort(i beads.Issue) string {
	for _, l := range i.Labels {
		if v, ok := strings.CutPrefix(l, sessionLabelPrefix); ok {
			v = strings.TrimSpace(v)
			// Plain leading-rune slice (no ellipsis): a session prefix is
			// an opaque hash, so "abcdef01" reads better than "abcde01…".
			r := []rune(v)
			if len(r) > colSession {
				return string(r[:colSession])
			}
			return v
		}
	}
	return ""
}

// commonIDPrefix returns the longest common prefix of every issue's
// ID that ends in `-` so the trimmed suffix is still readable.
// Returns "" if there's no consistent prefix (or fewer than 2 rows).
func commonIDPrefix(issues []beads.Issue) string {
	if len(issues) < 2 {
		return ""
	}
	pref := issues[0].ID
	for _, i := range issues[1:] {
		pref = lcp(pref, i.ID)
		if pref == "" {
			return ""
		}
	}
	if idx := strings.LastIndex(pref, "-"); idx >= 0 {
		return pref[:idx+1]
	}
	return ""
}

// lcp is the longest-common-prefix of two strings.
func lcp(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// renderHeader prints the column-titles row above the issue list,
// followed by a thin divider so the header doesn't visually merge
// with the first data row. The leading two spaces line up with the
// cursor column on data rows so the title and ID columns share a
// left edge. Repo and Branch only appear when the current list
// spans multiple workspaces.
func (m Model) renderHeader() string {
	const cursor = "  "
	var b strings.Builder
	b.WriteString(cursor)
	if m.colVisible(colIDOwner) {
		fmt.Fprintf(&b, "%-*s  ", m.cw.owner, "Owner")
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDRepo) {
			fmt.Fprintf(&b, "%-*s  ", m.cw.repo, sortDecorate("Repo", m.sortBy == sortRepo, "↑", m.sortDesc))
		}
		if m.colVisible(colIDBranch) {
			fmt.Fprintf(&b, "%-*s  ", m.cw.branch, "Branch")
		}
	}
	fmt.Fprintf(&b, "%-*s  ", m.cw.id, sortDecorate("ID", m.sortBy == sortID, "↑", m.sortDesc))
	if m.colVisible(colIDType) {
		fmt.Fprintf(&b, "%-*s  ", m.cw.typ, "Type")
	}
	if m.colVisible(colIDStatus) {
		fmt.Fprintf(&b, "%-*s  ", m.cw.status, "Status")
	}
	fmt.Fprintf(&b, "%-*s  ", m.cw.prio, sortDecorate("Priority", m.sortBy == sortPriority, "↑", m.sortDesc))
	if m.colVisible(colIDUpdated) {
		fmt.Fprintf(&b, "%-*s  ", m.cw.updated, sortDecorate("Updated", m.sortBy == sortUpdated, "↓", m.sortDesc))
	}
	if m.colVisible(colIDSession) {
		fmt.Fprintf(&b, "%-*s  ", m.cw.session, "Session")
	}
	b.WriteString("Title")
	return tableHeaderStyle.Render(b.String())
}

// sortDecorate appends an arrow to a column header when that
// column is the active sort axis. natural is the arrow for the
// axis's natural direction (priority asc → ↑, updated desc → ↓);
// reverse flips it. Lets renderHeader stay a flat fmt.Fprintf
// rather than carrying a separate "active? reversed?" branch per
// column.
func sortDecorate(label string, active bool, natural string, reverse bool) string {
	if !active {
		return label
	}
	if reverse {
		return label + flipArrow(natural)
	}
	return label + natural
}

func flipArrow(a string) string {
	switch a {
	case "↑":
		return "↓"
	case "↓":
		return "↑"
	}
	return a
}

func (m Model) renderRow(i beads.Issue, selected bool) string {
	cursor := "  "
	if selected {
		cursor = cursorStyle.Render("▶ ")
	}
	// Marked rows get a ✓ in the cursor cell (replacing the
	// leading space) so the multi-select is visible at a glance.
	// Selected-and-marked shows ▶ (cursor wins; the user knows
	// they're on a marked row from the context).
	if !selected && m.marked[issueKey(i)] {
		cursor = cursorStyle.Render("✓ ")
	}
	// For closed rows, swap the "default" cell styles (id, type,
	// updated — all currently lipgloss.NewStyle() no-ops) for the
	// muted closedRowStyle, and pre-dim the bare column separator
	// + priority text. An earlier version of this code wrapped the
	// whole pre-built b.String() in a single foreground envelope,
	// but lipgloss/termenv emit a `\x1b[0m` reset at the end of
	// every inner Render, which cleared the envelope's color
	// before reaching most of the unstyled output — only the
	// leading whitespace inherited the dim. Painting per-cell
	// here is the design that actually works.
	idC, typeC, updatedC := idStyle, typeStyle, updatedStyle
	sep := "  "
	dim := func(s string) string { return s }
	if i.Status == "closed" {
		idC = closedRowStyle
		typeC = closedRowStyle
		updatedC = closedRowStyle
		sep = closedRowStyle.Render("  ")
		dim = func(s string) string { return closedRowStyle.Render(s) }
	}
	var b strings.Builder
	b.WriteString(cursor)
	if m.colVisible(colIDOwner) {
		b.WriteString(paddedResponsibilityBadge(i, m.cw.owner))
		b.WriteString(sep)
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDRepo) {
			b.WriteString(renderMatchCell(sanitizeInline(i.Repo), m.cw.repo, m.query, typeC))
			b.WriteString(sep)
		}
		if m.colVisible(colIDBranch) {
			// A git branch name can carry control bytes; sanitize like the
			// other single-line cells (roborev #1848).
			b.WriteString(renderMatchCell(sanitizeInline(i.Branch), m.cw.branch, m.query, typeC))
			b.WriteString(sep)
		}
	}
	b.WriteString(renderIDCell(m.displayID(i), m.cw.id, m.query, idC))
	b.WriteString(sep)
	if m.colVisible(colIDType) {
		b.WriteString(typeC.Render(fmt.Sprintf("%-*s", m.cw.typ, abbrevType(i.IssueType))))
		b.WriteString(sep)
	}
	if m.colVisible(colIDStatus) {
		b.WriteString(m.statusCellStyle(i).Render(fmt.Sprintf("%-*s", m.cw.status, m.statusCell(i))))
		b.WriteString(sep)
	}
	// Pad the priority value to the column width so it aligns under the
	// "Priority" header (which is wider than the bare "Pn" value).
	prio := fmt.Sprintf("%-*s", m.cw.prio, fmt.Sprintf("P%d", i.Priority))
	switch {
	case i.Status == "closed":
		b.WriteString(dim(prio)) // closed-row dim wins over priority emphasis
	case m.priorityEmphasis:
		b.WriteString(priorityStyleFor(i.Priority).Render(prio))
	default:
		b.WriteString(prio)
	}
	b.WriteString(sep)
	if m.colVisible(colIDUpdated) {
		b.WriteString(updatedC.Render(fmt.Sprintf("%-*s", m.cw.updated, relTime(i.UpdatedAt))))
		b.WriteString(sep)
	}
	if m.colVisible(colIDSession) {
		b.WriteString(typeC.Render(fmt.Sprintf("%-*s", m.cw.session, sessionShort(i))))
		b.WriteString(sep)
	}
	// Truncate the title to whatever space remains after every
	// preceding column. Without this, long titles wrap or overflow
	// the right edge — most existing rows in real use spill past
	// the terminal. Detail view (enter) still shows the full text.
	// Sanitize before trunc/highlight so width is computed on the clean
	// text and no terminal escape from a hostile title reaches the screen
	// (would-you-kindly-waub).
	title := sanitizeInline(i.Title)
	origLen := utf8.RuneCountInString(title)
	// Review-sourced rows (label=roborev) get a leading ◆ glyph so they're
	// distinguishable per-row in any preset — a provenance marker kept out of
	// the owner column's ownership axis. The prefix charges the title budget
	// its MEASURED width (dispWidth, not a hardcoded count): ◆ is East-Asian
	// *ambiguous*, so it's 2 cells under ambWide and "◆ " is 3 — reserving a
	// flat 2 would overrun the right edge by a cell on ambiguous-wide
	// terminals. Added only when the budget can spare it.
	reviewPrefix := ""
	prefixWidth := 0
	if i.HasLabel(filter.ReviewLabel) {
		if w := dispWidth(reviewMarkGlyph + " "); m.titleBudget() > w {
			reviewPrefix = reviewMarkStyle.Render(reviewMarkGlyph) + " "
			prefixWidth = w
		}
	}
	if avail := m.titleBudget() - prefixWidth; avail > 0 {
		title = trunc(title, avail)
	}
	b.WriteString(reviewPrefix)
	// Apply fuzzy-match highlighting after truncation. When trunc
	// inserts an ellipsis (`runes[:n-1] + "…"`, taken when n >= 2
	// AND the string was longer than n), drop any match at or
	// after the ellipsis position so we don't style the `…` glyph
	// itself. trunc's n==1 branch returns a bare first rune with
	// NO ellipsis, so we keep the visibleLen >= 2 guard to avoid
	// suppressing a legitimate index-0 highlight in that case.
	// Matches past the truncated tail are silently dropped (no
	// off-screen ANSI).
	if idxs := m.titleMatches[issueKey(i)]; len(idxs) > 0 {
		if visibleLen := utf8.RuneCountInString(title); visibleLen < origLen && visibleLen >= 2 {
			ceiling := visibleLen - 1
			filtered := make([]int, 0, len(idxs))
			for _, ix := range idxs {
				if ix < ceiling {
					filtered = append(filtered, ix)
				}
			}
			idxs = filtered
		}
		// For closed rows, pass closedRowStyle as the "rest" style
		// so every non-highlighted run in the title carries the
		// dim. Without this, the first fuzzy highlight's trailing
		// \x1b[0m would clear an outer wrap and leave the title
		// tail at terminal default. Open rows pass nil so no
		// extra Render allocations happen on the common path.
		var rest *lipgloss.Style
		if i.Status == "closed" {
			r := closedRowStyle
			rest = &r
		}
		title = highlightRunesWithRest(title, idxs, fuzzyMatchStyle, rest)
		b.WriteString(title)
	} else {
		// No fuzzy matches: dim the whole title for closed rows
		// (no-op for open).
		b.WriteString(dim(title))
	}
	return b.String()
}

// substringRuneIdxs returns the rune indices of the first
// case-insensitive occurrence of query in s, or nil if absent. Used to
// highlight the matched run in the substring-filtered columns (repo,
// branch, ID) the same way titleMatches highlights the fuzzy title.
//
// It compares rune-by-rune with unicode.ToLower rather than lowercasing
// the whole string and mapping byte offsets back: strings.ToLower can
// change a string's rune count (e.g. İ → i + combining dot), which would
// shift the highlight onto the wrong runes of the original-case value.
func substringRuneIdxs(s, query string) []int {
	if s == "" || query == "" {
		return nil
	}
	sr := []rune(s)
	qr := []rune(query)
	for k := range qr {
		qr[k] = unicode.ToLower(qr[k])
	}
	for start := 0; start+len(qr) <= len(sr); start++ {
		matched := true
		for k := range qr {
			if unicode.ToLower(sr[start+k]) != qr[k] {
				matched = false
				break
			}
		}
		if matched {
			idxs := make([]int, len(qr))
			for k := range idxs {
				idxs[k] = start + k
			}
			return idxs
		}
	}
	return nil
}

// renderMatchCell renders a fixed-width column cell: value truncated to
// width, padded out to it, with a case-insensitive substring match of
// query highlighted in fuzzyMatchStyle and everything else (including the
// trailing pad) in base. Mirrors the title's fuzzy highlight for the
// substring-filtered columns. Empty/absent query → plain base cell.
func renderMatchCell(value string, width int, query string, base lipgloss.Style) string {
	return padAndHighlight(trunc(value, width), width, query, base)
}

// renderIDCell is renderMatchCell for the ID column, which needs
// middle-eliding (truncID) rather than trunc's right-eliding so a
// too-narrow column can't render every row in a workspace identically.
func renderIDCell(value string, width int, query string, base lipgloss.Style) string {
	return padAndHighlight(truncID(value, width), width, query, base)
}

// padAndHighlight right-pads an already-fitted cell to width and
// applies the fuzzy-match highlighting. Shared by the cell renderers so
// they differ only in HOW they truncate.
func padAndHighlight(val string, width int, query string, base lipgloss.Style) string {
	if pad := width - lipgloss.Width(val); pad > 0 {
		val += strings.Repeat(" ", pad)
	}
	rest := base
	return highlightRunesWithRest(val, substringRuneIdxs(val, query), fuzzyMatchStyle, &rest)
}

// highlightRunesWithRest returns s with the runes at the given rune
// indices wrapped in style.Render. Indices past the end of s (e.g.
// matches in a truncated title) are silently dropped. It also takes an
// optional "rest" style applied to runs of non-highlighted runes. Used by
// renderRow for closed rows with active fuzzy matches: every
// embedded fuzzy SGR emits a \x1b[0m reset on its trailing edge,
// which would clear an outer dim envelope and leave the title
// segments AFTER the first highlight at terminal default. Re-
// applying the rest style per non-highlighted run keeps the dim
// across the whole title. Nil restStyle skips the wrap (open
// rows, where the no-op style would still trigger a Render
// allocation per run).
func highlightRunesWithRest(s string, idxs []int, style lipgloss.Style, restStyle *lipgloss.Style) string {
	if len(idxs) == 0 {
		if restStyle != nil {
			return restStyle.Render(s)
		}
		return s
	}
	set := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		set[i] = true
	}
	var b strings.Builder
	pos := 0
	// Buffer consecutive non-highlighted runes so a single
	// restStyle.Render wraps each run, not each rune.
	var plain strings.Builder
	flushPlain := func() {
		if plain.Len() == 0 {
			return
		}
		if restStyle != nil {
			b.WriteString(restStyle.Render(plain.String()))
		} else {
			b.WriteString(plain.String())
		}
		plain.Reset()
	}
	for _, r := range s {
		if set[pos] {
			flushPlain()
			b.WriteString(style.Render(string(r)))
		} else {
			plain.WriteRune(r)
		}
		pos++
	}
	flushPlain()
	return b.String()
}

// titleBudget returns how many runes are available for the title
// column given the current terminal width and the fixed widths of
// every preceding column. Returns 0 when m.width is unknown (before
// the first WindowSizeMsg) so we just print the full title — the
// next paint will redraw with the right budget. A 20-rune floor
// keeps the column from collapsing to nothing on absurdly narrow
// panes; the user can widen and re-render.
func (m Model) titleBudget() int {
	if m.width <= 0 {
		return 0
	}
	// Each "  " separator is 2 spaces; we count one after every
	// non-final column to mirror what renderRow prints. Hidden
	// columns contribute 0 — the saved width flows into the title
	// cell, which is exactly what users hide columns for.
	const sep = 2
	used := 2 // cursor (▶ or 2 spaces is 2 visual cols either way)
	if m.colVisible(colIDOwner) {
		used += m.cw.owner + sep
	}
	if m.isMultiRepo() {
		if m.colVisible(colIDRepo) {
			used += m.cw.repo + sep
		}
		if m.colVisible(colIDBranch) {
			used += m.cw.branch + sep
		}
	}
	used += m.cw.id + sep
	if m.colVisible(colIDType) {
		used += m.cw.typ + sep
	}
	if m.colVisible(colIDStatus) {
		used += m.cw.status + sep
	}
	used += m.cw.prio + sep
	if m.colVisible(colIDUpdated) {
		used += m.cw.updated + sep
	}
	if m.colVisible(colIDSession) {
		used += m.cw.session + sep
	}
	avail := m.width - used
	if avail < 20 {
		avail = 20 // floor so we don't render an empty title cell
	}
	// Title is a true flex column: it consumes all remaining width so a
	// wide terminal shows as much of the title as fits instead of
	// leaving dead space to the right (user request — fill the gap). An
	// earlier build capped this at 50 cols to stop titles sprawling
	// across the row, but the empty-margin cost outweighed the sprawl
	// concern; the detail view (enter) still shows the untruncated text.
	return avail
}

// paddedResponsibilityBadge renders the per-row responsibility cell
// so the column stays aligned regardless of badge presence/variant.
// Rows with no responsibility signal (no human label and no
// src:agent label) emit colResp spaces; flagged rows emit the
// styled badge padded out to the same visual width with trailing
// blanks. We can't just %-*s the badge string because lipgloss
// escape codes would be counted as visual width by fmt.
func paddedResponsibilityBadge(i beads.Issue, width int) string {
	badge := responsibilityBadgeFor(i)
	if badge == "" {
		return strings.Repeat(" ", width)
	}
	pad := width - lipgloss.Width(badge)
	if pad > 0 {
		badge += strings.Repeat(" ", pad)
	}
	return badge
}

// responsibilityBadgeFor returns the badge for the "Owner" column,
// telling the reader whose move it is. The badge is NEVER blank:
//   - has `human` label → plain "HUMAN" (the human-needs-to-act
//     signal trumps everything else; src distinction is dropped —
//     a glance at the column should give a yes/no answer, not a
//     three-way categorisation that buries the lede)
//   - has `agent-handoff` label → "AGENT-HANDOFF" — another agent owns
//     this; THIS agent must not interfere, a human orchestrates the
//     coordination. Ranks above HUMAN-BLOCK/AGENT because it's an
//     explicit, deliberate flag, not a computed state.
//   - blocked by a human-flagged dep → "HUMAN-BLOCK"
//   - anything else → AGENT. A null owner (no `src:`/`human` label at
//     all) DEFAULTS to AGENT — a task with no explicit owner is treated
//     as agent-owned rather than rendering an empty column.
func responsibilityBadgeFor(i beads.Issue) string {
	if i.IsHuman() {
		return humanBadge.Render("HUMAN")
	}
	// AGENT-HANDOFF is an explicit "leave this to another agent" flag, so it
	// outranks both the computed HUMAN-BLOCK and the plain AGENT default — a
	// human is expected to orchestrate the cross-agent coordination.
	if i.IsAgentHandoff() {
		return agentHandoffBadge.Render("AGENT-HANDOFF")
	}
	// Everything not flagged for a human is agent-owned — including an issue
	// with NO owner label at all. A null owner DEFAULTS to AGENT rather than
	// rendering a blank column, so the owner badge is never empty.
	// HUMAN-BLOCK takes precedence over plain AGENT so a row the agent cannot
	// unblock reads visually different from rows the inbox imperative says to
	// act on (set by markBlockedByHuman post-Fetch when a dep carries the
	// human label).
	if i.BlockedByHuman {
		return humanBlockBadge.Render("HUMAN-BLOCK")
	}
	return agentBadge.Render("AGENT")
}

// abbrevType returns a fixed-width type slug. Most bd types fit in
// 4 chars natively (task, bug, epic); the longer ones are truncated
// to the same width so column alignment holds.
func abbrevType(t string) string {
	if len(t) <= colType {
		return t
	}
	return t[:colType]
}

// abbrevStatus normalises bd's status names for the table column.
// "in_progress" gets the conventional "wip" because the full string
// would dominate the row width and 'wip' is unambiguous in context.
func abbrevStatus(s string) string {
	if s == "in_progress" {
		return "wip"
	}
	return s
}

// statusCell is the text the Status column shows for a row: its bd
// status, or "closing" while a close is in flight against it. Both
// computeColWidths and renderRow go through this one function — a
// second word in the column that the width pass didn't know about
// would push every cell to its right out of alignment.
func (m Model) statusCell(i beads.Issue) string {
	if m.isClosing(i) {
		return closingLabel
	}
	return abbrevStatus(i.Status)
}

// statusCellStyle pairs with statusCell. An in-flight close borrows
// the in-progress emphasis (amber): work is happening on this row
// right now, which is exactly what the user needs to see before they
// reach for the key again.
func (m Model) statusCellStyle(i beads.Issue) lipgloss.Style {
	if m.isClosing(i) {
		return statusInProgress
	}
	return statusStyleFor(i.Status)
}

// relTime renders a coarse "how long ago" stamp for the Updated
// column. Bins (now / <1h / <1d / <30d / older) keep the column
// narrow without losing the rough age signal a triage reader wants.
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

func (m Model) statusBar() string {
	left := fmt.Sprintf("[%s]  %d/%d", m.preset, len(m.visible), len(m.all))
	if stats := m.renderStatsLine(); stats != "" {
		left += "  " + stats
	}
	if m.query != "" {
		left += fmt.Sprintf("  filter:%q", m.query)
	}
	switch {
	case m.cacheStale && !m.cacheSavedAt.IsZero():
		// While the rows on screen come from the on-disk cache and
		// no successful live fetch has replaced them, surface that
		// they're stale. The cached branch takes precedence over
		// "synced" because lastSync is updated on every fetchedMsg
		// — including failures — and a failed first fetch after a
		// warm-start would otherwise label stale cached rows as
		// "synced", lying about freshness to the user.
		left += "  cached " + relTime(m.cacheSavedAt)
	case !m.lastSync.IsZero():
		left += "  synced " + m.lastSync.Format("15:04:05")
	}
	// In-flight refresh indicator. Subtle on purpose — the table
	// stays visible underneath; this just tells the user that
	// hitting r (or switching presets) actually did something
	// while bd's round-trip is in flight.
	if m.refreshing {
		left += "  ↻ refreshing"
	}
	// The "(read-only)" note rides on the info line (footerBindings drops
	// the write keys when there's no Mutator).
	if m.mutator() == nil {
		left += "  (read-only)"
	}
	// Status info on its own full-width bar, then the key bindings wrapped
	// into a column-aligned grid below it (roborev's footer style) so every
	// binding stays visible rather than running off the edge. The info line
	// keeps statusBarStyle's filled-bar look, padded out so the bar spans
	// the whole row instead of a ragged segment. We pad with dispWidth (and
	// let the bg cover the trailing spaces) rather than lipgloss's .Width():
	// .Width measures the · separator narrow and would overrun ambiguous-
	// wide terminals by a cell. The 2 accounts for statusBarStyle's
	// Padding(0,1). The grid rows are dim helpStyle below it, and the grid's
	// height feeds chromeExtra so the table shrinks to make room.
	if m.width > 0 {
		if fill := m.width - 2 - dispWidth(left); fill > 0 {
			left += strings.Repeat(" ", fill)
		}
	}
	out := statusBarStyle.Render(left)
	for _, gl := range m.helpGridLines(m.footerBindings(), m.width) {
		out += "\n" + gl
	}
	return out
}

// footerBindings is the short-help set the status-bar footer renders: the
// write-aware list, minus the write keys when there's no Mutator wired up.
// Shared by statusBar (render) and chromeExtra (height budget) so the two
// can't drift on the read-only swap rule.
func (m Model) footerBindings() []key.Binding {
	if m.splitView() && m.paneFocused() {
		return m.keys.paneHelp(m.mutator() != nil)
	}
	if m.mutator() == nil {
		return m.keys.shortHelpReadOnly()
	}
	return m.keys.ShortHelp()
}

// ambWide measures display width treating East-Asian *ambiguous*-width
// glyphs (·, ±, ▕, arrows, …) as 2 cells. Many terminals render those
// double-wide; lipgloss/the default runewidth count them as 1. We use
// this only for the status-bar budget, where under-measuring lets the
// footer overrun the pane on ambiguous-wide terminals.
var ambWide = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = true
	return c
}()

// dispWidth is ambWide.StringWidth for a plain (ANSI-free) string.
func dispWidth(s string) int { return ambWide.StringWidth(s) }

// helpGridLines lays the short-help bindings out as a column-aligned grid
// (roborev's footer style): the most columns whose row-major layout fits
// `avail` — fewest rows — with every column padded to its widest cell and
// joined by " ▕ " so the separators line up between rows. All bindings
// stay visible; the grid grows downward rather than truncating. Returns
// the styled lines (helpStyle), or nil when the width is unknown or there
// is nothing to show. Widths are measured with dispWidth so ambiguous-
// width glyphs (▕, ·, ±) don't push a row past the edge.
func (m Model) helpGridLines(bindings []key.Binding, avail int) []string {
	if avail < 1 {
		return nil
	}
	items := make([]string, 0, len(bindings))
	for _, bnd := range bindings {
		if !bnd.Enabled() {
			continue
		}
		h := bnd.Help()
		items = append(items, h.Key+" "+h.Desc)
	}
	if len(items) == 0 {
		return nil
	}
	sepW := dispWidth(" ▕ ")
	gridWidth := func(cols int) (int, []int) {
		w := make([]int, cols)
		for i, it := range items {
			if d := dispWidth(it); d > w[i%cols] {
				w[i%cols] = d
			}
		}
		total := sepW * (cols - 1)
		for _, cw := range w {
			total += cw
		}
		return total, w
	}
	cols, colW := 1, []int{0}
	for c := len(items); c >= 1; c-- {
		if total, w := gridWidth(c); total <= avail {
			cols, colW = c, w
			break
		}
	}
	var lines []string
	for r := 0; r*cols < len(items); r++ {
		cells := make([]string, 0, cols)
		for c := 0; c < cols && r*cols+c < len(items); c++ {
			it := items[r*cols+c]
			// Pad every cell EXCEPT the last one in the row to its column
			// width so the ▕ separators line up; the row's final cell has
			// no following separator, so padding it is just dead trailing
			// whitespace.
			last := c == cols-1 || r*cols+c == len(items)-1
			if !last {
				if pad := colW[c] - dispWidth(it); pad > 0 {
					it += strings.Repeat(" ", pad)
				}
			}
			cells = append(cells, it)
		}
		lines = append(lines, helpStyle.Render(strings.Join(cells, " ▕ ")))
	}
	return lines
}

// renderStatsLine builds the "· N human · M mine" suffix appended
// to the status bar's left side. Computed from m.all (no extra
// fetch) and intentionally excludes "ready" — bd ready has
// blocker-aware semantics that a label count can't approximate,
// and a wrong number in a stats line is worse than no number.
// Empty when there's nothing to display (no human, no me set).
//
// IMPORTANT: counts are scoped to the *current preset*'s fetch.
// m.all holds only the rows the active preset returned, so "N
// human" under PresetReady counts human-flagged ready issues
// (not workspace-wide). The `N/M` cell to the left of this
// suffix already advertises the preset name, so the scoping is
// implicit — but if a future preset is added where the count
// could mislead, surface the scoping explicitly here.
func (m Model) renderStatsLine() string {
	human := 0
	mine := 0
	review := 0
	for _, i := range m.all {
		for _, l := range i.Labels {
			if l == "human" {
				human++
				break
			}
		}
		if i.HasLabel(filter.ReviewLabel) {
			review++
		}
		// Assignee, not Owner: the mine preset queries assignee=,
		// so the count must tally the same field or the status-bar
		// number and the view it advertises disagree.
		if m.me != "" && i.Assignee == m.me {
			mine++
		}
	}
	var parts []string
	if human > 0 {
		parts = append(parts, fmt.Sprintf("%d human", human))
	}
	if review > 0 {
		// Surface roborev-filed review work at a glance, in any preset —
		// distinguishing review-sourced rows without overloading the
		// owner-column ownership axis (the dedicated `review` preset is
		// the filtered view).
		parts = append(parts, fmt.Sprintf("%d review", review))
	}
	if m.me != "" {
		// Show the mine slot even at 0 so the user knows it's
		// computed — silently dropping when zero would make a
		// user think their identity isn't wired up.
		parts = append(parts, fmt.Sprintf("%d mine", mine))
	}
	if len(parts) == 0 {
		return ""
	}
	return "· " + strings.Join(parts, " · ")
}

// renderFetchErrorBanner formats the per-sub Fetch failures into a
// single line. Names are joined with commas; a long list collapses
// to "N repos failed: a, b, c, +M more" so a registry full of
// failing repos doesn't blow out the line. The actionable hint
// ("press r to retry; wyk doctor for details") rides on every
// variant — the truncated case is exactly when retrying is most
// likely the right move. If width > 0 and the formatted message
// still exceeds it (e.g. several repos with long names), trunc
// caps it with an ellipsis so the banner can't wrap. width<=0
// disables the cap (used by tests).
func renderFetchErrorBanner(errs []FetchError, width int) string {
	const showFirst = 3
	const tail = " (press r to retry; wyk doctor for details)"
	n := len(errs)
	// Same-error coalesce: when every sub returned the same
	// underlying error string (typical case: every repo hit the
	// 10s timeout because the user's machine is under load), the
	// per-name list is noisy and the actionable signal is the
	// shared error. Surface it once. Errors with distinct text
	// fall through to the name-list path so the user still sees
	// which repos are affected.
	if n > 1 && allFetchErrsSame(errs) {
		s := fmt.Sprintf("%d repos all failed: %s%s", n, errs[0].Err.Error(), tail)
		if width > 0 && len(s) > width {
			s = trunc(s, width)
		}
		return s
	}
	names := make([]string, 0, n)
	for _, e := range errs {
		names = append(names, e.Repo)
	}
	var s string
	switch {
	case n == 1:
		s = "1 repo failed to load: " + names[0] + tail
	case n <= showFirst:
		s = fmt.Sprintf("%d repos failed to load: %s%s", n, strings.Join(names, ", "), tail)
	default:
		s = fmt.Sprintf("%d repos failed to load: %s, +%d more%s",
			n, strings.Join(names[:showFirst], ", "), n-showFirst, tail)
	}
	if width > 0 && len(s) > width {
		s = trunc(s, width)
	}
	return s
}

// allFetchErrsSame reports whether every FetchError in the slice
// has the same Err.Error() text. Used by the banner to collapse
// the "every repo hit the same timeout" case into a single
// actionable line. Callers MUST construct FetchErrors with a
// non-nil Err — the coalesce branch in renderFetchErrorBanner
// formats errs[0].Err.Error() without a nil guard, so a nil-Err
// entry sneaking in would panic. Every in-tree construction site
// (see source.go) populates Err; tests should do the same.
func allFetchErrsSame(errs []FetchError) bool {
	if len(errs) < 2 {
		return false
	}
	first := errs[0].Err.Error()
	for _, e := range errs[1:] {
		if e.Err.Error() != first {
			return false
		}
	}
	return true
}

// chromeMinOverhead is the number of non-row lines viewList always
// emits when the table is shown: title, blank, header, blank, status
// bar, plus a one-line breathing-room buffer so the bottom row never
// kisses the status bar. Banners (setupHint, fetch error, status,
// modal prompts) are NOT in this base because they're conditional;
// bodyHeight subtracts them via the m.chromeExtra() count below.
const chromeMinOverhead = 5

// chromeExtra counts the conditional chrome lines that compete with
// rows for vertical real estate. Each banner is one line; modal
// prompts vary. Kept close to viewList so the budget arithmetic
// matches what's actually rendered.
func (m Model) chromeExtra() int {
	n := 0
	// The status-bar key bindings wrap into a grid BELOW the info line
	// (the info line itself is counted in chromeMinOverhead). Each wrapped
	// row is an extra line competing with table rows. Use the same builder
	// and binding set statusBar renders so the budget can't drift.
	n += len(m.helpGridLines(m.footerBindings(), m.width))
	if m.setupHint != "" {
		// setupHint can wrap; count newlines + 1.
		n += 1 + strings.Count(m.setupHint, "\n")
	}
	if m.preset != filter.PresetAll || m.priorityCap >= 0 || m.sortBy != sortNone || m.showClosed {
		n++ // filter chip strip
	}
	if m.lastErr != nil && len(m.all) > 0 {
		n++ // refresh-failed banner
	}
	if len(m.fetchErrors) > 0 {
		n++ // per-sub fetch-error banner
	}
	if m.status != "" {
		n++ // transient write-feedback banner
	}
	if m.updateNudge != "" {
		n++ // update-available nudge
	}
	switch {
	// Every mode that renders a textinput prompt at the bottom of
	// viewList costs 2 lines of chrome — a blank separator + the
	// input itself. Both that switch and this one now read the set
	// from usesTextInput, so they cannot drift; an under-count here
	// pushes the title/last rows past the terminal edge while a
	// prompt is open.
	case m.usesTextInput():
		n += 2 // blank + single-line textinput
	case m.mode == modeNote:
		// blank separator + textarea body height. noteArea
		// height is fixed (6 by default) but read it from the
		// component so a future SetHeight call stays in sync.
		n += 1 + m.noteArea.Height()
	case m.mode == modeConfirmClose:
		if m.pendingTarget.ID != "" || len(m.marked) > 0 {
			n += 2 // blank + confirm prompt
		}
	}
	return n
}

// bodyHeight is the number of issue rows the viewport will render
// given the current terminal height and chrome state. Floors at 1
// so we always show at least one row (and at least one ↑/↓ hint)
// regardless of how cramped the terminal is. If m.height is zero
// (before the first WindowSizeMsg arrives), fall back to a generous
// default so the initial paint isn't a one-line stub.
func (m Model) bodyHeight() int {
	if m.height <= 0 {
		return 20
	}
	h := m.height - chromeMinOverhead - m.chromeExtra()
	// Reserve a line for each "+N more" hint that may render.
	// Always subtract one — we'd rather under-fill by a row than
	// over-fill and clip the cursor row off the bottom.
	h -= 2
	if h < 1 {
		h = 1
	}
	return h
}

// ensureCursorVisible adjusts m.scroll so m.cursor falls inside the
// rendered window. Called after every cursor mutation (j, k, g, G,
// jump-to-human) and whenever m.visible shrinks or grows. The same
// math runs at render time via bodyHeight, so the two agree.
func (m *Model) ensureCursorVisible() {
	h := m.bodyHeight()
	if h < 1 {
		h = 1
	}
	maxScroll := len(m.visible) - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	} else if m.cursor >= m.scroll+h {
		m.scroll = m.cursor - h + 1
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// renderListTitle renders the list view's title line. Shared by
// viewList and rowsStartY so the hit-test chrome math can't drift
// from the render if the title ever gains dynamic content — same
// drift protection the chip strip gets from sharing renderFilterChips.
func renderListTitle() string {
	return titleStyle.Render("would-you-kindly")
}

// chromeRows returns how many terminal rows a rendered chrome string
// occupies: one per hard newline-separated line, regardless of how
// wide the line is. That matches bubbletea's standard renderer, which
// TRUNCATES any line wider than the window rather than letting the
// terminal soft-wrap it (standard_renderer.go: `ansi.Truncate(line,
// r.width, "")` whenever the width is known) — so an over-wide
// setupHint or chip strip still occupies exactly one screen row.
// Counting soft wrap here would overcount and skew clicks downward
// (roborev #2035).
func chromeRows(rendered string) int {
	return 1 + strings.Count(rendered, "\n")
}

// rowsStartY returns the Y-coordinate (zero-indexed from the top
// of the rendered output) at which the first table row lands.
// Mirrors the viewList chrome ordering — bumping any conditional
// chrome there means bumping it here too (the title and chip-strip
// lines come from the SAME render helpers viewList uses, so the two
// can't disagree on content or on when the strip renders). We use
// this to map a click's msg.Y back to a row index.
func (m Model) rowsStartY() int {
	y := chromeRows(renderListTitle())
	if m.setupHint != "" {
		y += chromeRows(setupHintStyle.Render(m.setupHint))
	}
	if chips := renderFilterChips(m.preset, m.priorityCap, m.sortBy, m.showClosed); chips != "" {
		y += chromeRows(chips)
	}
	y++ // blank line between header chrome and table header
	y++ // table header
	return y
}

// renderFilterChips builds the filter-strip line shown above the
// table. Returns the empty string when nothing is filtered (preset
// is the default `all`, no priority cap, no sort, no show-closed)
// so a fresh view stays chrome-free. Each active filter renders
// as an amber pill.
func renderFilterChips(p filter.Preset, priorityCap int, sortBy sortKey, showClosed bool) string {
	var parts []string
	if p != filter.PresetAll {
		parts = append(parts, chipActiveStyle.Render(" "+string(p)+" "))
	}
	if priorityCap >= 0 {
		label := fmt.Sprintf(" ≤P%d ", priorityCap)
		if priorityCap == 0 {
			label = " P0 only "
		}
		parts = append(parts, chipActiveStyle.Render(label))
	}
	if sortBy != sortNone {
		parts = append(parts, chipActiveStyle.Render(" ↕ "+sortBy.label()+" "))
	}
	if showClosed {
		parts = append(parts, chipActiveStyle.Render(" +closed "))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// emptyMatchCopy returns the preset-aware "no rows match this
// filter" copy followed by a recovery hint on a second line so a
// user who hits an empty view doesn't have to guess the next
// keystroke. The human preset gets a small celebration since
// "nothing flagged for you" is the goal state; other presets
// describe the absence factually + nudge toward a useful next
// step (cycle presets, include closed, clear the filter).
func emptyMatchCopy(p filter.Preset, query string) string {
	if query != "" {
		return fmt.Sprintf("no matches for %q\n  → / esc to clear the fuzzy filter, or Tab to cycle presets", query)
	}
	switch p {
	case filter.PresetHuman:
		return "✓ no human-flagged issues — nothing waiting on you right now\n  → Tab cycles presets; r refreshes if you expected a hand-off"
	case filter.PresetReady:
		return "no ready work — everything left is blocked or in progress\n  → Tab to cycle to `human` or `blocked`; press `[` to jump human-flagged rows"
	case filter.PresetMine:
		return "nothing assigned to you in this workspace\n  → Tab cycles presets; check `-me` if you expected rows"
	case filter.PresetBlocked:
		return "no blocked issues — work is flowing\n  → Tab cycles presets"
	case filter.PresetReview:
		return "no open review findings — roborev hasn't filed anything (or it's all handled)\n  → Tab cycles presets; this view tracks issues roborev's beads hook files"
	default:
		return "no issues match this view\n  → C includes closed rows; / opens a fuzzy filter; Tab cycles presets"
	}
}

// firstRunEmptyCopy is shown when bd has no issues at all (fresh
// workspace, no rows ever fetched). Points the user at the most
// likely next action.
func firstRunEmptyCopy() string {
	return "no issues yet — try `wyk handoff -create \"<title>\"` to file your first one, or `bd create \"<title>\"` directly"
}

func friendlyError(err error) string {
	switch {
	case errors.Is(err, beads.ErrBDNotFound):
		return "bd is not installed (or not on PATH). Install from https://github.com/gastownhall/beads"
	case errors.Is(err, beads.ErrNoWorkspace):
		return "no beads workspace here. Run `bd init` in your repo root."
	default:
		return "error: " + err.Error()
	}
}

// trunc shortens s to fit n DISPLAY CELLS (not runes), appending an
// ellipsis when it cuts. Width is measured with dispWidth, the same
// measure the TUI uses to size columns, so a double-wide rune (CJK/emoji,
// and ambiguous-width glyphs under ambWide) costs 2 — a rune-count budget
// let such titles render up to ~2x their column and break table alignment
// (would-you-kindly-qabo). The ellipsis "…" is itself ambiguous-width (2
// cells under ambWide), so we reserve ITS measured width, not a hardcoded
// 1 — otherwise the result would be one cell over budget. The result
// always satisfies dispWidth(trunc(s, n)) <= n.
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if dispWidth(s) <= n {
		return s
	}
	const ell = "…"
	ew := dispWidth(ell)
	if n < ew {
		// No room for the ellipsis at all — return as much leading
		// content as fits in n cells, unmarked.
		return fitCells(s, n)
	}
	return fitCells(s, n-ew) + ell
}

// truncID fits a bd issue ID into width cells while preserving the half
// that tells two rows apart.
//
// IDs are `<workspace>-<suffix>` and plain trunc ellipsizes from the
// RIGHT — it keeps the workspace name every row in that repo shares and
// drops the suffix. On a terminal too narrow for the full ID, every row
// would then render the identical `would-you-kind…`, which is worse
// than the prefix-trimmed `2oa`/`1ej` this column used to show
// (roborev #4028). So elide the MIDDLE instead: the suffix is kept
// whole and whatever budget remains goes to the leading workspace name,
// giving `would-you-ki…2oa` — unique per row and still recognisable.
func truncID(id string, width int) string {
	if width <= 0 {
		return ""
	}
	if dispWidth(id) <= width {
		return id
	}
	const ell = "…"
	ew := dispWidth(ell)
	if width <= ew {
		return fitCells(id, width)
	}
	// Everything after the last `-` is the discriminating suffix.
	// An ID with no `-` (or a trailing one) has no split point, so the
	// whole string is treated as the part worth keeping and falls
	// through to the trailing-run branch below.
	suffix := ""
	if idx := strings.LastIndex(id, "-"); idx >= 0 && idx+1 < len(id) {
		suffix = id[idx+1:]
	}
	if head := width - ew - dispWidth(suffix); suffix != "" && head >= 1 {
		return fitCells(id, head) + ell + suffix
	}
	// The suffix alone doesn't leave room for any workspace name: keep
	// the trailing run, which still differs row to row.
	return ell + fitTrailingCells(id, width-ew)
}

// fitCells returns the longest leading run of s whose display width
// (dispWidth) is <= budget, never splitting a rune. budget <= 0 -> "".
func fitCells(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := dispWidth(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

// fitTrailingCells is fitCells from the other end: the longest TRAILING
// run of s whose display width is <= budget, never splitting a rune.
// Used by truncID, where the tail is the part worth keeping.
func fitTrailingCells(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	r := []rune(s)
	used, i := 0, len(r)
	for i > 0 {
		w := dispWidth(string(r[i-1]))
		if used+w > budget {
			break
		}
		used += w
		i--
	}
	return string(r[i:])
}
