package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/bubbles/viewport"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// Split layout: on a wide-enough terminal the list takes a left pane and
// the cursor issue's detail (badge, title, meta, labels, runbook, notes,
// deps) renders in a right pane that follows the cursor. Modelled on
// roborev's queue + review split — the human's job in wyk is reading
// runbooks, and a split view removes the ⏎/esc round-trip per row.
//
// Focus follows mode: modeList = list focused (pane is a live preview),
// modeDetail = pane focused (the existing detail keys — a/d/n/c, tab
// links, j/k scroll — work unchanged; esc returns to the list). Below
// the size threshold, or with the pane hidden via `p`, the stacked
// single-column layout is exactly what it was before.

// layoutPref is the user's split-vs-stacked choice, persisted in
// state.json. The zero value (auto) is the default.
type layoutPref int

const (
	layoutAuto    layoutPref = iota // split when the terminal fits splitMinWidth×splitMinHeight
	layoutStacked                   // never split (pane hidden via `p`)
	layoutSplit                     // split whenever the terminal clears the smaller forced floor
)

// layoutPrefFromLabel / label round-trip the pref through state.json as
// a readable word rather than an enum int, like sortKey does.
func layoutPrefFromLabel(s string) layoutPref {
	switch s {
	case "stacked":
		return layoutStacked
	case "split":
		return layoutSplit
	}
	return layoutAuto
}

func (p layoutPref) label() string {
	switch p {
	case layoutStacked:
		return "stacked"
	case layoutSplit:
		return "split"
	}
	return ""
}

const (
	// splitMinWidth/Height are the auto-split breakpoint (roborev's).
	splitMinWidth  = 140
	splitMinHeight = 36
	// splitForceMinWidth/Height are the floor for a user who has
	// explicitly asked for the pane on a smaller terminal — the list
	// auto-hides columns to cope, but below this nothing is legible.
	splitForceMinWidth  = 100
	splitForceMinHeight = 20
	// The pane takes ~45% of the width, clamped so runbook prose wraps
	// at a readable measure and the list keeps its columns.
	splitPaneMinWidth = 50
	splitPaneMaxWidth = 100
	// splitDivider separates the panes on every body row.
	splitDivider = " │ "
	// detailFollowDebounce coalesces held-down j/k into one bd show
	// per landing row (roborev uses 75ms too). The slim row from the
	// list paints immediately; only the notes/deps enrichment waits.
	detailFollowDebounce = 75 * time.Millisecond
)

// detailFollowMsg fires after detailFollowDebounce; gen guards against
// a tick scheduled for a row the cursor has since left.
type detailFollowMsg struct{ gen int }

// splitFits reports whether the terminal clears the auto-split
// breakpoint.
func (m Model) splitFits() bool {
	return m.width >= splitMinWidth && m.height >= splitMinHeight
}

// splitActive reports whether the split composition should be used,
// given the terminal size and the user's pref.
func (m Model) splitActive() bool {
	switch m.layoutPref {
	case layoutStacked:
		return false
	case layoutSplit:
		return m.width >= splitForceMinWidth && m.height >= splitForceMinHeight
	default:
		return m.splitFits()
	}
}

// splitView reports whether the CURRENT mode renders through the split
// composition. The list, the detail (pane focused) and every prompt
// that overlays the list do; the full-screen overlays (help, columns,
// :bd output) don't.
func (m Model) splitView() bool {
	if !m.splitActive() {
		return false
	}
	switch m.mode {
	case modeHelp, modeColumns, modeOutput:
		return false
	}
	return true
}

// paneFocused is true when keys route to the detail pane.
func (m Model) paneFocused() bool {
	if m.mode == modeDetail {
		return true
	}
	switch m.mode {
	case modeConfirmClose, modeDefer, modeNote:
		return m.promptReturn == modeDetail
	}
	return false
}

// detailShown reports whether the detail surface (full-screen or pane)
// is on screen, so async enrichment (bd show, dep edges) landing for
// m.detailIssue should be adopted. Replaces the bare mode == modeDetail
// gate those handlers used before the pane existed.
func (m Model) detailShown() bool {
	return m.mode == modeDetail || m.splitView()
}

// splitGeom is the per-paint pane math: list width, pane width, and the
// number of body rows both panes fill.
type splitGeom struct {
	listW, paneW, rows int
}

