package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/lease"
)

// The detail surface: modeDetail's handlers, dep-link cycling,
// enrichment, dep-cache resolution, and detail rendering.

// detailMsg carries the enriched Issue back from a Detail dispatch.
// See Update's modeDetail entry branch.
type detailMsg struct {
	issue beads.Issue
	err   error
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Quit):
		return m.quitNow()
	case keyHit(msg, m.keys.Help):
		return m.openHelp()
	case msg.String() == "c", keyHit(msg, m.keys.Yank):
		// `c` copies the issue's instructions (description + notes) to
		// the clipboard; `y` stays a silent alias for muscle memory.
		return m.handleYankDetailBody()
	case keyHit(msg, m.keys.Close):
		// `a` closes an open issue (confirm prompt) or reopens a
		// closed one (immediate) — both overlaid on the detail view.
		return m.detailCloseOrReopen()
	case keyHit(msg, m.keys.Defer):
		// `d` defers the issue. This shadows the viewport's
		// half-page-down; j/k, pgdn, space and g/G still scroll.
		return m.beginDeferDetail()
	case keyHit(msg, m.keys.Mouse):
		// The toggle matters MOST here: releasing capture to
		// click-drag-copy a long runbook shouldn't require backing
		// out to the list first.
		return m.toggleMouse()
	case keyHit(msg, m.keys.AddNote):
		// `n` appends a note. This reclaims the old `n`/`p` link-cycle
		// aliases — Tab/Shift-Tab remain the link-selection axis.
		return m.beginNoteDetail()
	case msg.Type == tea.KeyTab:
		// Cycle the dependency/dependent link selection forward. j/k
		// stay as body scroll — link nav is the separate Tab axis.
		return m.cycleDetailLink(+1)
	case msg.Type == tea.KeyShiftTab:
		return m.cycleDetailLink(-1)
	case keyHit(msg, m.keys.Open):
		// Enter opens the highlighted link (drill into the graph); with
		// nothing highlighted it backs out one level, preserving the
		// old enter==back muscle memory.
		return m.openDetailLink()
	case keyHit(msg, m.keys.Back):
		return m.detailBackOrPop()
	}
	// Forward any other key (j/k/PgUp/PgDn/g/G inside the
	// detail view, mouse wheel events, etc.) to the viewport so
	// the body scrolls.
	var cmd tea.Cmd
	m.detailVP, cmd = m.detailVP.Update(msg)
	return m, cmd
}

// detailCloseOrReopen is the `a` action in the detail view. An open
// issue routes through the same y/N confirm as the list close (here
// overlaid on the detail view via promptReturn); a closed issue is
// reopened immediately — matching the list, where reopen (`u` undo)
// carries no confirm because it's non-destructive. The detailIssue
// snapshot is the target, so a drilled-in link that isn't in the
// current filtered list still closes/reopens correctly.
func (m Model) detailCloseOrReopen() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	i := m.detailIssue
	if i.ID == "" {
		return m, nil
	}
	if i.Status == "closed" {
		mu := m.mutator()
		// Optimistically flip the local copy so the badge/footer
		// reflect the reopen before the (list-only) refetch lands.
		m.detailIssue.Status = "open"
		m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
		return m, runWriteWithIssue("reopen", i, func(ctx context.Context) error {
			return mu.Reopen(ctx, i)
		})
	}
	m.pendingTarget = i
	m.promptReturn = modeDetail
	m.mode = modeConfirmClose
	return m, nil
}

// beginDeferDetail opens the defer-until prompt for the detail issue,
// overlaid on the detail view (promptReturn = modeDetail). Mirrors
// beginDefer's single-target path but targets m.detailIssue and has
// no bulk branch.
func (m Model) beginDeferDetail() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if m.detailIssue.ID == "" {
		return m, nil
	}
	m.pendingTarget = m.detailIssue
	m.promptReturn = modeDetail
	m.mode = modeDefer
	m.input.SetValue("")
	m.input.Prompt = "defer until ▸ "
	m.input.Placeholder = "+1d, +1w, tomorrow, next monday, 2026-06-15…"
	m.input.Focus()
	return m, textinput.Blink
}

