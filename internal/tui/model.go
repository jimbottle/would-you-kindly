// Package tui is the Bubble Tea interface that drives would-you-kindly.
//
// The model is kept deliberately flat: a single Model struct holds the
// current issues, cursor position, mode (list, detail, filter input),
// and the active preset. Bubble Tea's Update routes key events to
// per-mode handlers that mutate the model and return commands.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// mode tracks the user's interaction context.
type mode int

const (
	modeList   mode = iota // browsing the issue list
	modeDetail             // expanded detail view of one issue
	modeFilter             // / prompt active, typing into textinput
)

// Source abstracts where issues come from so the skeleton can render
// fake data and Phase 1 can swap in the real bd CLI without touching
// the TUI. Implementations must be safe to call from a Bubble Tea
// command goroutine.
type Source interface {
	Fetch(preset filter.Preset) ([]beads.Issue, error)
}

// Model is the Bubble Tea model.
type Model struct {
	src    Source
	keys   keyMap
	mode   mode
	preset filter.Preset
	query  string

	all      []beads.Issue // last full fetch result
	visible  []beads.Issue // after fuzzy filter
	cursor   int
	width    int
	height   int
	lastErr  error
	lastSync time.Time

	input textinput.Model
}

// New constructs a Model with the given Source and a sensible default
// preset (all).
func New(src Source) Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "fuzzy filter…"
	ti.CharLimit = 200

	return Model{
		src:    src,
		keys:   defaultKeyMap(),
		mode:   modeList,
		preset: filter.PresetAll,
		input:  ti,
	}
}

// Init triggers the first fetch.
func (m Model) Init() tea.Cmd {
	return m.fetchCmd()
}

// fetchCmd asks the Source for issues matching the current preset.
func (m Model) fetchCmd() tea.Cmd {
	src, preset := m.src, m.preset
	return func() tea.Msg {
		issues, err := src.Fetch(preset)
		return fetchedMsg{issues: issues, err: err}
	}
}

type fetchedMsg struct {
	issues []beads.Issue
	err    error
}

// Update is the main event router.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case fetchedMsg:
		m.lastSync = time.Now()
		m.lastErr = msg.err
		if msg.err == nil {
			m.all = msg.issues
			m.recomputeVisible()
		}
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeDetail:
			return m.updateDetail(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Quit):
		return m, tea.Quit
	case keyHit(msg, m.keys.Down):
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case keyHit(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	case keyHit(msg, m.keys.Top):
		m.cursor = 0
	case keyHit(msg, m.keys.Bottom):
		m.cursor = max(0, len(m.visible)-1)
	case keyHit(msg, m.keys.Open):
		if len(m.visible) > 0 {
			m.mode = modeDetail
		}
	case keyHit(msg, m.keys.Filter):
		m.mode = modeFilter
		m.input.SetValue(m.query)
		m.input.Focus()
		return m, textinput.Blink
	case keyHit(msg, m.keys.Human):
		m.preset = filter.PresetHuman
		m.cursor = 0
		return m, m.fetchCmd()
	case keyHit(msg, m.keys.Cycle):
		m.preset = filter.NextPreset(m.preset)
		m.cursor = 0
		return m, m.fetchCmd()
	case keyHit(msg, m.keys.Refresh):
		return m, m.fetchCmd()
	}
	return m, nil
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyHit(msg, m.keys.Back), keyHit(msg, m.keys.Open):
		m.mode = modeList
	case keyHit(msg, m.keys.Quit):
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		m.query = m.input.Value()
		m.mode = modeList
		m.input.Blur()
		m.recomputeVisible()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.query = m.input.Value()
	m.recomputeVisible()
	return m, cmd
}