// splitGeometry derives the pane rectangles from the terminal size and
// the same vertical budget the stacked list uses (bodyHeight rows plus
// the table header and the two "+N more" hint slots), so the top and
// bottom chrome are identical between layouts.
func (m Model) splitGeometry() splitGeom {
	paneW := m.width * 45 / 100
	paneW = min(max(paneW, splitPaneMinWidth), splitPaneMaxWidth)
	listW := m.width - paneW - lipgloss.Width(splitDivider)
	if listW < 20 {
		listW = 20
	}
	return splitGeom{listW: listW, paneW: paneW, rows: m.bodyHeight() + 3}
}

// paneViewportDims returns the detail viewport's width and height inside
// the pane: the pane minus its fixed header lines and the footer line.
func (m Model) paneViewportDims() (int, int) {
	g := m.splitGeometry()
	h := g.rows - len(m.paneHeaderLines(m.detailIssue, g.paneW)) - 1
	return g.paneW, max(h, 1)
}

// syncDetailViewport sizes the detail viewport for the current layout:
// the pane's inner rectangle when the split composition is up, the
// full-screen detail budget otherwise. Called by the Update wrapper
// after every message (banners, prompts and the pane's own header can
// all change the height), so the stored Height — which drives the
// viewport's scroll clamp and page size — never drifts from what View
// paints.
func (m *Model) syncDetailViewport() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	if m.splitView() {
		m.detailVP.Width, m.detailVP.Height = m.paneViewportDims()
		return
	}
	m.detailVP.Width = m.width
	m.detailVP.Height = max(m.height-detailChromeHeight, 1)
}

// followCursor keeps the pane on the cursor row while the list is
// focused. The slim row from the list is staged immediately (title,
// description and labels paint on the very next frame); the bd-show
// enrichment (notes) and dep-edge lookups are debounced so holding j
// doesn't fan out a shell-out per intermediate row. Also re-stages when
// the SAME row's list fields changed under us (a refetch after a write),
// carrying the already-loaded notes across. Returns the debounce tick,
// or nil when nothing moved.
func (m *Model) followCursor() tea.Cmd {
	if !m.splitActive() || m.mode != modeList || len(m.visible) == 0 {
		return nil
	}
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	cur := m.visible[m.cursor]
	same := issueKey(cur) == issueKey(m.detailIssue)
	if same && !rowChanged(cur, m.detailIssue) {
		return nil
	}
	if same {
		// Same issue, fresher list fields: keep notes + scroll, and
		// the dep-link selection is meaningless in list focus anyway.
		cur.Notes = m.detailIssue.Notes
		prev := m.detailVP.YOffset
		m.detailIssue = cur
		m.detailVP.SetContent(m.renderDetailBody(cur))
		m.detailVP.SetYOffset(prev)
	} else {
		m.detailIssue = cur
		m.detailLinkIdx = -1
		m.detailStack = nil
		m.detailVP.SetContent(m.renderDetailBody(cur))
		m.detailVP.GotoTop()
	}
	m.followGen++
	gen := m.followGen
	return tea.Tick(detailFollowDebounce, func(time.Time) tea.Msg { return detailFollowMsg{gen: gen} })
}

// rowChanged reports whether the list-carried fields of a differ from
// the staged detail copy b — the signal that a refetch brought a newer
// version of the row the pane is showing.
func rowChanged(a, b beads.Issue) bool {
	if a.Title != b.Title || a.Status != b.Status || a.Priority != b.Priority ||
		a.IssueType != b.IssueType || a.Assignee != b.Assignee ||
		a.Description != b.Description || !a.UpdatedAt.Equal(b.UpdatedAt) ||
		a.BlockedByHuman != b.BlockedByHuman {
		return true
	}
	if len(a.Labels) != len(b.Labels) {
		return true
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return true
		}
	}
	return false
}

// handleDetailFollow runs the debounced enrichment for the row the pane
// landed on. Stale ticks (the cursor moved again) and ticks that outlive
// the split (terminal shrank, pane hidden) are dropped.
func (m Model) handleDetailFollow(msg detailFollowMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.followGen || !m.splitActive() || m.detailIssue.ID == "" {
		return m, nil
	}
	return m, m.detailEnrichCmd(m.detailIssue)
}