// beginNoteDetail opens the note textarea for the detail issue,
// overlaid on the detail view (promptReturn = modeDetail). Mirrors
// beginNote but targets m.detailIssue.
func (m Model) beginNoteDetail() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if m.detailIssue.ID == "" {
		return m, nil
	}
	m.pendingTarget = m.detailIssue
	m.promptReturn = modeDetail
	m.mode = modeNote
	m.noteArea.Reset()
	m.noteArea.Focus()
	return m, textarea.Blink
}

// cycleDetailLink moves the dependency/dependent selection by delta
// (wrapping), seeds the body with the new highlight, and scrolls the
// viewport so the selected link stays visible. A first press from the
// no-selection state lands on the first link (forward) or last
// (backward). No-op when the issue has no links.
func (m Model) cycleDetailLink(delta int) (tea.Model, tea.Cmd) {
	links := m.detailLinks()
	if len(links) == 0 {
		return m, nil
	}
	switch {
	case m.detailLinkIdx < 0 && delta < 0:
		m.detailLinkIdx = len(links) - 1
	case m.detailLinkIdx < 0:
		m.detailLinkIdx = 0
	default:
		m.detailLinkIdx = (m.detailLinkIdx + delta + len(links)) % len(links)
	}
	content, selLine := m.renderDetailBodyWithLine(m.detailIssue)
	m.detailVP.SetContent(content)
	m.ensureDetailLinkVisible(selLine)
	return m, nil
}

// ensureDetailLinkVisible scrolls the detail viewport the minimum
// amount needed to bring the highlighted link's line (selLine, the
// structural index from detailBody) into view. No-op for a negative
// index or an unmeasured viewport.
func (m *Model) ensureDetailLinkVisible(selLine int) {
	h := m.detailVP.Height
	if selLine < 0 || h <= 0 {
		return
	}
	switch {
	case selLine < m.detailVP.YOffset:
		m.detailVP.SetYOffset(selLine)
	case selLine >= m.detailVP.YOffset+h:
		m.detailVP.SetYOffset(selLine - h + 1)
	}
}

