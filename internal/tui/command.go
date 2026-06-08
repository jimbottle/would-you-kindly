package tui

// This file holds the `:` command-palette: prompt entry, dispatch
// (:assign / :priority / :label / :filter / :bd), the raw-bd escape
// hatch and its output pager, and the filter-alias command. Extracted
// from model.go to isolate the palette's parsing and dispatch.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/filters"
)

// beginCommand opens the `:` command-palette prompt. Empty
// submission cancels; otherwise updateCommand dispatches through
// commandTable.
func (m Model) beginCommand() (tea.Model, tea.Cmd) {
	m.mode = modeCommand
	m.input.SetValue("")
	m.input.Prompt = ":"
	m.input.Placeholder = "refresh / preset <name> / sort <axis> / reverse / filter save <name>"
	m.input.Focus()
	return m, textinput.Blink
}

// updateCommand drives the `:` prompt. esc cancels; enter parses
// the value into a command + args and dispatches through
// commandTable. Unknown commands surface a status banner that
// names the known set so the user can recover.
func (m Model) updateCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.input.Value())
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if raw == "" {
			return m, nil
		}
		return m.dispatchCommand(raw)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// dispatchCommand splits the input into name + remaining args
// and routes to the matching handler. Unknown commands set a
// status banner with the supported list. The list is small
// enough that a flat switch is more readable than a registry
// pattern.
func (m Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	name, rest, _ := strings.Cut(raw, " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "refresh":
		return m.manualRefresh()
	case "preset":
		p := filter.Preset(rest)
		// Reject unknown presets early — silently switching to a
		// no-op preset would just confuse the user about why the
		// list didn't change.
		known := false
		for _, q := range filter.AllPresets() {
			if q == p {
				known = true
				break
			}
		}
		if !known {
			m.setStatus(":preset: unknown name " + fmt.Sprintf("%q", rest))
			return m, flashClearCmd(m.statusGen)
		}
		return m.switchPreset(p)
	case "sort":
		// Bare `:sort` lands here with rest == "" — treat as a
		// usage error so the user sees the expected axes
		// instead of silently switching to no-sort. The explicit
		// way to clear is `:sort none`.
		if rest == "" {
			m.setStatus(":sort: axis required (one of none, priority, updated, repo, id, deps)")
			return m, flashClearCmd(m.statusGen)
		}
		k, ok := parseSortKey(rest)
		if !ok {
			m.setStatus(":sort: unknown axis. Try one of none, priority, updated, repo, id, deps")
			return m, flashClearCmd(m.statusGen)
		}
		return m.setSortKey(k)
	case "reverse":
		return m.reverseSort()
	case "filter":
		return m.dispatchFilterCommand(rest)
	case "bd":
		return m.runRawBD(rest)
	case "help":
		return m.openHelp()
	case "assign":
		// Bare `:assign` opens the cursor row's owner prompt
		// (same as O); `:assign <name>` short-circuits the prompt
		// and dispatches the value directly so power users can
		// pipe through the palette without a second keystroke.
		if rest == "" {
			return m.beginAssign()
		}
		return m.dispatchPaletteAssign(rest)
	case "priority":
		// `:priority <0-4>` sets an absolute priority on the
		// cursor row (or every marked row), distinct from the
		// `+`/`-` relative-bump keys. Out-of-range surfaces a
		// usage error.
		return m.dispatchPalettePriority(rest)
	case "label":
		// Bare `:label` opens the toggle prompt (same as L);
		// `:label <name>` toggles directly.
		if rest == "" {
			return m.beginLabel()
		}
		return m.dispatchPaletteLabel(rest)
	default:
		m.setStatus(":" + name + ": unknown command. Known: refresh, preset, sort, reverse, filter save <name>, assign, priority <0-4>, label, bd <args>, help")
		return m, flashClearCmd(m.statusGen)
	}
}