// toggleLayout is the `p` key: hide the pane (stacked) or show it. On a
// terminal below the auto breakpoint, showing forces the split down to
// the smaller floor; below even that, the pref is left alone and the
// status line says why nothing happened.
func (m Model) toggleLayout() (tea.Model, tea.Cmd) {
	if m.splitActive() {
		m.layoutPref = layoutStacked
		m.setStatus("detail pane hidden — p shows it again")
		return m, nil
	}
	if m.splitFits() {
		m.layoutPref = layoutAuto
	} else {
		m.layoutPref = layoutSplit
	}
	if !m.splitActive() {
		m.setStatus(fmt.Sprintf("terminal too small for a detail pane (needs at least %d×%d)", splitForceMinWidth, splitForceMinHeight))
		return m, nil
	}
	m.setStatus("detail pane shown — ⏎ focuses it, esc returns, p hides it")
	return m, nil
}

// viewSplit composes the split screen: the list's top chrome (title,
// setup hint, chip strip) full-width, then rows of list | pane, then
// the list's bottom chrome (prompts, banners, status bar) full-width.
// Reusing the stacked view's chrome builders keeps rowsStartY's click
// math and chromeExtra's height budget valid without a second copy.
func (m Model) viewSplit() string {
	g := m.splitGeometry()
	var b strings.Builder
	b.WriteString(m.listTopChrome())
	left := m.splitListLines(g.listW, g.rows)
	right := m.paneLines(g.paneW, g.rows)
	div := helpStyle.Render(splitDivider)
	if m.paneFocused() {
		div = cursorStyle.Render(splitDivider)
	}
	for r := 0; r < g.rows; r++ {
		b.WriteString(left[r])
		b.WriteString(div)
		b.WriteString(right[r])
		b.WriteByte('\n')
	}
	b.WriteString(m.listBottomChrome())
	return b.String()
}

// splitListLines renders the table (header, the scrolled row window,
// the ↑/↓ hints) for a pane of the given width, as exactly `rows`
// lines each padded/truncated to width. Column sizing and auto-hide
// run against the PANE width on a scratch copy of the model, while the
// row window comes from the real bodyHeight so it agrees with
// ensureCursorVisible.
func (m Model) splitListLines(width, rows int) []string {
	h := m.bodyHeight()
	t := m
	t.width = width
	t.cw = t.computeColWidths(t.visible)
	t.autoHidden = t.computeAutoHidden()

	var lines []string
	switch {
	case len(t.all) > 0:
		lines = append(lines, t.renderHeader())
		if len(t.visible) == 0 {
			lines = append(lines, strings.Split(emptyStyle.Render(emptyMatchCopy(t.preset, t.query)), "\n")...)
			break
		}
		start := t.scroll
		end := min(start+h, len(t.visible))
		if start > end {
			start = end
		}
		for i := start; i < end; i++ {
			lines = append(lines, t.renderRow(t.visible[i], i == t.cursor))
		}
		if start > 0 {
			lines = append(lines, emptyStyle.Render(fmt.Sprintf("  ↑ %d more above", start)))
		}
		if end < len(t.visible) {
			lines = append(lines, emptyStyle.Render(fmt.Sprintf("  ↓ %d more below", len(t.visible)-end)))
		}
	case t.lastErr != nil:
		lines = append(lines, errorStyle.Render(friendlyError(t.lastErr)), "",
			emptyStyle.Render("press r to retry, q to quit"))
	case t.loading:
		lines = append(lines, t.spinner.View()+emptyStyle.Render(" loading…"))
	case t.preset != filter.PresetAll:
		lines = append(lines, strings.Split(emptyStyle.Render(emptyMatchCopy(t.preset, t.query)), "\n")...)
	default:
		lines = append(lines, emptyStyle.Render(firstRunEmptyCopy()))
	}
	return fitLines(lines, width, rows)
}

// paneLines renders the detail pane: fixed header (badge, title, meta,
// labels), the scrolling body viewport, and a one-line footer with the
// scroll position / focus hint. Exactly `rows` lines of `width` cells.
func (m Model) paneLines(width, rows int) []string {
	i := m.detailIssue
	if i.ID == "" {
		return fitLines([]string{emptyStyle.Render("no issue selected")}, width, rows)
	}
	header := m.paneHeaderLines(i, width)
	vp := m.detailVP
	vp.Width = width
	vp.Height = max(rows-len(header)-1, 1)
	vp.SetContent(m.renderDetailBody(i))
	lines := append(header, strings.Split(vp.View(), "\n")...)
	lines = append(lines, m.paneFooter(vp, i))
	return fitLines(lines, width, rows)
}