// detailEnrichCmd dispatches the same enrichment the list-open path
// runs for target: Detail() (full body/notes) plus dep-resolution.
// Cross-repo targets route by ID prefix through the Detailer /
// DepLister. Used by both drill-in (openDetailLink) and back (pop) so
// the restored issue is always re-enriched — covering the race where a
// SLIM parent got pushed because Enter fired before its detailMsg
// landed.
func (m Model) detailEnrichCmd(target beads.Issue) tea.Cmd {
	var cmds []tea.Cmd
	if d, ok := m.src.(Detailer); ok {
		t := target
		cmds = append(cmds, func() tea.Msg {
			full, err := d.Detail(context.Background(), t)
			return detailMsg{issue: full, err: err}
		})
	}
	if c := m.resolveDetailDeps(target.ID); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// openDetailLink drills into the highlighted dependency/dependent: it
// pushes the current (enriched) issue onto detailStack so Back returns
// here, swaps detailIssue to the link, and dispatches Detail +
// dep-resolution for it (dep-list rows carry a stamped Repo, which the
// Detailer routes on; the DepLister routes by ID prefix). With no
// valid selection it falls back to
// backing out one level, so Enter still means "back" when no link is
// highlighted.
func (m Model) openDetailLink() (tea.Model, tea.Cmd) {
	links := m.detailLinks()
	if m.detailLinkIdx < 0 || m.detailLinkIdx >= len(links) {
		return m.detailBackOrPop()
	}
	target := links[m.detailLinkIdx]
	m.detailStack = append(m.detailStack, m.detailIssue)
	m.detailIssue = target
	m.detailLinkIdx = -1
	m.detailVP.SetContent(m.renderDetailBody(target))
	m.detailVP.GotoTop()
	return m, m.detailEnrichCmd(target)
}

// detailBackOrPop is the Back/Esc behaviour: if we drilled into a link
// it pops the stack back to the issue we came from; otherwise it leaves
// the detail view for the list. The popped issue is re-enriched
// (detailEnrichCmd) rather than trusted as-is — if Enter had fired
// before the parent's original detailMsg landed, the SLIM copy is what
// was pushed, and re-fetching restores its notes (the detailMsg handler
// preserves scroll, so an already-enriched parent just re-renders
// identically).
func (m Model) detailBackOrPop() (tea.Model, tea.Cmd) {
	if n := len(m.detailStack); n > 0 {
		prev := m.detailStack[n-1]
		m.detailStack = m.detailStack[:n-1]
		m.detailIssue = prev
		m.detailLinkIdx = -1
		m.detailVP.SetContent(m.renderDetailBody(prev))
		m.detailVP.GotoTop()
		return m, m.detailEnrichCmd(prev)
	}
	m.mode = modeList
	m.detailLinkIdx = -1
	m.detailStack = nil
	return m, nil
}

// handleYankDetailBody yanks the current detail issue's
// Description + Notes verbatim (concatenated with a blank line
// separator) via the same OSC 52 path as the list-mode yanks.
// Useful when the user wants to paste the runbook into chat /
// editor / commit message without the terminal-selection dance
// — which the mouse capture would otherwise interfere with.
func (m Model) handleYankDetailBody() (tea.Model, tea.Cmd) {
	i := m.detailIssue
	if i.ID == "" {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to copy")
			return m, flashClearCmd(m.statusGen)
		}
		i = m.visible[m.cursor]
	}
	var b strings.Builder
	if strings.TrimSpace(i.Description) != "" {
		b.WriteString(i.Description)
	}
	if strings.TrimSpace(i.Notes) != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(i.Notes)
	}
	payload := b.String()
	if payload == "" {
		m.setStatus("nothing to copy (description and notes are empty)")
		return m, flashClearCmd(m.statusGen)
	}
	if err := clipboardCopy(payload); err != nil {
		m.setStatus("copy failed: " + err.Error())
		return m, nil
	}
	m.setStatus(fmt.Sprintf("copied %s instructions (%d bytes)", i.ID, len(payload)))
	return m, flashClearCmd(m.statusGen)
}

// depsResolvedMsg carries a batch of freshly-resolved dependency
// edge sets back to the model. The deps-sort path populates only
// deps/failed (forward edges) so it can switch from the count proxy
// to the real topological order; the detail-entry path additionally
// populates dependents/dependentsFailed (the reverse edge) so the
// detail view can render both sections. deps/dependents map issue ID
// → its direct dependencies / dependents; failed/dependentsFailed
// list IDs whose ListDeps / ListDependents call errored (recorded so
// the resolver doesn't re-spin them). All four are merged into the
// matching caches in Update; an empty/nil field is simply a no-op
// merge, so the deps-sort sender can keep emitting just deps/failed.
type depsResolvedMsg struct {
	deps             map[string][]beads.Issue
	failed           []string
	dependents       map[string][]beads.Issue
	dependentsFailed []string
}

// maybeResolveDeps returns a Cmd that resolves the direct
// dependencies of every visible row not already in m.depCache (or
// known-failed), so the deps sort can build a real topological
// order. Returns nil — no Cmd — when the deps sort isn't active,
// no DepLister is wired, or every visible row is already resolved;
// nil keeps the existing async/message conventions (a no-op Cmd
// would still cost a round-trip through the event loop).
//
// Resolution runs off the Bubble Tea event loop in the Cmd
// goroutine so the N `bd dep list` shell-outs never block input;
// the result arrives as a depsResolvedMsg and triggers a re-sort.
func (m Model) maybeResolveDeps() tea.Cmd {
	if m.sortBy != sortDeps || m.depLister == nil {
		return nil
	}
	// Collect the IDs we still need. Snapshot from m.visible so the
	// scope matches what's on screen; skip anything already cached
	// or already known to fail.
	var want []string
	seen := make(map[string]bool, len(m.visible))
	for _, i := range m.visible {
		if seen[i.ID] {
			continue
		}
		seen[i.ID] = true
		if _, ok := m.depCache[i.ID]; ok {
			continue
		}
		if m.depErr[i.ID] {
			continue
		}
		want = append(want, i.ID)
	}
	if len(want) == 0 {
		return nil
	}
	dl := m.depLister
	return func() tea.Msg {
		// Bound concurrency the same way the HUMAN-BLOCK scan does so
		// a large visible set doesn't fan out an unbounded number of
		// bd subprocesses.
		sem := make(chan struct{}, markBlockedByHumanConcurrency)
		var wg sync.WaitGroup
		var mu sync.Mutex
		deps := make(map[string][]beads.Issue, len(want))
		var failed []string
		for _, id := range want {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				d, err := dl.ListDeps(context.Background(), id)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failed = append(failed, id)
					return
				}
				deps[id] = d
			}(id)
		}
		wg.Wait()
		return depsResolvedMsg{deps: deps, failed: failed}
	}
}

