package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// This file holds rendering: the table, badges, status bar, and text measurement.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestDisplayID_ShowsTheFullIDSingleRepo(t *testing.T) {
	// The ID column exists to match a row against an ID someone
	// quoted — and agents quote bd IDs in full. Even in single-repo
	// mode, where every row shares `would-you-kindly-`, the prefix
	// stays (would-you-kindly-rvv9).
	src := &stubSource{issues: []beads.Issue{
		{ID: "would-you-kindly-2oa", Title: "a"},
		{ID: "would-you-kindly-1ej", Title: "b"},
		{ID: "would-you-kindly-ma5", Title: "c"},
	}}
	m := applyFetched(New(src), src)
	if got := m.displayID(m.all[0]); got != "would-you-kindly-2oa" {
		t.Errorf("displayID single-repo: got %q, want the full ID", got)
	}
}

func TestDisplayID_ShowsTheFullIDMultiRepo(t *testing.T) {
	// Multi-repo: the per-row Repo is NOT stripped either — the
	// prefix is the half of the ID that says which workspace, and
	// that's exactly what disambiguates two rows an agent named.
	m := Model{
		all: []beads.Issue{
			{ID: "alpha-1", Repo: "alpha", Title: "a"},
			{ID: "beta-9", Repo: "beta", Title: "b"},
		},
	}
	if got := m.displayID(m.all[0]); got != "alpha-1" {
		t.Errorf("alpha-1 → %q, want the full ID", got)
	}
	if got := m.displayID(m.all[1]); got != "beta-9" {
		t.Errorf("beta-9 → %q, want the full ID", got)
	}
}

func TestColWidths_IDColumnFitsTheLongestFullID(t *testing.T) {
	// The whole point of the change: a long workspace-prefixed ID
	// must render WITHOUT an ellipsis on a normal-width terminal.
	// The old flat cap (colID+4 = 16) truncated these.
	id := "workspace-custom-palette-3f2" // 28 cells
	m := Model{width: 200, all: []beads.Issue{{ID: id, Repo: "workspace-custom-palette"}}}
	m.visible = m.all
	w := m.computeColWidths(m.visible)
	if w.id < len(id) {
		t.Errorf("ID column width %d is too narrow for %q (%d cells)", w.id, id, len(id))
	}
	if got := trunc(id, w.id); got != id {
		t.Errorf("ID rendered as %q, want it untruncated", got)
	}
}

func TestMaxIDWidth_ScalesWithTerminalAndStaysBounded(t *testing.T) {
	cases := []struct {
		name  string
		width int
		want  int
	}{
		// Unknown width (before the first WindowSizeMsg): don't
		// truncate blind — allow the hard max.
		{"unset", 0, idHardMax},
		// Narrow terminal: floor at the old fixed cap so a small
		// window is no worse off than before the change.
		{"narrow", 40, colID + 4},
		// Mid: a third of the width.
		{"mid", 90, 30},
		// Wide: clamped by the hard ceiling, so a pathological ID
		// can't run away with the row.
		{"wide", 400, idHardMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{width: c.width}
			if got := m.maxIDWidth(); got != c.want {
				t.Errorf("maxIDWidth(width=%d) = %d, want %d", c.width, got, c.want)
			}
		})
	}
}

func TestHumanBadge_AlwaysReadsHUMAN(t *testing.T) {
	// All human-labeled issues render the same plain HUMAN badge
	// regardless of src — the column is a yes/no signal, not a
	// three-way categorisation.
	cases := []beads.Issue{
		{Labels: []string{"human", "src:agent"}},
		{Labels: []string{"human", "src:human"}},
		{Labels: []string{"human"}},
	}
	for _, i := range cases {
		got := responsibilityBadgeFor(i)
		if !strings.Contains(got, "HUMAN") {
			t.Errorf("badge should read HUMAN for %v; got %q", i.Labels, got)
		}
		if strings.Contains(got, "←") || strings.Contains(got, "·") {
			t.Errorf("badge should be plain (no arrow/dot) for %v; got %q", i.Labels, got)
		}
	}
}