// paneHeaderLines is the pane's fixed identity block — the same
// content as the full-screen detail's header, packed tighter (no
// margin between title and meta) because the pane is short. The title
// word-wraps to the pane width rather than truncating; a runbook's
// title is often the whole instruction.
func (m Model) paneHeaderLines(i beads.Issue, width int) []string {
	if i.ID == "" {
		return nil
	}
	var lines []string
	if badge := responsibilityBadgeFor(i); badge != "" {
		lines = append(lines, badge)
	}
	title := sanitizeInline(i.Title)
	if width > 0 {
		title = lipgloss.NewStyle().Width(width).Render(title)
	}
	for _, l := range strings.Split(title, "\n") {
		lines = append(lines, paneTitleStyle.Render(strings.TrimRight(l, " ")))
	}
	lines = append(lines, fmt.Sprintf("%s  %s  %s  P%d",
		idStyle.Render(i.ID),
		statusStyleFor(i.Status).Render(i.Status),
		i.IssueType,
		i.Priority,
	))
	if len(i.Labels) > 0 {
		lines = append(lines, detailLabelStyle.Render("labels: ")+sanitizeInline(strings.Join(i.Labels, ", ")))
	}
	lines = append(lines, "")
	return lines
}

// paneFooter is the pane's last line: the scroll window when the body
// overflows (roborev's "[a-b of N lines]"), plus the focus hint while
// the list has focus so ⏎ is discoverable as "read this".
func (m Model) paneFooter(vp viewport.Model, i beads.Issue) string {
	var parts []string
	if h := vp.Height; vp.TotalLineCount() > h {
		total := vp.TotalLineCount()
		start := int(vp.ScrollPercent()*float64(total-h) + 0.5)
		parts = append(parts, fmt.Sprintf("[%d-%d of %d lines]", start+1, min(start+h, total), total))
	}
	if !m.paneFocused() {
		parts = append(parts, "⏎ read")
	} else {
		act := "a close"
		if i.Status == "closed" {
			act = "a reopen"
		}
		parts = append(parts, act, "esc back")
	}
	return helpStyle.Render(strings.Join(parts, " ▕ "))
}

// fitLines pads/truncates every line to exactly width cells and the
// slice to exactly rows entries, so two panes zip together with an
// aligned divider. ANSI-aware: lipgloss.Width measures the visible
// cells and MaxWidth truncates without cutting an escape sequence.
func fitLines(lines []string, width, rows int) []string {
	out := make([]string, 0, rows)
	for _, l := range lines {
		if len(out) == rows {
			break
		}
		out = append(out, fitCell(l, width))
	}
	blank := strings.Repeat(" ", width)
	for len(out) < rows {
		out = append(out, blank)
	}
	return out
}

// fitCell truncates s to width cells (ANSI-aware) and pads with spaces
// to exactly width.
func fitCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) > width {
		s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// handleSplitMouse routes a mouse event by which pane the pointer is
// over: the list side selects rows / scrolls the list (and hands focus
// back to the list if the pane had it); the pane side scrolls the
// detail body, and a click there focuses the pane. Only reachable
// while the list or the pane has focus — prompts drop mouse input as
// they do in the stacked layout.
func (m Model) handleSplitMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeList && m.mode != modeDetail {
		return m, nil
	}
	if msg.Action == tea.MouseActionRelease {
		return m, nil
	}
	g := m.splitGeometry()
	overPane := msg.X >= g.listW+lipgloss.Width(splitDivider)
	if overPane {
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			var cmd tea.Cmd
			m.detailVP, cmd = m.detailVP.Update(msg)
			return m, cmd
		case tea.MouseButtonLeft:
			if m.mode == modeList && m.detailIssue.ID != "" {
				m.mode = modeDetail
			}
		}
		return m, nil
	}
	if m.mode == modeDetail {
		// A click or wheel on the list side while reading: focus
		// returns to the list, then the event lands as usual.
		m.mode = modeList
	}
	return m.handleMouse(msg)
}

// paneHelp is the short-help set the status bar shows while the pane
// has focus: the detail actions the user can press right now. Mirrors
// updateDetail's dispatch — a binding here without a handler there is
// a lie in the footer.
func (k keyMap) paneHelp(writable bool) []key.Binding {
	out := []key.Binding{k.Back, k.PaneScroll, k.PaneLink}
	if writable {
		out = append(out, k.Close, k.Defer, k.AddNote)
	}
	return append(out, k.PaneCopy, k.Layout, k.Help, k.Quit)
}