// patchDepCacheStatus updates the rendered Status of every cached
// dependency/dependent row matching id, across both caches. The detail
// view renders these cached rows as `ID — title (status)`; without
// this, closing/reopening/deferring an issue would keep showing it
// with its old status under every issue it relates to until the cache
// entry was evicted (would-you-kindly-1ym). Handles the case the
// list-refresh path below can't: a just-closed issue leaves the
// default (open-only) list, so it would never be refreshed from m.all.
func (m *Model) patchDepCacheStatus(id, status string) {
	for _, lists := range []map[string][]beads.Issue{m.depCache, m.dependentCache} {
		for _, rows := range lists {
			for i := range rows {
				if rows[i].ID == id {
					rows[i].Status = status
				}
			}
		}
	}
}

// refreshDepCachesFromList freshens the Status/Title of cached
// dependency rows from the just-fetched m.all, by ID. This keeps the
// detail view's dep sections current after ANY refresh — a 20s poll,
// an fs-event, or an external process's write — not just this TUI's
// own mutations, so a status that drifted underneath a cached row
// updates the next time the list refreshes. A dep that isn't in the
// current list (closed-and-filtered, or cross-repo) keeps its cached
// value; the explicit patchDepCacheStatus covers the close case.
func (m *Model) refreshDepCachesFromList() {
	if len(m.depCache) == 0 && len(m.dependentCache) == 0 {
		return
	}
	byID := make(map[string]beads.Issue, len(m.all))
	for _, iss := range m.all {
		byID[iss.ID] = iss
	}
	for _, lists := range []map[string][]beads.Issue{m.depCache, m.dependentCache} {
		for _, rows := range lists {
			for i := range rows {
				if live, ok := byID[rows[i].ID]; ok {
					rows[i].Status = live.Status
					rows[i].Title = live.Title
				}
			}
		}
	}
}

// detailLinks returns the flattened, ordered list of selectable links
// for the current detail issue — its cached dependencies followed by
// its cached dependents — matching the render order in detailBody. The
// detailLinkIdx cursor and Enter-to-open both index into this slice.
func (m Model) detailLinks() []beads.Issue {
	id := m.detailIssue.ID
	links := make([]beads.Issue, 0, len(m.depCache[id])+len(m.dependentCache[id]))
	links = append(links, m.depCache[id]...)
	links = append(links, m.dependentCache[id]...)
	return links
}

// renderDetailBody builds the detail body for issue i with the current
// link selection applied. Centralises the detailBody call so the
// several re-seed sites don't each re-thread the caches + selection.
func (m Model) renderDetailBody(i beads.Issue) string {
	body, _ := m.renderDetailBodyWithLine(i)
	return body
}

// renderDetailBodyWithLine is renderDetailBody plus the line index of
// the highlighted link row (-1 if none) — used by cycleDetailLink to
// scroll the selection into view structurally.
func (m Model) renderDetailBodyWithLine(i beads.Issue) (string, int) {
	id := i.ID
	return detailBody(
		i, m.detailVP.Width,
		m.depCache[id], m.dependentCache[id],
		m.depErr[id], m.dependentErr[id],
		m.detailLinkIdx,
	)
}

