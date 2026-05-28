package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true)

	// idStyle is unstyled — table data cells use the terminal's
	// default foreground so the Repo/Branch/ID/T/P/Updated columns
	// read as bright as the Title. Earlier versions dimmed these
	// columns to push attention toward the title, but the user
	// preference is a flat uniform white table.
	idStyle = lipgloss.NewStyle()

	statusOpen       = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	statusInProgress = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	statusBlocked    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusClosed     = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Strikethrough(true)
	statusOther      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// humanBadge is the fallback rendering when an issue carries the
	// `human` label but no `src:` source label — older issues from
	// before the contract was formalised.
	humanBadge = lipgloss.NewStyle().
			Background(lipgloss.Color("212")).
			Foreground(lipgloss.Color("232")).
			Bold(true).
			Padding(0, 1)

	// humanBadgeAgent renders the "agent handed this back" case —
	// hot pink, the visual signal that something needs your attention.
	// Reuses humanBadge's pink so the variant stays recognisable.
	humanBadgeAgent = lipgloss.NewStyle().
			Background(lipgloss.Color("212")).
			Foreground(lipgloss.Color("232")).
			Bold(true).
			Padding(0, 1)

	// humanBadgeSelf renders the "I filed this for myself" case —
	// muted blue. Different enough at a glance that the eye can sort
	// the two without reading the badge text.
	humanBadgeSelf = lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
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

	// tableHeaderStyle renders the column-header row above the issue
	// list — underlined for visual separation from the data rows,
	// otherwise unstyled (matches the data cells' default foreground).
	tableHeaderStyle = lipgloss.NewStyle().Underline(true)

	// typeStyle, updatedStyle: unstyled, same as idStyle. Kept as
	// named values so the renderRow code stays symmetric and a
	// future change can re-introduce per-column emphasis cheaply.
	typeStyle    = lipgloss.NewStyle()
	updatedStyle = lipgloss.NewStyle()

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

	// setupHintStyle renders the onboarding nag (e.g. "no repos
	// registered, run wyk init -scan ~/Projects"). Bright enough to
	// be noticed on first run; not loud enough to keep dominating
	// the eye on every subsequent paint.
	setupHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Italic(true)

	// wykIndicatorStyle renders the ✓ in the W column for repos that
	// have wyk's post-commit hook installed. Green so it reads as
	// "this is configured correctly" at a glance.
	wykIndicatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
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
