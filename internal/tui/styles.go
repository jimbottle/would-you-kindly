package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbottle/would-you-kindly/internal/theme"
)

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

	// agentBadge renders the "agent's responsibility" case — green
	// to read as "in flight / on it", distinct from the pink/blue
	// HUMAN variants which read as "needs your attention". Surfaces
	// on rows that match the agent inbox query
	// (label=src:agent AND NOT label=human).
	agentBadge = lipgloss.NewStyle().
			Background(lipgloss.Color("84")).
			Foreground(lipgloss.Color("232")).
			Bold(true).
			Padding(0, 1)

	// humanBlockBadge renders the "agent owns it but a human is
	// blocking" case — amber, so it reads between the pink HUMAN
	// (the human's move) and the green AGENT (agent on it). The
	// inbox imperative explicitly doesn't fire on these rows — the
	// agent cannot make progress until a blocker closes — so the
	// colour cue matters.
	humanBlockBadge = lipgloss.NewStyle().
			Background(lipgloss.Color("214")).
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

	// chipActiveStyle renders an active filter chip above the
	// table (e.g. "human" preset, "≤P1" priority cap). Bright
	// background + dark text so it reads as a pill the eye can
	// land on, distinct from the muted header row below.
	chipActiveStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("214")).
			Foreground(lipgloss.Color("232")).
			Bold(true)

	// fetchErrorStyle renders the multi-repo per-sub failure banner.
	// Amber (not red) so an erroring sub reads as "needs attention"
	// without screaming over the rest of the table — distinct from
	// the bright red errorStyle used for whole-fetch fatal errors.
	fetchErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Italic(true)

	// fuzzyMatchStyle highlights individual runes inside a Title
	// cell when a fuzzy-filter match landed there. Bright amber +
	// bold so the matched runes catch the eye against the default
	// foreground — at a glance the user can confirm "yes, this row
	// is here because of `xyz` and the match is in /these/ runes".
	fuzzyMatchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	// closedRowStyle dims a row in the list when its Status is
	// "closed". Mid-grey so the row is still legible (search,
	// yank, detail-view all still work) but doesn't compete with
	// open rows for the eye. The inner per-column styles
	// (statusClosed strikethrough, badges, fuzzy-match highlight)
	// already set their own foregrounds and stay vivid — this
	// envelope only paints the unstyled runs.
	closedRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ApplyTheme overlays a user theme.json onto the built-in styles.
// Empty fields keep the built-in default; non-empty fields are
// applied via lipgloss.Color (which accepts ANSI 256 codes like
// "212" or hex literals like "#ff66cc"). Call once at startup
// before any goroutine renders — these vars are package-level and
// not synchronised.
//
//nolint:gocyclo // straight-line one-field-per-style fan-out; splitting hurts readability
func ApplyTheme(t theme.Theme) {
	if t.Title != "" {
		titleStyle = titleStyle.Foreground(lipgloss.Color(t.Title))
	}
	if t.StatusOpen != "" {
		statusOpen = statusOpen.Foreground(lipgloss.Color(t.StatusOpen))
	}
	if t.StatusInProgress != "" {
		statusInProgress = statusInProgress.Foreground(lipgloss.Color(t.StatusInProgress))
	}
	if t.StatusBlocked != "" {
		statusBlocked = statusBlocked.Foreground(lipgloss.Color(t.StatusBlocked))
	}
	if t.StatusClosed != "" {
		statusClosed = statusClosed.Foreground(lipgloss.Color(t.StatusClosed))
	}
	if t.StatusOther != "" {
		statusOther = statusOther.Foreground(lipgloss.Color(t.StatusOther))
	}
	if t.HumanBadgeBG != "" {
		humanBadge = humanBadge.Background(lipgloss.Color(t.HumanBadgeBG))
	}
	if t.HumanBadgeFG != "" {
		humanBadge = humanBadge.Foreground(lipgloss.Color(t.HumanBadgeFG))
	}
	if t.AgentBadgeBG != "" {
		agentBadge = agentBadge.Background(lipgloss.Color(t.AgentBadgeBG))
	}
	if t.AgentBadgeFG != "" {
		agentBadge = agentBadge.Foreground(lipgloss.Color(t.AgentBadgeFG))
	}
	if t.HumanBlockBG != "" {
		humanBlockBadge = humanBlockBadge.Background(lipgloss.Color(t.HumanBlockBG))
	}
	if t.HumanBlockFG != "" {
		humanBlockBadge = humanBlockBadge.Foreground(lipgloss.Color(t.HumanBlockFG))
	}
	if t.Cursor != "" {
		cursorStyle = cursorStyle.Foreground(lipgloss.Color(t.Cursor))
	}
	if t.StatusBarBG != "" {
		statusBarStyle = statusBarStyle.Background(lipgloss.Color(t.StatusBarBG))
	}
	if t.StatusBarFG != "" {
		statusBarStyle = statusBarStyle.Foreground(lipgloss.Color(t.StatusBarFG))
	}
	if t.Error != "" {
		errorStyle = errorStyle.Foreground(lipgloss.Color(t.Error))
	}
	if t.Empty != "" {
		emptyStyle = emptyStyle.Foreground(lipgloss.Color(t.Empty))
	}
	if t.DetailHeader != "" {
		detailHeaderStyle = detailHeaderStyle.Foreground(lipgloss.Color(t.DetailHeader))
	}
	if t.DetailLabel != "" {
		detailLabelStyle = detailLabelStyle.Foreground(lipgloss.Color(t.DetailLabel))
	}
	if t.Help != "" {
		helpStyle = helpStyle.Foreground(lipgloss.Color(t.Help))
	}
	if t.Confirm != "" {
		confirmStyle = confirmStyle.Foreground(lipgloss.Color(t.Confirm))
	}
	if t.StatusBanner != "" {
		statusBannerStyle = statusBannerStyle.Foreground(lipgloss.Color(t.StatusBanner))
	}
	if t.SetupHint != "" {
		setupHintStyle = setupHintStyle.Foreground(lipgloss.Color(t.SetupHint))
	}
	if t.WykIndicator != "" {
		wykIndicatorStyle = wykIndicatorStyle.Foreground(lipgloss.Color(t.WykIndicator))
	}
	if t.ChipActiveBG != "" {
		chipActiveStyle = chipActiveStyle.Background(lipgloss.Color(t.ChipActiveBG))
	}
	if t.ChipActiveFG != "" {
		chipActiveStyle = chipActiveStyle.Foreground(lipgloss.Color(t.ChipActiveFG))
	}
	if t.FetchError != "" {
		fetchErrorStyle = fetchErrorStyle.Foreground(lipgloss.Color(t.FetchError))
	}
	if t.FuzzyMatch != "" {
		fuzzyMatchStyle = fuzzyMatchStyle.Foreground(lipgloss.Color(t.FuzzyMatch))
	}
	if t.ClosedRow != "" {
		closedRowStyle = closedRowStyle.Foreground(lipgloss.Color(t.ClosedRow))
	}
}

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