// recomputeVisible applies the in-memory fuzzy filter to m.all.
// "Fuzzy" here is a case-insensitive substring over title and
// description — enough for MVP; a true rank-based matcher can drop
// in later without changing the call site.
func (m *Model) recomputeVisible() {
	if m.query == "" {
		m.visible = m.all
	} else {
		q := strings.ToLower(m.query)
		out := make([]beads.Issue, 0, len(m.all))
		for _, i := range m.all {
			if strings.Contains(strings.ToLower(i.Title), q) ||
				strings.Contains(strings.ToLower(i.Description), q) {
				out = append(out, i)
			}
		}
		m.visible = out
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// View dispatches to the per-mode renderer.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	var b strings.Builder

	header := titleStyle.Render("would-you-kindly")
	b.WriteString(header)
	b.WriteString("\n\n")

	switch {
	case m.lastErr != nil:
		b.WriteString(errorStyle.Render("error: " + m.lastErr.Error()))
		b.WriteString("\n\n")
		b.WriteString(emptyStyle.Render("press r to retry, q to quit"))
	case len(m.all) == 0:
		b.WriteString(emptyStyle.Render("no issues — bd returned an empty list"))
	case len(m.visible) == 0:
		b.WriteString(emptyStyle.Render(fmt.Sprintf("no matches for %q", m.query)))
	default:
		for i, issue := range m.visible {
			b.WriteString(m.renderRow(issue, i == m.cursor))
			b.WriteByte('\n')
		}
	}

	// filter input lives just above the status bar when active
	if m.mode == modeFilter {
		b.WriteString("\n")
		b.WriteString(m.input.View())
	}

	b.WriteString("\n")
	b.WriteString(m.statusBar())
	return b.String()
}

func (m Model) renderRow(i beads.Issue, selected bool) string {
	cursor := "  "
	if selected {
		cursor = cursorStyle.Render("▶ ")
	}

	st := statusStyleFor(i.Status)
	icon := st.Render(statusIcon(i.Status))

	id := idStyle.Render(fmt.Sprintf("%-22s", trunc(i.ID, 22)))
	pri := fmt.Sprintf("P%d", i.Priority)

	row := fmt.Sprintf("%s%s  %s  %s  %s", cursor, icon, id, pri, i.Title)

	if i.IsHuman() {
		row += "  " + humanBadge.Render("HUMAN")
	}
	return row
}

func (m Model) viewDetail() string {
	if len(m.visible) == 0 {
		return ""
	}
	i := m.visible[m.cursor]

	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render(i.Title))
	b.WriteString("\n")

	meta := fmt.Sprintf("%s  %s  %s  P%d",
		idStyle.Render(i.ID),
		statusStyleFor(i.Status).Render(i.Status),
		i.IssueType,
		i.Priority,
	)
	if i.IsHuman() {
		meta += "  " + humanBadge.Render("HUMAN")
	}
	b.WriteString(meta)
	b.WriteString("\n\n")

	if len(i.Labels) > 0 {
		b.WriteString(detailLabelStyle.Render("labels: "))
		b.WriteString(strings.Join(i.Labels, ", "))
		b.WriteString("\n\n")
	}

	b.WriteString(detailLabelStyle.Render("instructions"))
	b.WriteString("\n")
	if strings.TrimSpace(i.Description) == "" {
		b.WriteString(emptyStyle.Render("(no description)"))
	} else {
		b.WriteString(i.Description)
	}

	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("esc / enter: back   q: quit"))
	return b.String()
}

func (m Model) statusBar() string {
	left := fmt.Sprintf("[%s]  %d/%d", m.preset, len(m.visible), len(m.all))
	if m.query != "" {
		left += fmt.Sprintf("  filter:%q", m.query)
	}
	if !m.lastSync.IsZero() {
		left += "  synced " + m.lastSync.Format("15:04:05")
	}
	help := "j/k move  ⏎ open  / filter  h human  tab cycle  r refresh  q quit"
	gap := " "
	if m.width > 0 {
		need := lipgloss.Width(left) + lipgloss.Width(help) + 2
		if need < m.width {
			gap = strings.Repeat(" ", m.width-need)
		}
	}
	return statusBarStyle.Render(left + gap + help)
}

func keyHit(msg tea.KeyMsg, b key.Binding) bool {
	return key.Matches(msg, b)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