func TestTrunc_RuneAware(t *testing.T) {
	// Width semantics in the TUI are visual, not byte: a column
	// width of N should hold N characters regardless of whether
	// each is one byte or four. Pre-fix trunc sliced with s[:n-1]
	// which could split a multi-byte rune mid-codepoint and emit
	// invalid UTF-8 before the ellipsis. Pin the contract on a few
	// concrete inputs so a future "performance" refactor back to
	// byte semantics fails here loudly.
	// IMPORTANT: trunc measures DISPLAY CELLS via dispWidth (ambWide), and
	// the ellipsis "…" is itself ambiguous-width = 2 cells under ambWide.
	// So a cut result is (content cells) + 2, and trunc reserves 2 for the
	// ellipsis. Every expectation below therefore satisfies
	// dispWidth(want) <= n (asserted in the loop).
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short-ascii-untouched", "abc", 5, "abc"},
		// budget 5, ellipsis costs 2 → 3 cells of content: "abc…".
		{"long-ascii-truncated", "abcdefgh", 5, "abc…"},
		{"zero-width-empty", "anything", 0, ""},
		// n=1 < ellipsis width(2): no room for "…", show the leading cell.
		{"one-width-single-rune", "abc", 1, "a"},
		// café: c/a/f are 1 cell, é is ambiguous = 2. Budget 3, minus the
		// 2-cell ellipsis leaves 1 cell → "c…".
		{"multibyte-stays-valid", "café", 3, "c…"},
		// All Greek (ambiguous, 2 cells each). Budget 3 minus ellipsis(2)
		// leaves 1 cell — no whole Greek rune fits → just "…".
		{"all-multibyte", "αβγδ", 3, "…"},
		// CJK = 2 cells each. Budget 5 minus ellipsis(2) = 3 cells → one
		// 世 (2) fits, a second would be 4>3 → "世…".
		{"cjk-fits-cell-budget", "世界世界世", 5, "世…"},
		// Exactly fits: 3 CJK = 6 cells, budget 6 → untouched.
		{"cjk-exact-fit-untouched", "世界世", 6, "世界世"},
		// Budget 4 minus ellipsis(2) = 2 → one 世 → "世…".
		{"cjk-odd-budget", "世界世", 4, "世…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trunc(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("trunc(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			// The load-bearing invariant: the result never exceeds the
			// cell budget (the bug qabo's review caught).
			if w := dispWidth(got); w > tc.n {
				t.Errorf("trunc(%q, %d) = %q is %d cells, exceeds budget %d", tc.in, tc.n, got, w, tc.n)
			}
		})
	}
}

func TestTitleTruncation_NarrowTerminalEllipsizesTitle(t *testing.T) {
	// On a narrow pane the title used to spill past the right
	// edge. With titleBudget capping the column, long titles get
	// the ellipsis treatment; details still live behind enter.
	longTitle := "Pivot to eBay OAuth + Trading API (Chrome Custom Tabs for auth) — replaces WebView-only sign-in"
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: longTitle, Status: "open", Labels: []string{}},
	}}
	m := New(src)
	// Narrow pane: 80 columns. Multi-repo chrome eats ~80; the
	// budget floor (20) kicks in, so the title is heavily clipped.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if strings.Contains(out, longTitle) {
		t.Errorf("expected long title to be truncated on a 80-col pane; got full title:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis after a clipped title; got:\n%s", out)
	}
}

func TestTitleTruncation_WideTerminalShowsFullTitle(t *testing.T) {
	// Sanity check: with plenty of room the title is rendered
	// verbatim. titleBudget should NOT collapse content that fits.
	title := "Decide uninstall feedback form provider"
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: title, Status: "open", Labels: []string{}},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 300, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	out := m.View()
	if !strings.Contains(out, title) {
		t.Errorf("wide pane should show the full title; got:\n%s", out)
	}
}

