// Package tui is the Bubble Tea interface that drives would-you-kindly.
//
// The model is kept deliberately flat: a single Model struct holds the
// current issues, cursor position, mode (list, detail, filter input),
// and the active preset. Bubble Tea's Update routes key events to
// per-mode handlers that mutate the model and return commands.
package tui

import (
	"context"
	"errors"
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

// refreshInterval is how often the TUI polls bd for changes. A timer
// keeps things simple and avoids a filesystem-watcher dependency;
// .beads/issues.jsonl rewrites are cheap to re-query.
const refreshInterval = 10 * time.Second

// mode tracks the user's interaction context.
type mode int

const (
	modeList   mode = iota // browsing the issue list
	modeDetail             // expanded detail view of one issue
	modeFilter             // / prompt active, typing into textinput
)

// Source abstracts where issues come from so a test can plug in
// fixtures while the binary uses the real bd CLI. Implementations
// must be safe to call from a Bubble Tea command goroutine and
// respect context cancellation so the program can exit cleanly.
type Source interface {
	Fetch(ctx context.Context, preset filter.Preset) ([]beads.Issue, error)
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
	loading  bool // true between a fetch dispatch and its result

	// tickGen identifies the currently-live tick chain. Each suspend
	// or restart bumps it; stale ticks (e.g. one scheduled before a
	// refresh restart) carry an older gen and are dropped, preventing
	// duplicate tick chains after a terminal-error → recovery cycle.
	tickGen int

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

// Init triggers the first fetch and starts the refresh tick.
func (m Model) Init() tea.Cmd {
	// gen 0 is implicit on the zero-valued Model; the matching tick
	// message carries gen 0 too, so the chain starts coherently.
	return tea.Batch(m.fetchCmd(), tickCmd(m.tickGen))
}

// fetchCmd asks the Source for issues matching the current preset.
// It uses a fresh background context per call; the bd Client applies
// its own per-call timeout. The originating preset is echoed back in
// the result so stale fetches (a tick that arrived while the user was
// switching presets) can be dropped instead of overwriting newer data.
func (m Model) fetchCmd() tea.Cmd {
	src, preset := m.src, m.preset
	return func() tea.Msg {
		issues, err := src.Fetch(context.Background(), preset)
		return fetchedMsg{preset: preset, issues: issues, err: err}
	}
}

func tickCmd(gen int) tea.Cmd {
	return tea.Tick(refreshInterval, func(_ time.Time) tea.Msg { return tickMsg{gen: gen} })
}

type fetchedMsg struct {
	preset filter.Preset
	issues []beads.Issue
	err    error
}

type tickMsg struct{ gen int }

// isTerminalErr reports whether an error is one the auto-refresh tick
// should give up on. These don't self-heal mid-session; the user must
// install bd or move into a workspace and hit `r` to recover.
func isTerminalErr(err error) bool {
	return errors.Is(err, beads.ErrBDNotFound) || errors.Is(err, beads.ErrNoWorkspace)
}

// Update is the main event router.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case fetchedMsg:
		// Drop results from a fetch dispatched for a preset we've
		// since moved off of — otherwise an in-flight tick can clobber
		// the user's newly-selected view.
		if msg.preset != m.preset {
			return m, nil
		}
		m.loading = false
		m.lastSync = time.Now()
		m.lastErr = msg.err
		if msg.err == nil {
			m.all = msg.issues
			m.recomputeVisible()
		}
		return m, nil

	case tickMsg:
		// Drop ticks from a chain we've already replaced — this
		// happens when a manual refresh restarts the tick before
		// an earlier one has had a chance to fire and self-suspend.
		if msg.gen != m.tickGen {
			return m, nil
		}
		// Suspend the auto-refresh while we're in a terminal error
		// state (no bd / no workspace). Bump the generation so any
		// later refresh starts a fresh chain that supersedes this one.
		if isTerminalErr(m.lastErr) {
			m.tickGen++
			return m, nil
		}
		return m, tea.Batch(m.fetchCmd(), tickCmd(m.tickGen))

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

	// Forward any other message (e.g. textinput's cursor-blink ticks)
	// to the focused textinput while the filter prompt is open. Without
	// this the cursor stops blinking after the initial Blink command.
	if m.mode == modeFilter {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
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
		return m.switchPreset(filter.PresetHuman)
	case keyHit(msg, m.keys.Cycle):
		return m.switchPreset(filter.NextPreset(m.preset))
	case keyHit(msg, m.keys.Refresh):
		// Manual refresh also restarts the auto-tick if it was
		// suspended after a terminal error. Bumping tickGen retires
		// any older tick that's still in-flight, so the new chain is
		// the only one alive.
		m.loading = true
		cmds := []tea.Cmd{m.fetchCmd()}
		if isTerminalErr(m.lastErr) {
			m.tickGen++
			cmds = append(cmds, tickCmd(m.tickGen))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// switchPreset clears the visible rows before dispatching the new
// fetch so the UI doesn't flash the old preset's data under the new
// header. The loading flag distinguishes the in-flight state from a
// genuinely empty result. Any pending fuzzy filter stays — it re-
// applies once the new data arrives.
func (m Model) switchPreset(p filter.Preset) (tea.Model, tea.Cmd) {
	m.preset = p
	m.cursor = 0
	m.all = nil
	m.visible = nil
	m.loading = true
	return m, m.fetchCmd()
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
	// ctrl+c quits unconditionally; the status bar advertises it and
	// the textinput wouldn't otherwise intercept it.
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
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
		b.WriteString(errorStyle.Render(friendlyError(m.lastErr)))
		b.WriteString("\n\n")
		b.WriteString(emptyStyle.Render("press r to retry, q to quit"))
	case m.loading:
		b.WriteString(emptyStyle.Render("loading…"))
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

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
