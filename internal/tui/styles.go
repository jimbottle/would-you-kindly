package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/raylytics/would-you-kindly/internal/theme"
)

var (
	// Semantic color tokens. Each is background-adaptive: the Dark
	// variant is the original value (tuned for dark terminals, where
	// most TUI users live); the Light variant is a deeper shade that
	// holds ≥4.5:1 contrast on a light terminal, so dimmed and
	// saturated text doesn't wash out on a white background. Lipgloss
	// resolves the variant from the terminal's detected background.
	// Self-contained elements (badges, chips, status bar) set both
	// fg+bg and stay legible on any background.
	cAccent    = lipgloss.AdaptiveColor{Light: "162", Dark: "212"} // pink — title, cursor, headers
	cInfo      = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}   // blue — open status
	cWarn      = lipgloss.AdaptiveColor{Light: "130", Dark: "214"} // amber — wip, prompts, hints, soft errors
	cDanger    = lipgloss.AdaptiveColor{Light: "160", Dark: "203"} // red — blocked, fatal error
	cSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "84"}   // green — write-success banner
	cDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "244"} // secondary-but-readable grey
	cFaint     = lipgloss.AdaptiveColor{Light: "248", Dark: "240"} // de-emphasised (closed rows): dim = toward the bg
	cHighlight = lipgloss.AdaptiveColor{Light: "91", Dark: "135"}  // violet — fuzzy-match runes, off the overloaded amber

	titleStyle = lipgloss.NewStyle().
			Foreground(cAccent).
			Bold(true)

	// idStyle is unstyled — table data cells use the terminal's
	// default foreground so the Repo/Branch/ID/T/P/Updated columns
	// read as bright as the Title. Earlier versions dimmed these
	// columns to push attention toward the title, but the user
	// preference is a flat uniform white table.
	idStyle = lipgloss.NewStyle()

	statusOpen       = lipgloss.NewStyle().Foreground(cInfo)
	statusInProgress = lipgloss.NewStyle().Foreground(cWarn)
	statusBlocked    = lipgloss.NewStyle().Foreground(cDanger)
	statusClosed     = lipgloss.NewStyle().Foreground(cDim).Strikethrough(true)
	statusOther      = lipgloss.NewStyle().Foreground(cDim)

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
			Foreground(cAccent).
			Bold(true)

	// statusBarStyle: a filled bar with its own fg+bg, so it reads on
	// any terminal — but flip light/dark so a light terminal gets a
	// light-grey bar with dark text instead of a heavy dark slab.
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "252", Dark: "236"}).
			Foreground(lipgloss.AdaptiveColor{Light: "238", Dark: "252"}).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(cDanger).
			Bold(true)

	emptyStyle = lipgloss.NewStyle().
			Foreground(cDim).
			Italic(true)

	detailHeaderStyle = lipgloss.NewStyle().
				Foreground(cAccent).
				Bold(true).
				MarginBottom(1)

	detailLabelStyle = lipgloss.NewStyle().
				Foreground(cDim)

	helpStyle = lipgloss.NewStyle().
			Foreground(cDim)

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
			Foreground(cWarn).
			Bold(true)

	// statusBannerStyle renders transient write feedback ("closed wyk-42",
	// "note failed: …") above the status bar. Subtle but visible.
	statusBannerStyle = lipgloss.NewStyle().
				Foreground(cSuccess).
				Italic(true)

	// setupHintStyle renders the onboarding nag (e.g. "no repos
	// registered, run wyk init -scan ~/Projects"). Bright enough to
	// be noticed on first run; not loud enough to keep dominating
	// the eye on every subsequent paint.
	setupHintStyle = lipgloss.NewStyle().
			Foreground(cWarn).
			Italic(true)

	// chipActiveStyle renders an active filter chip above the table
	// (e.g. "human" preset, "≤P1" priority cap). A steel-blue pill the
	// eye can land on — deliberately NOT amber, so an active filter
	// doesn't read like the amber wip/blocked/attention statuses.
	chipActiveStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("67")).
			Foreground(lipgloss.Color("231")).
			Bold(true)

	// fetchErrorStyle renders the multi-repo per-sub failure banner.
	// Amber (not red) so an erroring sub reads as "needs attention"
	// without screaming over the rest of the table — distinct from
	// the bright red errorStyle used for whole-fetch fatal errors.
	fetchErrorStyle = lipgloss.NewStyle().
			Foreground(cWarn).
			Italic(true)

	// fuzzyMatchStyle highlights individual runes inside a Title cell
	// when a fuzzy-filter match landed there. Violet + bold so the
	// matched runes catch the eye against the default foreground and
	// read as a search hit, not as an amber status — at a glance the
	// user confirms "this row is here because of `xyz`, matched here".
	fuzzyMatchStyle = lipgloss.NewStyle().
			Foreground(cHighlight).
			Bold(true)

	// closedRowStyle dims a row in the list when its Status is
	// "closed". Mid-grey so the row is still legible (search,
	// yank, detail-view all still work) but doesn't compete with
	// open rows for the eye. The inner per-column styles
	// (statusClosed strikethrough, badges, fuzzy-match highlight)
	// already set their own foregrounds and stay vivid — this
	// envelope only paints the unstyled runs.
	closedRowStyle = lipgloss.NewStyle().Foreground(cFaint)
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

// priorityStyleFor returns the P-column emphasis for a priority when the
// opt-in priority_emphasis setting is on. P0 is loud (danger, bold), P1
// amber, P3/P4 dim, and P2 (plus anything out of range) neutral — so
// enabling it makes urgent rows pop and the backlog recede while leaving
// the mid case as the flat default. Colours reuse the adaptive semantic
// tokens, so they track the terminal background too.
func priorityStyleFor(p int) lipgloss.Style {
	switch p {
	case 0:
		return lipgloss.NewStyle().Foreground(cDanger).Bold(true)
	case 1:
		return lipgloss.NewStyle().Foreground(cWarn)
	case 3, 4:
		return lipgloss.NewStyle().Foreground(cDim)
	default:
		return lipgloss.NewStyle()
	}
}