// dispatchPaletteAssign sets the cursor row's owner (or every
// marked row) to the supplied value without opening the prompt.
// Mirrors updateAssign's enter case so behaviour stays in sync.
func (m Model) dispatchPaletteAssign(owner string) (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("assign", targets, func(ctx context.Context, i beads.Issue) error {
			return mu.SetAssignee(ctx, i, owner)
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus(":assign: nothing to reassign")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	if !m.issueExists(target.ID) {
		m.setStatus(":assign cancelled: " + target.ID + " was removed by a refresh")
		return m, flashClearCmd(m.statusGen)
	}
	m.lastAction = repeatableAction{kind: "assign", arg: owner}
	return m, runWriteWithIssue("assign", target, func(ctx context.Context) error {
		return mu.SetAssignee(ctx, target, owner)
	})
}

// dispatchPalettePriority is the absolute-priority counterpart to
// the `+`/`-` relative-bump keys. Accepts 0-4 (bd's range);
// out-of-range or non-numeric input surfaces a usage error so a
// typo doesn't get clamped silently into a real write.
func (m Model) dispatchPalettePriority(arg string) (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if arg == "" {
		m.setStatus(":priority: value required (0-4)")
		return m, flashClearCmd(m.statusGen)
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 0 || n > 4 {
		m.setStatus(":priority: value must be 0-4")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("priority", targets, func(ctx context.Context, i beads.Issue) error {
			return mu.SetPriority(ctx, i, n)
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus(":priority: nothing to reprioritize")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	if !m.issueExists(target.ID) {
		m.setStatus(":priority cancelled: " + target.ID + " was removed by a refresh")
		return m, flashClearCmd(m.statusGen)
	}
	m.lastAction = repeatableAction{kind: "priority", arg: strconv.Itoa(n)}
	return m, runWrite(fmt.Sprintf("set P%d", n), target.ID, func(ctx context.Context) error {
		return mu.SetPriority(ctx, target, n)
	})
}

// dispatchPaletteLabel mirrors updateLabel's toggle-or-add logic
// for the palette path. Single target toggles; bulk path is
// add-only (consistent with H's bulk semantics).
func (m Model) dispatchPaletteLabel(label string) (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("label", targets, func(ctx context.Context, i beads.Issue) error {
			if i.HasLabel(label) {
				return nil
			}
			return mu.AddLabel(ctx, i, label)
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus(":label: nothing to label")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	if !m.issueExists(target.ID) {
		m.setStatus(":label cancelled: " + target.ID + " was removed by a refresh")
		return m, flashClearCmd(m.statusGen)
	}
	if target.HasLabel(label) {
		m.lastAction = repeatableAction{kind: "unlabel", arg: label}
		return m, runWrite("unlabel:"+label, target.ID, func(ctx context.Context) error {
			return mu.RemoveLabel(ctx, target, label)
		})
	}
	m.lastAction = repeatableAction{kind: "label", arg: label}
	return m, runWrite("label:"+label, target.ID, func(ctx context.Context) error {
		return mu.AddLabel(ctx, target, label)
	})
}

// runRawBD shells out a `bd <args>` invocation in the cursor
// row's workspace and switches to modeOutput to show stdout. If
// the source doesn't implement rawBDInvoker (e.g. a test stub),
// surface a status banner so the user knows the command isn't
// available. Empty args is a usage error — bare `:bd` would
// surface bd's own usage anyway, but we save the round-trip.
func (m Model) runRawBD(rest string) (tea.Model, tea.Cmd) {
	if rest == "" {
		m.setStatus(":bd: args required (try :bd ready, :bd show <id>, …)")
		return m, flashClearCmd(m.statusGen)
	}
	raw, ok := m.src.(rawBDInvoker)
	if !ok {
		m.setStatus(":bd: this source doesn't support raw invocations")
		return m, flashClearCmd(m.statusGen)
	}
	// Pick the cursor row's repo so the bd subprocess lands in the
	// right workspace; empty (no rows / out-of-range cursor)
	// falls back to whatever the source picks (first sub in
	// multi, the single client in single).
	repo := ""
	if len(m.visible) > 0 && m.cursor >= 0 && m.cursor < len(m.visible) {
		repo = m.visible[m.cursor].Repo
	}
	args := shellFields(rest)
	note := rawWriteWarning(args)
	return m, func() tea.Msg {
		out, err := raw.RawBD(context.Background(), repo, args)
		return rawBDMsg{args: rest, out: out, err: err, note: note}
	}
}

// shellFields splits s into args, honoring "..." and '...' quoting
// so `:bd query "p0"` reaches bd as ["query", "p0"] instead of
// ["query", "\"p0\""]. Doesn't handle escapes (\"), backticks, or
// $() — wyk is a TUI launcher for bd commands, not a shell, and
// the simpler grammar is easier to reason about. Mixed quoting
// inside a single token (e.g. foo"bar") is preserved as-is.
//
// An explicitly-empty quoted argument is preserved: `--desc ""`
// emits ["--desc", ""] so a user can clear a bd field that
// accepts an empty value. Without the `started` flag this would
// silently drop the empty token (no runes written).
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inDouble, inSingle := false, false
	started := false // true once the current token has any content (including an empty quoted span)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case (r == ' ' || r == '\t') && !inDouble && !inSingle:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

// rawBDInvoker is the optional capability the `:bd <args>`
// command needs — wired by BDSource and MultiBDSource. The model
// type-asserts at the call site so a read-only or test source
// can still load.
type rawBDInvoker interface {
	RawBD(ctx context.Context, repo string, args []string) ([]byte, error)
}

// rawBDMsg carries the result of a `:bd <args>` invocation back
// to the model. err non-nil means bd exited non-zero (or the
// subprocess failed entirely); in either case we still want to
// show whatever stdout we captured plus the error.
type rawBDMsg struct {
	args string
	out  []byte
	err  error
	// note is a wyk-side advisory (not from bd) prepended to the output
	// overlay — e.g. the un-committed-write warning (would-you-kindly-17aw).
	note string
}

// rawWriteWarning returns an advisory string when a `:bd` argv is a
// WRITE that would land in Dolt's working set WITHOUT an explicit
// --dolt-auto-commit (so it may silently revert on a later push);
// otherwise it returns "". The user passing --dolt-auto-commit (either
// value) means they own the persistence decision, so we stay quiet.
func rawWriteWarning(args []string) string {
	if hasFlagPrefix(args, "--dolt-auto-commit") {
		return ""
	}
	verb, sub := firstVerbAndSub(args)
	write := false
	switch verb {
	case "create", "close", "reopen", "delete", "update", "defer", "undefer",
		"supersede", "forget", "remember", "note", "comment":
		write = true
	case "dep", "label":
		// Two-word forms: "dep add/remove" and "label add/remove" write;
		// "dep list" / "label" (list) are reads.
		if sub == "add" || sub == "remove" || sub == "rm" {
			write = true
		}
	}
	if !write {
		return ""
	}
	return "note: raw `:bd` writes are NOT auto-committed — this change may not " +
		"survive a later `bd dolt push`. Re-run with --dolt-auto-commit=on, or use " +
		"the dedicated TUI keys (a/H/n/d/…), which commit for you."
}

// firstVerbAndSub finds the first non-flag token (the bd subcommand) and
// the token after it, skipping leading global flags so a form like
// `:bd -C /dir close a-1` still detects `close`. A value-taking global
// flag in separate-arg form (-C <dir>) consumes its value too; the
// inline form (-C=dir, --flag=v) consumes only itself.
func firstVerbAndSub(args []string) (verb, sub string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		if !strings.Contains(a, "=") && valueTakingGlobalFlag(a) {
			i += 2
		} else {
			i++
		}
	}
	if i < len(args) {
		verb = args[i]
		if i+1 < len(args) {
			sub = args[i+1]
		}
	}
	return verb, sub
}

// valueTakingGlobalFlag reports whether a bd global flag consumes the
// following token as its value (separate-arg form). Coverage is
// intentionally limited to the common globals (-C / --dir, by far the
// most likely to precede a `:bd` subcommand). This gates an
// advisory-only un-committed-write warning, so a rarer value-flag
// (`--db <path>`, etc.) preceding the verb merely risks a MISSED nudge,
// never a wrong action — an acceptable trade for not enumerating bd's
// whole global-flag surface here (roborev #1842).
func valueTakingGlobalFlag(a string) bool {
	switch a {
	case "-C", "--dir":
		return true
	}
	return false
}

// hasFlagPrefix reports whether args contains `name` or `name=...`.
func hasFlagPrefix(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// updateOutput drives the read-only modeOutput overlay. q / esc /
// enter all close it; any other key is dropped.
func (m Model) updateOutput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc", "q", "enter":
		m.mode = modeList
		m.outputText = ""
		m.outputVP.SetContent("")
		return m, nil
	}
	// Anything else (j/k/PgUp/PgDn/g/G/d/u/ctrl+f/ctrl+b) flows
	// to the viewport, which has its own KeyMap for vim-style +
	// half-page scrolling.
	var cmd tea.Cmd
	m.outputVP, cmd = m.outputVP.Update(msg)
	return m, cmd
}

// viewOutput renders the captured bd output through the scrollable
// viewport so a long `bd list --all` doesn't lose the header and
// footer to terminal scroll. Footer shows ScrollPercent when the
// body actually overflows — mirrors the detail view's pattern.
func (m Model) viewOutput() string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("bd output"))
	b.WriteString("\n")
	b.WriteString(m.outputVP.View())
	b.WriteString("\n")
	footer := "esc / q / enter to close   j/k ↑↓ scroll"
	if m.outputVP.TotalLineCount() > m.outputVP.Height {
		pct := int(m.outputVP.ScrollPercent() * 100)
		footer = fmt.Sprintf("%d%%   %s", pct, footer)
	}
	b.WriteString(helpStyle.Render(footer))
	return b.String()
}

// dispatchFilterCommand handles the `:filter <sub> <args>` family.
// Only `save <name>` is supported today; the function exists as a
// branch point so a future `:filter clear`, `:filter list`, etc.
// don't bloat the main dispatchCommand switch.
func (m Model) dispatchFilterCommand(rest string) (tea.Model, tea.Cmd) {
	sub, args, _ := strings.Cut(rest, " ")
	args = strings.TrimSpace(args)
	switch sub {
	case "save":
		if args == "" {
			m.setStatus(":filter save: missing alias name")
			return m, flashClearCmd(m.statusGen)
		}
		if m.query == "" {
			m.setStatus(":filter save: no active query to save")
			return m, flashClearCmd(m.statusGen)
		}
		// Compose the would-be aliases (in-memory state stays
		// untouched until Save succeeds) so a persistence failure
		// can't leave the session showing an alias that won't
		// survive a restart.
		path, err := filters.DefaultPath()
		if err != nil {
			m.setStatus(":filter save failed: " + err.Error())
			return m, nil
		}
		// cloneAliases guarantees a non-nil Aliases map; no nil
		// guard needed here.
		next := cloneAliases(m.filterAliases)
		next.Aliases[args] = m.query
		if err := filters.Save(path, next); err != nil {
			m.setStatus(":filter save failed: " + err.Error())
			return m, nil
		}
		m.filterAliases = next
		m.setStatus("saved @" + args)
		return m, flashClearCmd(m.statusGen)
	case "list":
		// Show every saved alias in a sorted plain-text overlay
		// (reuses modeOutput's viewport so a registry of 50+
		// aliases stays scrollable). Empty registry shows a
		// status banner instead of an empty overlay — saves a
		// keystroke for the common "I haven't saved any" case.
		if len(m.filterAliases.Aliases) == 0 {
			m.setStatus(":filter list: no aliases saved (use :filter save <name>)")
			return m, flashClearCmd(m.statusGen)
		}
		names := make([]string, 0, len(m.filterAliases.Aliases))
		for k := range m.filterAliases.Aliases {
			names = append(names, k)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString("saved filter aliases\n\n")
		for _, name := range names {
			fmt.Fprintf(&b, "  @%-12s  %s\n", name, m.filterAliases.Aliases[name])
		}
		m.outputText = b.String()
		m.outputVP.SetContent(m.outputText)
		m.outputVP.GotoTop()
		m.mode = modeOutput
		return m, nil
	case "remove":
		if args == "" {
			m.setStatus(":filter remove: missing alias name")
			return m, flashClearCmd(m.statusGen)
		}
		if _, ok := m.filterAliases.Aliases[args]; !ok {
			m.setStatus(":filter remove: no alias @" + args)
			return m, flashClearCmd(m.statusGen)
		}
		// Stage the deletion on a clone so a persist failure
		// keeps the in-memory view consistent with disk — a
		// stale ":filter list" showing a still-saved alias is
		// less confusing than an alias that silently reappears
		// after a restart.
		path, err := filters.DefaultPath()
		if err != nil {
			m.setStatus(":filter remove failed: " + err.Error())
			return m, nil
		}
		next := cloneAliases(m.filterAliases)
		delete(next.Aliases, args)
		if err := filters.Save(path, next); err != nil {
			m.setStatus(":filter remove failed: " + err.Error())
			return m, nil
		}
		m.filterAliases = next
		m.setStatus("removed @" + args)
		return m, flashClearCmd(m.statusGen)
	default:
		m.setStatus(":filter: unknown subcommand. Try: save <name>, list, remove <name>")
		return m, flashClearCmd(m.statusGen)
	}
}

// cloneAliases returns a deep copy of the on-disk Aliases shape
// so the filter-save/remove flows can stage their mutation on a
// clone and only commit it to m.filterAliases when the persist
// succeeds. Without this, a persist failure would leave the
// in-memory map mutated while the user sees ":filter * failed",
// surfacing the divergence on the next ":filter list".
func cloneAliases(a filters.Aliases) filters.Aliases {
	out := filters.Aliases{Version: a.Version, Aliases: make(map[string]string, len(a.Aliases))}
	for k, v := range a.Aliases {
		out.Aliases[k] = v
	}
	return out
}
