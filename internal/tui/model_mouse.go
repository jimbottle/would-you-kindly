package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Mouse interaction with the list: wheel, click-to-select, click-again
// to open, header clicks that sort, the ↑/↓ overflow hints as page
// buttons, and a scrollbar that can be clicked or dragged. All of it
// is hit-tested against the same builders the view paints with
// (rowsStartY, headerCells, listScrollbar) so the click math can't
// drift from the render.

// scrollbarGutter is the width the title column gives up when the
// scrollbar is showing: one space of breathing room plus the bar.
const scrollbarGutter = 2

// Scrollbar glyphs: a dim track with a solid thumb, one cell wide.
const (
	scrollTrackGlyph = "│"
	scrollThumbGlyph = "┃"
)

// scrollbar is the list scrollbar's geometry for one paint. It only
// exists when the list overflows its window (listScrollbar returns
// ok=false otherwise) — a fully-visible list stays chrome-free.
type scrollbar struct {
	x        int // column the bar occupies
	y0       int // screen row of the first body row (rowsStartY)
	h        int // track height — the row window (bodyHeight)
	thumbTop int // thumb offset within the track
	thumbLen int // thumb height, ≥ 1
}

// listOverflows reports whether the visible list is taller than its
// window, i.e. whether the scrollbar (and the title gutter it needs)
// is showing.
func (m Model) listOverflows() bool {
	return len(m.visible) > m.bodyHeight()
}

// listScrollbar computes the scrollbar for a list rendered listWidth
// cells wide (m.width stacked, the pane width split). The thumb's
// length is the window's share of the list and its position the
// scroll's share of the travel, both floored at one cell so a huge
// list still shows a thumb and the thumb can always reach both ends.
func (m Model) listScrollbar(listWidth int) (scrollbar, bool) {
	h := m.bodyHeight()
	total := len(m.visible)
	if listWidth <= 0 || total <= h {
		return scrollbar{}, false
	}
	thumbLen := max(1, h*h/total)
	if thumbLen > h {
		thumbLen = h
	}
	travel := h - thumbLen
	maxScroll := total - h
	thumbTop := 0
	if travel > 0 && maxScroll > 0 {
		// Round to nearest so the thumb sits at the very bottom
		// exactly when the list is scrolled to the end.
		thumbTop = (m.scroll*travel + maxScroll/2) / maxScroll
		// Keep the ends honest — any scroll moves the thumb off the
		// top, and only the end reaches the bottom — when there's
		// room for both nudges. With a single cell of travel they'd
		// cancel each other out, so plain rounding wins there.
		if travel >= 2 {
			if m.scroll > 0 && thumbTop == 0 {
				thumbTop = 1
			}
			if m.scroll < maxScroll && thumbTop == travel {
				thumbTop = travel - 1
			}
		}
	}
	return scrollbar{
		x:        listWidth - 1,
		y0:       m.rowsStartY(),
		h:        h,
		thumbTop: thumbTop,
		thumbLen: thumbLen,
	}, true
}

// cell renders the bar's glyph for track row r (0-based from the top
// of the window).
func (sb scrollbar) cell(r int) string {
	if r >= sb.thumbTop && r < sb.thumbTop+sb.thumbLen {
		return cursorStyle.Render(scrollThumbGlyph)
	}
	return helpStyle.Render(scrollTrackGlyph)
}

// withScrollbar pads a rendered body row out to the bar's column and
// appends its glyph for track row r. Rows shorter than the line
// (short titles) get spaces; the title budget already reserved the
// gutter so no row can reach the bar's column on its own.
func (sb scrollbar) withScrollbar(row string, r int) string {
	pad := sb.x - lipgloss.Width(row)
	if pad < 1 {
		pad = 1
	}
	return row + strings.Repeat(" ", pad) + sb.cell(r)
}

// maxScroll is the largest scroll offset that still fills the window.
func (m Model) maxScroll() int {
	return max(0, len(m.visible)-m.bodyHeight())
}

// setScroll moves the window to the given offset (clamped) and pulls
// the cursor inside it. This is the inverse of ensureCursorVisible:
// the window leads and the cursor follows, which is what paging and
// scrollbar drags mean — the user is moving the view, not the row.
func (m *Model) setScroll(offset int) {
	h := m.bodyHeight()
	m.scroll = min(max(offset, 0), m.maxScroll())
	if m.cursor < m.scroll {
		m.cursor = m.scroll
	} else if m.cursor >= m.scroll+h {
		m.cursor = m.scroll + h - 1
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// pageList scrolls the window by whole pages (negative = up).
func (m *Model) pageList(pages int) {
	m.setScroll(m.scroll + pages*m.bodyHeight())
}

// handleMouse interprets a tea.MouseMsg against the list view. The
// list is laid out (top to bottom) as: chrome, the header line, the
// row window, then the ↑/↓ overflow hints; the scrollbar runs down
// the window's right edge. listWidth is the list's own width — the
// terminal's in the stacked layout, the list pane's in the split —
// and allowOpen says whether a click on the already-selected row
// opens it (handleSplitMouse withholds that for the click that only
// returns focus from the pane).
func (m Model) handleMouse(msg tea.MouseMsg, listWidth int, allowOpen bool) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionRelease:
		// A release is the tail of a gesture we already handled
		// (wheel ticks never emit one); it only ends a drag.
		m.dragScroll = false
		return m, nil
	case tea.MouseActionMotion:
		if m.dragScroll && msg.Button == tea.MouseButtonLeft {
			return m.dragScrollbar(msg, listWidth)
		}
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.cursor > 0 {
			m.cursor--
			m.ensureCursorVisible()
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			m.ensureCursorVisible()
		}
		return m, nil
	case tea.MouseButtonLeft:
		return m.handleLeftClick(msg, listWidth, allowOpen)
	}
	return m, nil
}