func (m Model) resolveDetailDeps(id string) tea.Cmd {
	if m.depLister == nil || id == "" {
		return nil
	}
	_, depsCached := m.depCache[id]
	needDeps := !depsCached && !m.depErr[id]
	_, dependentsCached := m.dependentCache[id]
	needDependents := !dependentsCached && !m.dependentErr[id]
	if !needDeps && !needDependents {
		return nil
	}
	dl := m.depLister
	return func() tea.Msg {
		var msg depsResolvedMsg
		if needDeps {
			if d, err := dl.ListDeps(context.Background(), id); err != nil {
				msg.failed = []string{id}
			} else {
				msg.deps = map[string][]beads.Issue{id: d}
			}
		}
		if needDependents {
			if d, err := dl.ListDependents(context.Background(), id); err != nil {
				msg.dependentsFailed = []string{id}
			} else {
				msg.dependents = map[string][]beads.Issue{id: d}
			}
		}
		return msg
	}
}

// detailChromeHeight is the number of lines viewDetail emits
// outside the scrolling body — used by WindowSizeMsg to size the
// viewport correctly. Counts: badge line, blank, title, meta,
// blank, labels (assume present), blank, footer help, plus a one-
// line breathing buffer. Slightly over-counts when labels are
// missing (the viewport just gets one extra line, never less).
const detailChromeHeight = 9

// detailBody composes the scrolling-eligible portion of the
// detail view: the section headings, description, and notes.
// The fixed-chrome portion (badge, title, meta, labels, footer)
// lives in viewDetail directly so the row's identity never
// scrolls off the top.
//
// width is the viewport width; the Description and Notes bodies
// are word-wrapped to fit via lipgloss.Style.Width so a long
// runbook step doesn't overflow horizontally off the right edge.
// Width <= 0 (pre-WindowSizeMsg) skips the wrap so the very
// first paint still renders something legible — the next paint
// with a real width re-wraps correctly. Section headings stay
// unwrapped — they're short and one-liner by design.
// deps/dependents carry the issue's forward dependencies (what
// blocks it) and dependents (what it blocks), pulled from the
// model's per-issue caches by the caller; depsErr/dependentsErr flag
// a failed lookup. Each renders as a collapsing section at the
// bottom: a dimmed `ID — title (status)` list under a header, with
// the header (and section) omitted entirely when there's nothing to
// show — zero rows and no error. On error a single dimmed
// `(… unavailable)` line stands in for the list so the body still
// flows.
// selectedLink is the index into the flattened deps++dependents list
// of the currently-highlighted link (-1 = none). The matching row gets
// a cursor prefix so the user can see which link Enter will open.
// detailBody returns the rendered body AND the 0-based line index of
// the highlighted link row (-1 when nothing is selected). Returning
// the line STRUCTURALLY — rather than recovering it by scanning the
// rendered output for the ▶ glyph — means a dependency title that
// happens to contain ▶ can't mis-target the scroll.
func detailBody(i beads.Issue, width int, deps, dependents []beads.Issue, depsErr, dependentsErr bool, selectedLink int) (string, int) {
	wrap := func(s string) string {
		if width <= 0 {
			return s
		}
		return lipgloss.NewStyle().Width(width).Render(s)
	}
	var b strings.Builder
	b.WriteString(detailLabelStyle.Render("instructions"))
	b.WriteString("\n")
	if strings.TrimSpace(i.Description) == "" {
		b.WriteString(emptyStyle.Render("(no description)"))
	} else {
		b.WriteString(wrap(sanitizeBlock(i.Description)))
	}
	if strings.TrimSpace(i.Notes) != "" {
		b.WriteString("\n\n")
		b.WriteString(detailLabelStyle.Render("notes"))
		b.WriteString("\n")
		b.WriteString(wrap(sanitizeBlock(i.Notes)))
	}
	// deps occupy global indices [0, len(deps)); dependents follow, so
	// the dependents section's selection offset is len(deps).
	selLine := -1
	if l := writeDepSection(&b, width, "dependencies", deps, depsErr, "(deps unavailable)", selectedLink, 0); l >= 0 {
		selLine = l
	}
	if l := writeDepSection(&b, width, "dependents", dependents, dependentsErr, "(dependents unavailable)", selectedLink, len(deps)); l >= 0 {
		selLine = l
	}
	return b.String(), selLine
}