func TestTitleBudget_WideTerminalFillsAvailableWidth(t *testing.T) {
	// Title is a flex column: on a wide terminal it consumes all the
	// width left after the fixed columns, rather than capping at a fixed
	// ceiling and leaving dead space to the right (user request — fill
	// the gap). A long title therefore shows far more than the old
	// 50-col cap would have allowed.
	const width = 400
	src := &stubSource{issues: []beads.Issue{
		{ID: "a-1", Title: "short", Status: "open", Labels: []string{}},
	}}
	m := New(src)
	model, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	m = model.(Model)
	m = applyFetched(m, src)
	avail := m.titleBudget()
	// The budget is whatever's left of the terminal after the fixed
	// columns — comfortably more than the old 50-col cap on a 400-col
	// pane, and never wider than the terminal itself.
	if avail <= 50 {
		t.Errorf("titleBudget on a %d-col terminal = %d, want it to flex well past the old 50-col cap", width, avail)
	}
	if avail >= width {
		t.Errorf("titleBudget = %d should be less than terminal width %d (fixed columns consume some)", avail, width)
	}
	// A title comfortably past the old 50-col cap but within the flexed
	// budget renders in full (no ellipsis) — proving the cap is gone.
	long := strings.Repeat("x", 80)
	if 80 >= avail {
		t.Fatalf("test precondition: 80-char title must fit the %d-col budget", avail)
	}
	src.issues = []beads.Issue{{ID: "a-1", Title: long, Status: "open", Labels: []string{}}}
	m = applyFetched(m, src)
	if out := m.View(); !strings.Contains(out, long) {
		t.Errorf("an 80-char title (past the old 50-col cap) should render in full; got:\n%s", out)
	}
}

func TestStatusBar_WrapsWithinWidthAndKeepsAllKeys(t *testing.T) {
	// The footer wraps the key bindings into a column-aligned grid below
	// the info line so EVERY binding stays visible (no truncation), and
	// no single line exceeds the terminal width — measured BOTH by
	// lipgloss AND by the ambiguous-wide width (the ▕/·/± glyphs render as
	// 2 cells on "ambiguous = wide" terminals, which is what made the old
	// one-line footer run off the edge). Floor is 60: below that the info
	// line itself is wider than the pane.
	src := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(src), src)
	// Derive the exact "key desc" cells the footer should render (incl.
	// "H ±human") from the same binding set, so a genuinely dropped
	// binding can't be masked by an incidental substring match.
	var wantCells []string
	for _, bnd := range m.footerBindings() {
		if !bnd.Enabled() {
			continue
		}
		h := bnd.Help()
		wantCells = append(wantCells, h.Key+" "+h.Desc)
	}
	for _, w := range []int{60, 70, 80, 100, 120, 133, 145, 160, 200} {
		m.width = w
		out := stripANSI(m.statusBar())
		for _, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width=%d: footer line lipgloss-width %d > %d: %q", w, got, w, line)
			}
			if got := dispWidth(line); got > w {
				t.Errorf("width=%d: footer line ambiguous-wide %d > %d: %q", w, got, w, line)
			}
		}
		for _, cell := range wantCells {
			if !strings.Contains(out, cell) {
				t.Errorf("width=%d: footer dropped binding %q (should wrap, not truncate):\n%s", w, cell, out)
			}
		}
	}
}

func TestResponsibilityBadge_AgentTaskNotHumanFlagged(t *testing.T) {
	// New AGENT branch: src:agent + NOT human → an AGENT badge.
	// This is the inbox row case — the agent's responsibility is
	// to act on these rather than note them.
	agentInbox := beads.Issue{Labels: []string{"src:agent"}}
	got := responsibilityBadgeFor(agentInbox)
	if got == "" {
		t.Fatalf("src:agent + NOT human should produce a badge; got empty")
	}
	if !strings.Contains(got, "AGENT") {
		t.Errorf("expected AGENT badge for inbox row; got %q", got)
	}
	if strings.Contains(got, "HUMAN") {
		t.Errorf("AGENT badge should not contain HUMAN; got %q", got)
	}
}

func TestResponsibilityBadge_HumanLabelTrumpsAgentSource(t *testing.T) {
	// An issue carrying both `human` and `src:agent` is in the
	// human's lap; the badge must read HUMAN (the agent's
	// hand-back arrow variant), NOT AGENT, even though src:agent
	// is also set.
	bounced := beads.Issue{Labels: []string{"human", "src:agent"}}
	got := responsibilityBadgeFor(bounced)
	if !strings.Contains(got, "HUMAN") {
		t.Errorf("human label should produce a HUMAN badge regardless of source; got %q", got)
	}
	if strings.Contains(got, "AGENT") {
		t.Errorf("AGENT must not appear when human label is set; got %q", got)
	}
}

