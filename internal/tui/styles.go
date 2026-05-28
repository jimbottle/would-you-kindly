package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true)

	idStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("244"))

	statusOpen       = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	statusInProgress = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	statusBlocked    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusClosed     = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Strikethrough(true)
	statusOther      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	humanBadge = lipgloss.NewStyle().
			Background(lipgloss.Color("212")).
			Foreground(lipgloss.Color("232")).
			Bold(true).
			Padding(0, 1)

	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	emptyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Italic(true)

	detailHeaderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("212")).
				Bold(true).
				MarginBottom(1)

	detailLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	// confirmStyle renders the destructive-action confirmation prompt
	// (e.g. "close wyk-42? [y/N]") with enough emphasis that the user
	// notices it before pressing the next key.
	confirmStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	// statusBannerStyle renders transient write feedback ("closed wyk-42",
	// "note failed: …") above the status bar. Subtle but visible.
	statusBannerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("84")).
				Italic(true)
)

func statusStyleFor(status string) lipgloss.Style {
	switch status {
	case "open":
		return statusOpen
	case "in_progress":
		return statusInProgress
	case "blocked":
		return statusBlocked
	case "closed":
		return statusClosed
	default:
		return statusOther
	}
}

func statusIcon(status string) string {
	switch status {
	case "open":
		return "○"
	case "in_progress":
		return "◐"
	case "blocked":
		return "●"
	case "deferred":
		return "◌"
	case "closed":
		return "✓"
	default:
		return "·"
	}
}