// writeDepSection appends one collapsing dependency section to b. It
// renders nothing when rows is empty and isErr is false (collapse).
// Otherwise it writes the header followed by either the unavailable
// line (isErr) or one dimmed `ID — title (status)` row per edge,
// truncated rune-aware to the body width so a long title can't
// overflow the viewport.
// selectedLink is the global link index highlighted across both
// sections; base is this section's offset into that flattened space
// (0 for dependencies, len(deps) for dependents). The row whose
// base+localIdx == selectedLink gets a "▶ " cursor prefix and the
// brighter cursor style so the keyboard selection is visible. Returns
// the 0-based line index of that selected row (or -1 if it isn't in
// this section) so the caller can scroll to it without a glyph scan.
func writeDepSection(b *strings.Builder, width int, header string, rows []beads.Issue, isErr bool, unavailable string, selectedLink, base int) int {
	if len(rows) == 0 && !isErr {
		return -1
	}
	b.WriteString("\n\n")
	b.WriteString(detailLabelStyle.Render(header))
	b.WriteString("\n")
	if isErr {
		b.WriteString(emptyStyle.Render(unavailable))
		return -1
	}
	selLine := -1
	for idx, r := range rows {
		if idx > 0 {
			b.WriteString("\n")
		}
		line := fmt.Sprintf("%s — %s (%s)", r.ID, sanitizeInline(r.Title), r.Status)
		if width > 2 {
			line = trunc(line, width-2)
		}
		if base+idx == selectedLink {
			// The about-to-be-written row's line index = newlines so
			// far (the leading "\n" for idx>0 was already written).
			selLine = strings.Count(b.String(), "\n")
			// Reserve the same 2-cell gutter the unselected rows get so
			// selection doesn't shift the text.
			b.WriteString(cursorStyle.Render("▶ ") + cursorStyle.Render(line))
		} else {
			b.WriteString(emptyStyle.Render("  " + line))
		}
	}
	return selLine
}