func TestResponsibilityBadge_HumanFiledIsAgentOwned(t *testing.T) {
	// A human FILING a task (src:human, no `human` label) is agent-owned
	// work — the agent does it — so it badges AGENT, not blank.
	humanFiled := beads.Issue{Labels: []string{"src:human"}}
	got := responsibilityBadgeFor(humanFiled)
	if !strings.Contains(got, "AGENT") {
		t.Errorf("src:human without human label should badge AGENT; got %q", got)
	}
	if strings.Contains(got, "HUMAN") {
		t.Errorf("an AGENT badge must not read HUMAN; got %q", got)
	}
}

func TestResponsibilityBadge_NullOwnerDefaultsToAgent(t *testing.T) {
	// A null owner (no labels at all, or only unrelated labels) DEFAULTS to
	// AGENT — the owner badge is never blank.
	for _, labels := range [][]string{nil, {"priority:hi"}} {
		got := responsibilityBadgeFor(beads.Issue{Labels: labels})
		if !strings.Contains(got, "AGENT") {
			t.Errorf("null owner %v should default to AGENT; got %q", labels, got)
		}
		if strings.Contains(got, "HUMAN") {
			t.Errorf("null owner %v must not read HUMAN; got %q", labels, got)
		}
	}
}

func TestResponsibilityBadge_HumanBlockForBlockedAgentTask(t *testing.T) {
	// src:agent + NOT human + BlockedByHuman → HUMAN-BLOCK badge.
	// The flag is set post-Fetch by markBlockedByHuman; this test
	// pins the badge-rendering side of the contract.
	blocked := beads.Issue{
		Labels:          []string{"src:agent"},
		BlockedByHuman:  true,
		DependencyCount: 1,
	}
	got := responsibilityBadgeFor(blocked)
	if !strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("BlockedByHuman should produce HUMAN-BLOCK badge; got %q", got)
	}
	// Must NOT also produce the plain AGENT label — those are
	// mutually exclusive states for the column.
	if strings.Contains(got, "AGENT") {
		t.Errorf("HUMAN-BLOCK badge should not also say AGENT; got %q", got)
	}
}

func TestResponsibilityBadge_AgentHandoff(t *testing.T) {
	// agent-handoff label → AGENT-HANDOFF badge: another agent owns it,
	// a human orchestrates, this agent leaves it alone.
	handoff := beads.Issue{Labels: []string{"src:agent", "agent-handoff"}}
	if got := responsibilityBadgeFor(handoff); !strings.Contains(got, "AGENT-HANDOFF") {
		t.Errorf("agent-handoff label should produce AGENT-HANDOFF badge; got %q", got)
	}

	// The explicit flag outranks a computed HUMAN-BLOCK.
	both := beads.Issue{Labels: []string{"src:agent", "agent-handoff"}, BlockedByHuman: true}
	if got := responsibilityBadgeFor(both); !strings.Contains(got, "AGENT-HANDOFF") || strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("agent-handoff should outrank HUMAN-BLOCK; got %q", got)
	}

	// But the `human` label still trumps everything.
	human := beads.Issue{Labels: []string{"src:agent", "agent-handoff", "human"}}
	if got := responsibilityBadgeFor(human); !strings.Contains(got, "HUMAN") || strings.Contains(got, "AGENT-HANDOFF") {
		t.Errorf("human label should trump agent-handoff; got %q", got)
	}
}

func TestResponsibilityBadge_HumanBlockOnlyWhenFlagSet(t *testing.T) {
	// An agent task with deps but no BlockedByHuman flag set
	// stays plain AGENT. The flag is set explicitly by the
	// dep-lookup pass; without it (lookup failed, blocker not
	// in current fetch, etc.) we don't speculate.
	deps := beads.Issue{
		Labels:          []string{"src:agent"},
		DependencyCount: 1,
		BlockedByHuman:  false,
	}
	got := responsibilityBadgeFor(deps)
	if !strings.Contains(got, "AGENT") {
		t.Errorf("agent task with deps but flag unset stays AGENT; got %q", got)
	}
	if strings.Contains(got, "HUMAN-BLOCK") {
		t.Errorf("HUMAN-BLOCK must require the explicit flag; got %q", got)
	}
}