// handleLeftClick dispatches a press by what it landed on: the
// scrollbar, the header, a row, or an overflow hint. Anything else
// (title, chip strip, banners, past the last line) is a no-op.
func (m Model) handleLeftClick(msg tea.MouseMsg, listWidth int, allowOpen bool) (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	y0 := m.rowsStartY()
	if sb, ok := m.listScrollbar(listWidth); ok && msg.X == sb.x {
		return m.clickScrollbar(msg, sb)
	}
	if msg.Y == y0-1 {
		return m.clickHeader(msg.X, listWidth)
	}
	rowY := msg.Y - y0
	if rowY < 0 {
		return m, nil
	}
	// The rendered window: bodyHeight rows, or fewer when the list
	// ends first. Clicks past it land on the hint lines (below).
	window := min(len(m.visible)-m.scroll, m.bodyHeight())
	if rowY < window {
		target := m.scroll + rowY
		if target < 0 || target >= len(m.visible) {
			return m, nil
		}
		if target == m.cursor && allowOpen {
			// A click on the row that's already selected is the
			// mouse's Enter: open it (or focus the split pane).
			return m.openCursorRow()
		}
		m.cursor = target
		m.ensureCursorVisible()
		return m, nil
	}
	// The ↑/↓ overflow hints double as page buttons. They render in
	// the same order and under the same conditions as the view.
	hint := rowY - window
	above := m.scroll > 0
	below := m.scroll+window < len(m.visible)
	switch {
	case hint == 0 && above:
		m.pageList(-1)
	case hint == 0 && below, hint == 1 && above && below:
		m.pageList(+1)
	}
	return m, nil
}

// clickScrollbar handles a press in the bar's column: on the thumb it
// starts a drag; above or below it pages in that direction; outside
// the track (header or hint rows) it's ignored.
func (m Model) clickScrollbar(msg tea.MouseMsg, sb scrollbar) (tea.Model, tea.Cmd) {
	r := msg.Y - sb.y0
	if r < 0 || r >= sb.h {
		return m, nil
	}
	switch {
	case r < sb.thumbTop:
		m.pageList(-1)
	case r >= sb.thumbTop+sb.thumbLen:
		m.pageList(+1)
	default:
		m.dragScroll = true
		m.dragGrab = r - sb.thumbTop
	}
	return m, nil
}

// dragScrollbar maps a drag-motion row to a scroll offset: the thumb's
// top follows the pointer (minus where it was grabbed) and the scroll
// is that position's share of the travel. The cursor rides inside the
// window via setScroll.
func (m Model) dragScrollbar(msg tea.MouseMsg, listWidth int) (tea.Model, tea.Cmd) {
	sb, ok := m.listScrollbar(listWidth)
	if !ok {
		m.dragScroll = false
		return m, nil
	}
	travel := sb.h - sb.thumbLen
	if travel <= 0 {
		return m, nil
	}
	top := msg.Y - sb.y0 - m.dragGrab
	top = min(max(top, 0), travel)
	maxScroll := m.maxScroll()
	m.setScroll((top*maxScroll + travel/2) / travel)
	return m, nil
}

// clickHeader sorts by the clicked column: a fresh axis sorts
// naturally, the active axis reverses, and non-sortable columns
// (Owner, Type, Status, Title…) do nothing.
func (m Model) clickHeader(x, listWidth int) (tea.Model, tea.Cmd) {
	// Column widths and auto-hiding are sized per paint on a scratch
	// copy (viewList / splitListLines), never stored on the model, so
	// size them the same way here — against the list's own width —
	// before walking the cells.
	t := m
	t.width = listWidth
	t.cw = t.computeColWidths(t.visible)
	t.autoHidden = t.computeAutoHidden()
	k, ok := t.headerSortAt(x)
	if !ok {
		return m, nil
	}
	if k == m.sortBy {
		return m.reverseSort()
	}
	return m.setSortKey(k)
}

// headerSortAt maps a header-line column to the sort key of the cell
// under it, walking the same cells renderHeader paints.
func (m Model) headerSortAt(x int) (sortKey, bool) {
	x0 := 2 // the cursor gutter
	for _, c := range m.headerCells() {
		x1 := x0 + c.width
		if x >= x0 && x < x1 {
			return c.sort, c.sort != sortNone
		}
		x0 = x1 + 2 // column separator
	}
	return sortNone, false
}