func (m Model) viewDetail() string {
	// Prefer the enriched (Detail-fetched) issue if available;
	// otherwise fall back to the slim row from the list. m.detailIssue
	// is set on entry to modeDetail; the Detail Cmd updates it
	// asynchronously with the full record (including notes).
	i := m.detailIssue
	if i.ID == "" {
		if len(m.visible) == 0 {
			return ""
		}
		i = m.visible[m.cursor]
	}

	var b strings.Builder

	// Responsibility badge on its own line above the title — for
	// a `← HUMAN` runbook the badge IS the headline. Empty when
	// the row has no responsibility signal.
	if badge := responsibilityBadgeFor(i); badge != "" {
		b.WriteString(badge)
		b.WriteString("\n\n")
	}

	b.WriteString(detailHeaderStyle.Render(sanitizeInline(i.Title)))
	b.WriteString("\n")

	meta := fmt.Sprintf("%s  %s  %s  P%d",
		idStyle.Render(i.ID),
		statusStyleFor(i.Status).Render(i.Status),
		i.IssueType,
		i.Priority,
	)
	b.WriteString(meta)
	b.WriteString("\n\n")

	if line := leaseDetailLine(i); line != "" {
		b.WriteString(detailLabelStyle.Render("lease: "))
		b.WriteString(line)
		b.WriteString("\n\n")
	}

	if len(i.Labels) > 0 {
		b.WriteString(detailLabelStyle.Render("labels: "))
		// Labels are unconstrained bd content (no charset limit), so a
		// hostile one could embed a terminal escape — sanitize like every
		// other untrusted field (roborev #1848).
		b.WriteString(sanitizeInline(strings.Join(i.Labels, ", ")))
		b.WriteString("\n\n")
	}

	// A write prompt opened from the detail view (close confirm /
	// defer input / note textarea) renders in place of the footer.
	// The note textarea is multi-line, so shrink the scrollable body
	// by its extra rows to keep the overall height stable — the
	// single-line confirm/defer prompts just reuse the footer's slot.
	// m is a value receiver, so mutating detailVP.Height here is
	// local to this paint.
	promptBlock, extraRows := m.detailPromptOverlay()
	if extraRows > 0 {
		m.detailVP.Height -= extraRows
		if m.detailVP.Height < 1 {
			m.detailVP.Height = 1
		}
	}

	// Scrollable body — viewport handles overflow. Re-seed
	// content on every paint so a direct mutation of
	// m.detailIssue (tests, future code paths) stays reflected.
	// viewport.SetContent preserves YOffset, so the user's scroll
	// position survives the refresh.
	m.detailVP.SetContent(m.renderDetailBody(i))
	b.WriteString(m.detailVP.View())

	b.WriteString("\n")
	if promptBlock != "" {
		b.WriteString(promptBlock)
		return b.String()
	}

	// Footer: scroll percent (only when there's actually
	// something to scroll) + key hint. `a` reads as reopen when the
	// issue is already closed (viewable via show-closed).
	act := "a: close"
	if i.Status == "closed" {
		act = "a: reopen"
	}
	footer := act + " ▕ d: defer ▕ n: note ▕ c: copy ▕ j/k scroll ▕ esc: back ▕ q: quit"
	// When the issue has dependency/dependent links, advertise the
	// drill-in nav: Tab highlights a link, Enter opens it.
	if len(m.detailLinks()) > 0 {
		footer = "tab: link ▕ ⏎ open ▕ " + footer
	}
	if m.detailVP.TotalLineCount() > m.detailVP.Height {
		pct := int(m.detailVP.ScrollPercent() * 100)
		footer = fmt.Sprintf("%d%% ▕ %s", pct, footer)
	}
	b.WriteString(helpStyle.Render(footer))
	return b.String()
}

// detailPromptOverlay returns the rendered write-prompt block to show
// in the detail view's footer slot, and the number of EXTRA rows it
// occupies beyond that single slot (so viewDetail can shrink the body
// to keep total height stable). An empty string means no prompt is
// active — render the normal footer. Only fires when the prompt was
// opened from the detail view (promptReturn == modeDetail).
func (m Model) detailPromptOverlay() (string, int) {
	if m.promptReturn != modeDetail {
		return "", 0
	}
	switch m.mode {
	case modeConfirmClose:
		if m.pendingTarget.ID == "" {
			return "", 0
		}
		return confirmStyle.Render(fmt.Sprintf("close %s? [y/N]", m.pendingTarget.ID)), 0
	case modeDefer:
		return m.input.View(), 0
	case modeNote:
		hint := helpStyle.Render("ctrl+s save ▕ esc cancel")
		// hint (1) + textarea (noteArea.Height()) rows, one of which
		// reuses the footer slot — the rest are the extra rows.
		return hint + "\n" + m.noteArea.View(), m.noteArea.Height()
	}
	return "", 0
}

// leaseDetailLine summarises the issue's lease for the detail view —
// holder, time left (or since lapse), branch, and whether it was stamped
// by wyk or inferred from a bare bd claim. Empty when nobody holds it.
// Owner and branch are bd content, so they're sanitized.
func leaseDetailLine(i beads.Issue) string {
	now := leaseNow()
	l := lease.Of(i, now, lease.TTL)
	if l.State == lease.None {
		return ""
	}
	owner := l.Owner
	if owner == "" {
		owner = "an unrecorded holder"
	}
	parts := []string{sanitizeInline(owner), lease.Remaining(l, now)}
	if l.Branch != "" {
		parts = append(parts, "on "+sanitizeInline(l.Branch))
	}
	if l.Implicit {
		parts = append(parts, "(inferred: claimed without wyk)")
	}
	return strings.Join(parts, " · ")
}