func TestFlashAutoClear_ScheduledByWriteSuccess(t *testing.T) {
	// handleWriteResult should set m.status AND return a
	// flashClearCmd. Drain the batch; the inner clear cmd should
	// produce a flashClearMsg tagged with the active statusGen.
	// (The actual clear happens when Update receives that msg —
	// we exercise that separately below.)
	restoreFlash := withFlashClearDelay(t, time.Millisecond)
	defer restoreFlash()

	s := &stubMutator{stubSource: stubSource{issues: sampleIssues()}}
	m := applyMutatorFetched(New(s), s)
	preGen := m.statusGen

	model, cmd := m.Update(writeMsg{action: "close", id: "wyk-42"})
	m = model.(Model)
	if m.statusGen <= preGen {
		t.Errorf("setStatus should bump statusGen; was %d, now %d", preGen, m.statusGen)
	}

	// Drain the batched cmds; one of the inner clears should be a
	// flashClearMsg with our gen.
	saw := false
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if bm, ok := msg.(tea.BatchMsg); ok {
				for _, inner := range bm {
					if inner == nil {
						continue
					}
					if fc, ok := inner().(flashClearMsg); ok && fc.gen == m.statusGen {
						saw = true
					}
				}
			}
		}
	}
	if !saw {
		t.Errorf("expected a flashClearMsg tagged with statusGen=%d among batched cmds", m.statusGen)
	}
}

func TestFlashAutoClear_StaleClearDoesNotWipeNewStatus(t *testing.T) {
	// A clear from gen=1 must not wipe a status whose gen is now
	// 2 (the user did another action before the timer fired).
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	m.setStatus("first")
	firstGen := m.statusGen
	m.setStatus("second")
	if m.statusGen == firstGen {
		t.Fatal("setStatus should bump statusGen on every call")
	}

	// Stale clear from gen=firstGen arrives — must be ignored.
	model, _ := m.Update(flashClearMsg{gen: firstGen})
	m = model.(Model)
	if m.status != "second" {
		t.Errorf("stale clear wiped the active status; want %q, got %q", "second", m.status)
	}

	// Current clear (gen=current) DOES wipe.
	model, _ = m.Update(flashClearMsg{gen: m.statusGen})
	m = model.(Model)
	if m.status != "" {
		t.Errorf("current-gen clear should wipe; got %q", m.status)
	}
}

func TestHighlightRunes_StylesMatchedRunesOnly(t *testing.T) {
	// lipgloss strips escapes in non-TTY environments (go test),
	// so we can't grep for SGR codes directly. Instead, render
	// each expected fragment with the same style and assert the
	// output contains those exact rendered bytes — same logic
	// the function uses, so the test pins the per-rune
	// segmentation regardless of color profile.
	got := highlightRunesWithRest("hello", []int{0, 4}, fuzzyMatchStyle, nil)
	wantH := fuzzyMatchStyle.Render("h")
	wantO := fuzzyMatchStyle.Render("o")
	if !strings.Contains(got, wantH) {
		t.Errorf("expected styled 'h' in output; got %q", got)
	}
	if !strings.Contains(got, wantO) {
		t.Errorf("expected styled 'o' in output; got %q", got)
	}
	// The plain middle 'ell' should pass through verbatim.
	if !strings.Contains(got, "ell") {
		t.Errorf("unmatched runes should pass through verbatim; got %q", got)
	}
}

func TestRenderMatchCell_HighlightsAndPreservesValue(t *testing.T) {
	// Force a real color profile so fuzzyMatchStyle emits escape codes —
	// lipgloss strips them in the non-TTY test env, which would let a
	// regression that dropped highlighting pass silently. Restore after.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	base := lipgloss.NewStyle()

	// A matching query wraps each matched rune in fuzzyMatchStyle
	// (highlightRunesWithRest styles runes individually). The 'r' in
	// "droid" is matched and unambiguous.
	cell := renderMatchCell("android", 12, "droid", base)
	if want := fuzzyMatchStyle.Render("r"); !strings.Contains(cell, want) {
		t.Errorf("matching query should highlight the matched runes; got %q", cell)
	}
	// Empty and non-matching queries emit NO styling (plain == raw).
	for _, q := range []string{"", "xyz"} {
		if c := renderMatchCell("android", 12, q, base); c != stripANSI(c) {
			t.Errorf("query %q should not add styling; got %q", q, c)
		}
	}
	// The visible text is preserved and padded/truncated to the width.
	for _, q := range []string{"", "droid", "xyz"} {
		plain := stripANSI(renderMatchCell("android", 12, q, base))
		if lipgloss.Width(plain) != 12 || !strings.HasPrefix(plain, "android") {
			t.Errorf("query %q: cell = %q (w=%d), want value + width 12", q, plain, lipgloss.Width(plain))
		}
	}
	// A value longer than the column truncates with an ellipsis.
	long := stripANSI(renderMatchCell("ebay-watchlist-watch", 10, "watch", base))
	if lipgloss.Width(long) != 10 || !strings.Contains(long, "…") {
		t.Errorf("long value should truncate to width 10 with ellipsis; got %q (w=%d)", long, lipgloss.Width(long))
	}
}

func TestHighlightRunes_OutOfRangeIndicesDropped(t *testing.T) {
	// Match indices past the end of s (e.g., truncated title) are
	// silently skipped — no panic, no trailing ANSI noise.
	got := highlightRunesWithRest("hi", []int{0, 5}, fuzzyMatchStyle, nil)
	if !strings.Contains(got, "i") {
		t.Errorf("expected the unmatched tail to render; got %q", got)
	}
}

func TestRenderStatsLine_CountsHumanAndMine(t *testing.T) {
	// The mine count tallies Assignee — the field the mine preset
	// queries (assignee=) — NOT Owner (who filed). a-4 is filed by ev
	// but assigned elsewhere, so it must not count.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"human"}, Assignee: "ev"},
		{ID: "a-2", Labels: []string{"human", "src:agent"}, Assignee: "ev"},
		{ID: "a-3", Labels: []string{"src:agent"}, Assignee: "other"},
		{ID: "a-4", Owner: "ev", Assignee: "other"},
		{ID: "a-5", Assignee: "ev"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src).WithMe("ev"), src)

	got := m.renderStatsLine()
	// 2 human (a-1, a-2), 3 mine (a-1, a-2, a-5 assigned to ev).
	if !strings.Contains(got, "2 human") {
		t.Errorf("expected '2 human' in stats; got %q", got)
	}
	if !strings.Contains(got, "3 mine") {
		t.Errorf("expected '3 mine' in stats; got %q", got)
	}
}

func TestRenderStatsLine_EmptyWhenNoSignals(t *testing.T) {
	// No identity AND no human-labeled issues → no stats line at
	// all. A bare "· 0 human · 0 mine" suffix would be visual
	// chrome the user can't act on; the empty string keeps the
	// status bar clean for read-only or unconfigured runs.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, Owner: "other"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src), src) // me unset

	if got := m.renderStatsLine(); got != "" {
		t.Errorf("expected empty stats line; got %q", got)
	}
}

func TestRenderStatsLine_MineSlotShowsZeroWhenMeSet(t *testing.T) {
	// With an identity wired up but zero owned rows, we still
	// render "0 mine" so the user can tell their identity made it
	// through. Silent omission would look like a config bug.
	issues := []beads.Issue{
		{ID: "a-1", Labels: []string{"src:agent"}, Owner: "other"},
	}
	src := &stubSource{issues: issues}
	m := applyFetched(New(src).WithMe("ev"), src)

	got := m.renderStatsLine()
	if !strings.Contains(got, "0 mine") {
		t.Errorf("expected '0 mine' when me set + zero owned; got %q", got)
	}
}

func TestRenderHeader_DecoratesActiveSortColumn(t *testing.T) {
	// Sort by priority should put ↑ next to Priority; sort by updated
	// should put ↓ next to Updated. sortNone leaves the header
	// arrow-free.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	if got := m.renderHeader(); strings.ContainsAny(got, "↑↓") {
		t.Errorf("sortNone should not decorate any column; got:\n%s", got)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if got := m.renderHeader(); !strings.Contains(got, "Priority↑") {
		t.Errorf("sortPriority should decorate the Priority column with ↑; got:\n%s", got)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = model.(Model)
	if got := m.renderHeader(); !strings.Contains(got, "Updated↓") {
		t.Errorf("sortUpdated should decorate the Updated column with ↓; got:\n%s", got)
	}
}

func TestRenderHeader_DecoratedColumnsStayWithinTheirWidth(t *testing.T) {
	// Regression for the 1259 LOW: "Updated↓" used to overflow
	// colUpdated=7 and push Title one column right of the data
	// rows. Pin the invariant: under every sort state, every
	// decorated column header renders at exactly its configured
	// width — never more.
	src := &stubSource{issues: []beads.Issue{
		{ID: "alpha-1", Repo: "alpha", Title: "x"},
		{ID: "beta-9", Repo: "beta", Title: "y"},
	}}
	m := applyFetched(New(src), src)
	// Seed the per-paint column widths the way viewList does, so the
	// header renders against the same content-sized widths.
	m.cw = m.computeColWidths(m.visible)

	cases := []struct {
		label string
		sort  sortKey
	}{
		{"none", sortNone},
		{"priority", sortPriority},
		{"updated", sortUpdated}, // the regression case
		{"repo", sortRepo},
		{"id", sortID},
	}
	// Anchor against an absolute expectation derived from the computed
	// column widths. Self-referential baselines (using sortNone as the
	// source of truth for the other cases) would silently mask a layout
	// bug that shifted the baseline itself — every case here, including
	// sortNone, is validated against the same expected rune-column.
	const sep = 2 // two-space separator after each column
	expectedTitleRune := 2 /* leading cursor */ +
		m.cw.owner + sep +
		m.cw.repo + sep +
		m.cw.branch + sep +
		m.cw.id + sep +
		m.cw.typ + sep +
		m.cw.status + sep +
		m.cw.prio + sep +
		m.cw.updated + sep +
		m.cw.session + sep

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			m.sortBy = c.sort
			out := stripANSI(m.renderHeader())
			byteAt := strings.Index(out, "Title")
			if byteAt < 0 {
				t.Fatalf("Title not found in header:\n%s", out)
			}
			// strings.Index returns a byte offset, but multi-byte
			// arrows (↑↓) would inflate it relative to visual
			// columns. Measure rune-count to get the true column
			// position.
			runesAt := utf8.RuneCountInString(out[:byteAt])
			if runesAt != expectedTitleRune {
				t.Errorf("sort=%s: Title at rune-col %d, want %d (header overflow!)",
					c.label, runesAt, expectedTitleRune)
			}
		})
	}
}

func TestStatusBar_FailedFetchAfterWarmStartStaysCached(t *testing.T) {
	// MED regression (job 1457): m.lastSync is set unconditionally
	// on every fetchedMsg — including failures. After a warm-start
	// (cached rows on screen, cacheStale=true), if the FIRST live
	// fetch fails, lastSync is now non-zero but the rows are still
	// the stale on-disk snapshot. The status bar must keep showing
	// "cached <age>" — not "synced HH:MM:SS", which would lie
	// about the data being current.
	src := &stubSource{}
	seed := Cache{
		Preset:  string(filter.PresetAll),
		SavedAt: time.Now().Add(-90 * time.Minute),
		Issues:  []beads.Issue{{ID: "stale-row"}},
	}
	m := New(src).WithCacheSnapshot(seed, "")
	if !m.cacheStale {
		t.Fatal("setup: cache seed didn't take")
	}

	// Live fetch fails.
	model, _ := m.Update(fetchedMsg{
		preset: filter.PresetAll,
		err:    errors.New("bd list --json: timed out after 10s"),
	})
	m = model.(Model)

	if !m.cacheStale {
		t.Fatal("cacheStale should remain true after a FAILED fetch — rows on screen are still the seed")
	}
	if m.lastSync.IsZero() {
		t.Fatal("setup: lastSync should be set on every fetchedMsg (including failures) — that's the bug condition")
	}

	bar := m.statusBar()
	if !strings.Contains(bar, "cached ") {
		t.Errorf("status bar should show 'cached <age>' while warm-start rows are still on screen; got %q", bar)
	}
	if strings.Contains(bar, "synced ") {
		t.Errorf("status bar must NOT show 'synced' over stale cached rows — that's the lie this guard exists to prevent; got %q", bar)
	}
}

func TestRenderDetailBody_HighlightsSelectedLink(t *testing.T) {
	m := detailWithLinks(t)
	m.detailLinkIdx = 0
	out := m.renderDetailBody(m.detailIssue)
	if !strings.Contains(out, "▶") {
		t.Errorf("the selected link should carry the ▶ cursor marker; got:\n%s", out)
	}
	// Unselected render has no marker.
	m.detailLinkIdx = -1
	if strings.Contains(m.renderDetailBody(m.detailIssue), "▶") {
		t.Errorf("no marker should render when nothing is selected")
	}
}

// TestIDCell_StaysUniqueOnANarrowTerminal is the regression guard for
// roborev #4028: full IDs share a long workspace prefix, so right-
// eliding the cell rendered EVERY row in a workspace as the same
// string. truncID elides the middle and keeps the suffix whole.
func TestIDCell_StaysUniqueOnANarrowTerminal(t *testing.T) {
	src := &stubSource{issues: []beads.Issue{
		{ID: "would-you-kindly-2oa", Title: "a", Status: "open"},
		{ID: "would-you-kindly-1ej", Title: "b", Status: "open"},
	}}
	m := applyFetched(New(src), src)
	// 50 columns: far too narrow for a 20-cell ID plus the rest.
	m.width = 50
	m.cw = m.computeColWidths(m.visible)
	m.autoHidden = m.computeAutoHidden()

	first := truncID(m.displayID(m.all[0]), m.cw.id)
	second := truncID(m.displayID(m.all[1]), m.cw.id)
	if first == second {
		t.Fatalf("both rows render the same ID cell %q — the suffix was truncated away", first)
	}
	for i, got := range []string{first, second} {
		want := []string{"2oa", "1ej"}[i]
		if !strings.HasSuffix(got, want) {
			t.Errorf("ID cell %q should end in the discriminating suffix %q", got, want)
		}
	}
	// And the cell still respects its budget.
	if w := lipgloss.Width(first); w > m.cw.id {
		t.Errorf("ID cell %q is %d cells, over the %d-cell column", first, w, m.cw.id)
	}
}

func TestTruncID(t *testing.T) {
	// NB: "…" measures TWO cells under ambWide (East-Asian ambiguous),
	// which is why the head budgets below are one narrower than a
	// naive count suggests. truncID measures with dispWidth rather
	// than assuming, so these stay exact.
	cases := []struct {
		name  string
		id    string
		width int
		want  string
	}{
		{"fits untouched", "would-you-kindly-2oa", 20, "would-you-kindly-2oa"},
		{"middle-elided keeps the suffix", "would-you-kindly-2oa", 16, "would-you-k…2oa"},
		{"long workspace still ends in its suffix", "louisville-open-data-expenditure-bot-4jm", 20, "louisville-open…4jm"},
		// No room for any head: keep the trailing run, which still
		// differs row to row.
		{"suffix-only budget", "would-you-kindly-2oa", 4, "…oa"},
		{"no separator falls back to the tail", "abcdefghij", 5, "…hij"},
		{"no room for an ellipsis", "would-you-kindly-2oa", 1, "w"},
		{"zero width", "would-you-kindly-2oa", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncID(c.id, c.width)
			if got != c.want {
				t.Errorf("truncID(%q, %d) = %q, want %q", c.id, c.width, got, c.want)
			}
			if w := lipgloss.Width(got); w > c.width {
				t.Errorf("truncID(%q, %d) = %q is %d cells — over budget", c.id, c.width, got, w)
			}
		})
	}
}
