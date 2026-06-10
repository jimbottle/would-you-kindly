// wyk (would-you-kindly) is a terminal UI over the bd (beads) issue
// tracker. It surfaces tasks an agent has handed to a human — see
// docs/CONTRACT.md for the convention it follows.
//
// Modes:
//
//	wyk                      TUI (default)
//	wyk --version            print version and exit
//	wyk --probe              non-TTY one-shot listing the human-flagged issues
//	wyk handoff <id>         hand <id> back to a human; runbook read from stdin
//	wyk init                 install the post-commit auto-close hook
//	wyk hook post-commit     called by the installed hook; closes referenced issues
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/filters"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
	"github.com/jimbottle/would-you-kindly/internal/theme"
	"github.com/jimbottle/would-you-kindly/internal/tui"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
	"github.com/jimbottle/would-you-kindly/internal/updater"
	"github.com/jimbottle/would-you-kindly/internal/watch"
	"github.com/jimbottle/would-you-kindly/pkg/handoff"
)

// noColorRequested reports whether the user has asked to disable
// color. NO_COLOR is the cross-tool convention (no-color.org — any
// non-empty value); WYK_NO_COLOR is the wyk-specific escape hatch
// for environments where the user wants colored output from
// everything else but not from wyk. Either is sufficient.
// Separated from applyNoColor so the env-detection logic is
// unit-testable without touching lipgloss's global renderer state.
func noColorRequested() bool {
	return os.Getenv("NO_COLOR") != "" || os.Getenv("WYK_NO_COLOR") != ""
}

// disableColor forces lipgloss's default renderer into ASCII so
// badges, chips, and status styles render as plain text. The single
// place the renderer is downgraded — shared by the env path
// (applyNoColor) and the --no-color flag.
func disableColor() {
	lipgloss.SetColorProfile(termenv.Ascii)
}

// applyNoColor downgrades to ASCII when the user has opted out of
// color via the environment. Called once at startup; useful for
// screen readers, log capture, SSH into dumb terminals, and CI runs
// of `wyk --probe`. The --no-color flag (parsed later, on the TUI /
// probe path) is the explicit equivalent.
func applyNoColor() {
	if !noColorRequested() {
		return
	}
	disableColor()
}

// subcommandHandlers maps every dispatchable subcommand name to its
// runner. main consults it before flag.Parse so each subcommand can
// own its own FlagSet without interfering with the top-level flags;
// strayArgMsg consults it so the flags-before-subcommand error can
// never drift from the real dispatch surface, and
// TestWykSubcommandsMatchDispatch pins it against the completion
// list (roborev #2029). The `--version` / `-v` aliases are handled
// separately in main — they're flag-spelled aliases, not subcommand
// names, and don't belong in completion or did-you-mean.
var subcommandHandlers = map[string]func([]string) int{
	"handoff":     runHandoff,
	"create":      runCreate,
	"init":        runInit,
	"hook":        runHook,
	"inbox":       runInbox,
	"stats":       runStats,
	"doctor":      runDoctor,
	"registry":    runRegistry,
	"conventions": runConventions,
	"update":      runUpdate,
	"dashboard":   runDashboard,
	"export":      runExport,
	"import":      runImport,
	"activity":    runActivity,
	"skills":      runSkills,
	"depgraph":    runDepgraph,
	"help":        runHelp,
	"completion":  runCompletion,
	"version":     runVersion,
}

func main() {
	applyNoColor()
	if len(os.Args) >= 2 {
		if run, ok := subcommandHandlers[os.Args[1]]; ok {
			os.Exit(run(os.Args[2:]))
		}
		switch os.Args[1] {
		case "--version", "-v":
			os.Exit(runVersion(os.Args[2:]))
		}
	}

	flag.Usage = printTopLevelUsage
	dir := flag.String("C", "", "run as if bd had been started in this directory")
	me := flag.String("me", "", "current user, used by the 'mine' preset (default: git user.email or $USER)")
	probe := flag.Bool("probe", false, "non-TTY: print the human-flagged issues and exit (useful in scripts/CI)")
	startupPreset := flag.String("preset", "", "launch into a specific preset (all, ready, human, mine, blocked)")
	noColor := flag.Bool("no-color", false, "disable colored output (same as NO_COLOR / WYK_NO_COLOR)")
	flag.Parse()
	// Anything left positional is either a subcommand typo (`wyk inbx`)
	// or a flags-before-subcommand invocation (`wyk -C dir handoff`) —
	// the dispatcher above only looks at os.Args[1], so both used to
	// fall through here and silently launch the TUI. Fail loudly
	// instead (would-you-kindly-tu9t).
	if msg, bad := strayArgGuard(flag.Args()); bad {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(64)
	}
	// The flag is the explicit equivalent of the env opt-out. The env
	// path already ran in applyNoColor at startup; honor the flag here,
	// before the model is built or --probe renders, since both touch
	// the package-level lipgloss styles.
	if *noColor {
		disableColor()
	}
	if *startupPreset != "" && !filter.IsPreset(*startupPreset) {
		fmt.Fprintf(os.Stderr, "wyk: unknown -preset %q (valid: ", *startupPreset)
		for i, p := range filter.AllPresets() {
			if i > 0 {
				fmt.Fprint(os.Stderr, ", ")
			}
			fmt.Fprint(os.Stderr, p)
		}
		fmt.Fprintln(os.Stderr, ")")
		os.Exit(64)
	}

	// Resolve --me lazily so a user supplying --me doesn't pay the cost
	// of shelling out to git, and so startup doesn't depend on git being
	// on PATH unless the default is actually needed.
	if *me == "" {
		*me = defaultMe()
	}

	src, repoPaths, hint, err := buildSource(*dir, *me)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}

	if *probe {
		os.Exit(runProbe(src))
	}

	// Overlay user theme.json onto the built-in lipgloss styles
	// before constructing the model — the styles are package vars,
	// so this must run before NewWithHint touches them. A missing
	// file falls through to the built-in defaults; a malformed
	// file logs a notice but still launches with defaults so a
	// botched theme can't lock the user out of the TUI.
	if th, err := theme.LoadDefault(); err == nil {
		tui.ApplyTheme(th)
	} else {
		fmt.Fprintln(os.Stderr, "wyk: theme.json:", err, "(using defaults)")
	}

	model := tui.NewWithHint(src, hint).WithMe(*me)
	// Restore the last session (filter preset, sort key, cursor) from
	// ~/.config/wyk/state.json so a re-opened tab lands where the user
	// left off. Applied BEFORE the -preset flag so an explicit flag
	// still wins, and before WithCacheSnapshot so the warm-start cache
	// is matched against the restored preset. A missing file restores
	// nothing; a corrupt one logs and falls back to defaults — never
	// fatal. We still wire the path on a load error so the next quit
	// REPAIRS the bad file.
	if sPath, err := tui.SessionDefaultPath(); err == nil {
		st, err := tui.LoadSession(sPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk: state.json:", err, "(starting fresh)")
		}
		model = model.WithSession(st, sPath)
	}
	if *startupPreset != "" {
		model = model.WithPreset(filter.Preset(*startupPreset))
	}

	// Spin up the filesystem watcher so external bd writes (a git
	// pull pulling a new issue, another wyk instance committing,
	// the post-commit hook auto-closing) refresh the list
	// instantly instead of waiting up to 10s for the polling tick.
	// Best-effort: a watcher failure (rare; usually a network FS)
	// silently degrades to the polling path. Lifecycle is tied to
	// the TUI's run — we leak the watcher goroutine on a hard exit,
	// which is fine because the process is already going away.
	if w, err := watch.New(context.Background(), repoPaths); err == nil {
		model = model.WithFSEvents(w.Events())
	}
	// Hydrate column-visibility state from ~/.config/wyk/ui.json
	// so the user's last layout choice survives a restart. A
	// missing or unreadable file falls back to "all columns on"
	// silently — we don't want a corrupt ui.json to block launch.
	// On a recoverable parse error we still wire the path so a
	// subsequent overlay save can REPAIR the bad file. The one
	// case we leave persistence disabled is an unsupported future
	// version — overwriting that would silently downgrade a
	// forward-compatible file.
	if uiPath, err := uiconfig.DefaultPath(); err == nil {
		cfg, err := uiconfig.Load(uiPath)
		switch {
		case err == nil:
			model = model.WithHiddenColumns(cfg.HiddenSet(), uiPath).
				WithPriorityEmphasis(cfg.PriorityEmphasis)
		case errors.Is(err, uiconfig.ErrUnsupportedVersion):
			// Don't touch the file. Leave columns at default for
			// this session.
		default:
			model = model.WithHiddenColumns(map[string]bool{}, uiPath)
		}
	}
	// Load filter aliases (~/.config/wyk/filters.json) so @name
	// expansion is available from the / prompt. A missing or
	// corrupt file silently falls back to no-aliases; a FUTURE
	// schema version surfaces a startup banner so the user knows
	// their newer file isn't being honored (and we don't risk
	// overwriting it on a `:filter save`). The latter distinction
	// is exactly what filters.ErrUnsupportedVersion exists for.
	if fpath, err := filters.DefaultPath(); err == nil {
		a, err := filters.Load(fpath)
		switch {
		case err == nil:
			model = model.WithFilterAliases(a)
		case errors.Is(err, filters.ErrUnsupportedVersion):
			fmt.Fprintf(os.Stderr, "wyk: %s declares a newer schema; aliases disabled this session. Update wyk or move the file aside to re-enable.\n", fpath)
		default:
			// Corrupt JSON / I/O — silent fallback so a
			// transient read error doesn't block launch.
		}
	}

	// Read the cached update nudge once at startup so the banner
	// can render immediately if there's already a snapshot on
	// disk. The background goroutine below refreshes it for the
	// next run.
	if nudge := readUpdateNudge(versionString()); nudge != "" {
		model = model.WithUpdateNudge(nudge)
	}

	// Warm-start: seed m.all from the last-saved fetch so the
	// first frame paints rows instead of the empty "loading…"
	// stand-in. The live fetch dispatched by Init still runs in
	// parallel; the cached rows are replaced when it returns.
	// Best-effort: any cache failure (missing file, stale TTL,
	// unsupported schema) silently falls back to the cold path.
	if cachePath, err := tui.CacheDefaultPath(); err == nil {
		cache, _ := tui.LoadCache(cachePath)
		model = model.WithCacheSnapshot(cache, cachePath)
	}
	// No mouse capture: with mouse reporting off, the host terminal
	// handles native click-drag text selection in every view, so users
	// can select and copy the list columns (and detail body). The cost
	// is wheel-scroll / click-to-set-cursor — navigation is keyboard
	// only (j/k, PgUp/PgDn, g/G). Selecting text over the CLI was the
	// repeated ask; mouse nav had keyboard equivalents.
	// Optional debug logging (would-you-kindly-2vyt): WYK_DEBUG=1 (or
	// WYK_LOG_FILE=<path>) tees Bubble Tea's standard logger — and every
	// bd invocation's argv + timing — to a file, so a stuck or slow TUI
	// can be diagnosed; stderr is invisible behind the alt-screen.
	if logPath := debugLogPath(); logPath != "" {
		if lf, err := tea.LogToFile(logPath, "wyk"); err == nil {
			defer func() { _ = lf.Close() }()
			beads.Debug = true
			log.Printf("wyk %s starting; debug logging enabled (%s)", versionString(), logPath)
		} else {
			fmt.Fprintf(os.Stderr, "wyk: could not open debug log %q: %v\n", logPath, err)
		}
	}
	p := tea.NewProgram(model, tea.WithAltScreen())
	// Kick a best-effort live check in the background. We don't
	// post the result back into the running TUI — the snapshot
	// lands on disk and the NEXT wyk invocation reads it. This
	// keeps the TUI hot path free of network I/O entirely.
	go backgroundUpdateCheck()
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wyk:", err)
		os.Exit(1)
	}
}

// backgroundUpdateCheck refreshes the update-check cache without
// blocking the TUI. Runs in a goroutine launched from main. All
// failures are swallowed silently — the cache stays stale and the
// next run still works.
func backgroundUpdateCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = updater.LatestCached(ctx, nil)
}

// buildSource picks single-repo vs multi-repo wiring based on the
// flags and the registry state:
//
//   - -C <dir>: explicit single-repo, scoped to that workspace.
//   - registry has 2+ repos: multi-repo source.
//   - registry has 1 repo: single-repo source against that repo
//     (NOT cwd) — a user who registered one project then runs `wyk`
//     from anywhere should land in that project, not get an opaque
//     "no workspace here" failure.
//   - registry is empty: single-repo against cwd, the v0.1.0
//     fallback so a user who hasn't run `wyk init` anywhere still
//     gets a working TUI from inside a bd repo.
func buildSource(dir, me string) (tui.Source, []string, string, error) {
	if dir != "" {
		// Validate -C up front so a bad path produces a clean message
		// instead of bd's raw JSON error blob surfacing through a
		// failed Fetch ("bd query …: { \"error\": … }"). A common
		// typo deserves a one-liner, not a stack of escaped JSON.
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil, "", fmt.Errorf("-C directory %q does not exist", dir)
			}
			return nil, nil, "", fmt.Errorf("-C directory %q: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, nil, "", fmt.Errorf("-C %q is not a directory", dir)
		}
		c := beads.NewClient()
		c.Dir = dir
		return &tui.BDSource{Client: c, Me: me, Name: filepath.Base(dir)}, []string{dir}, "", nil
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return nil, nil, "", err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, nil, "", err
	}
	switch len(reg.Repos) {
	case 0:
		// Empty registry: behave like v0.1.0 with the cwd, but
		// surface a banner in the TUI so the multi-repo feature
		// isn't invisible to users who just installed. Repo column
		// gets the cwd's basename so the layout stays consistent
		// with the multi-repo view.
		c := beads.NewClient()
		hint := "No repos registered yet — running against cwd only.\n" +
			"  Run `wyk init` here, or `wyk init -scan ~/Projects` to discover every bd workspace under that tree."
		// Fall back to a sentinel if cwd is unreadable so the
		// Repo column doesn't silently disappear — keeping the
		// layout consistent matters more than a perfect name in
		// the rare-failure case.
		name := "(cwd)"
		var paths []string
		if cwd, err := os.Getwd(); err == nil {
			name = filepath.Base(cwd)
			paths = []string{cwd}
		}
		return &tui.BDSource{Client: c, Me: me, Name: name}, paths, hint, nil
	case 1:
		// Single registered repo: use it (not cwd).
		c := beads.NewClient()
		c.Dir = reg.Repos[0].Path
		return &tui.BDSource{Client: c, Me: me, Name: reg.Repos[0].Name}, []string{reg.Repos[0].Path}, "", nil
	default:
		clients := make([]*beads.Client, len(reg.Repos))
		names := make([]string, len(reg.Repos))
		paths := make([]string, len(reg.Repos))
		for i, r := range reg.Repos {
			c := beads.NewClient()
			c.Dir = r.Path
			clients[i] = c
			names[i] = r.Name
			paths[i] = r.Path
		}
		src, err := tui.NewMultiBDSource(clients, names, me)
		return src, paths, "", err
	}
}

// runHandoff implements `wyk handoff`: read a runbook from stdin
// (or --file), then call pkg/handoff.BounceToHuman against the bd
// CLI client. Two modes:
//
//	wyk handoff <id>             hand off an EXISTING issue
//	wyk handoff -create "title"  FILE a new issue and hand it off
//	                             in one step (the common agent case)
//
// The -create mode is the more common agent-side path: the agent
// has just decided this needs a human, so it both files the bd
// issue and applies the human label in a single invocation.
//
// Exit codes:
//
//	0   success (also returned for --help, which is a deliberate request)
//	1   generic failure (bd error, IO error, …)
//	2   bd missing or no workspace
//	64  usage error (bad flags / missing args / TTY-stdin without --allow-empty)
//
// handoffRunbookTemplate is the skeleton `wyk handoff --template` prints.
// It mirrors the three REQUIRED sections in docs/CONTRACT.md so a human
// filling in a handoff doesn't have to memorize the headings.
const handoffRunbookTemplate = `## Why this needs you (please confirm this is accurate)
<What you tried (three concrete attempts), the boundary you hit, and why
no workaround exists. Phrased as a claim the human can push back on.>

## Steps
1. <concrete step with a location>
2. <…>
3. Close this issue when complete.

## What unblocks me when this returns
<The concrete artifact you expect back — a credential at a known path, a
URL in a constant, a decision recorded here — so the next agent can resume.>
`

func runHandoff(args []string) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	dir := fs.String("C", "", "run as if bd had been started in this directory")
	file := fs.String("file", "", "read the runbook from this file (default: stdin)")
	allowEmpty := fs.Bool("allow-empty", false,
		"permit an empty runbook (clears the issue's description). Required when stdin is a TTY.")
	createTitle := fs.String("create", "",
		"file a NEW bd issue with this title and hand it off; mutually exclusive with the <id> positional")
	priority := fs.String("priority", "1",
		"priority for the newly-created issue (only used with -create; 0-4 or P0-P4)")
	issueType := fs.String("type", "task",
		"issue type for the newly-created issue (only used with -create)")
	note := fs.String("note", "",
		"after the handoff lands, append this one-line note to the issue (via bd note) — useful for 'back to you, see X' annotations without nuking the runbook")
	identity := fs.String("identity", "",
		// The leading backquoted `name` is load-bearing: Go's flag
		// package takes the FIRST backquoted token in a usage string
		// as the flag's value placeholder. With backticks around `wyk
		// inbox` instead, -h rendered the flag as "-identity wyk inbox"
		// (would-you-kindly-k3fb).
		"route this handoff to the named agent identity `name` (adds the src:agent:<name> label) so it lands in that identity's wyk inbox when bounced back; falls back to $WYK_AGENT_IDENTITY")
	dryRun := fs.Bool("dry-run", false,
		"print the runbook, labels, and destination ID that would be written without invoking bd; useful for verifying a runbook is well-formed before committing the human to it")
	template := fs.Bool("template", false,
		"print the required 3-section runbook skeleton to stdout and exit (no bd writes); pipe it into your editor, fill it in, then `wyk handoff <id> < filled.md`")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// --help is a successful request; flag printed usage already.
			return 0
		}
		return 64
	}

	// --template short-circuits everything: emit the runbook skeleton so a
	// human (or agent) has the required structure to fill in, rather than
	// memorizing the three headings (would-you-kindly-uhux).
	if *template {
		fmt.Print(handoffRunbookTemplate)
		return 0
	}

	// Resolve the routing identity (flag > $WYK_AGENT_IDENTITY > none).
	// A set-but-malformed identity is a usage error; routing to a bad
	// label would silently never reach the intended inbox.
	ident, identErr := resolveIdentity(*identity)
	if identErr != nil {
		fmt.Fprintln(os.Stderr, "wyk handoff:", identErr)
		return 64
	}

	// Validate the two modes.
	switch {
	case *createTitle != "" && fs.NArg() > 0:
		fmt.Fprintln(os.Stderr, "wyk handoff: -create and a positional <issue-id> are mutually exclusive")
		return 64
	case *createTitle == "" && fs.NArg() != 1:
		fmt.Fprintln(os.Stderr,
			"usage: wyk handoff [-C <dir>] [-file <path>] [-allow-empty] [-note <text>] [-identity name] [-dry-run] <issue-id>\n"+
				"   or: wyk handoff -create \"<title>\" [-priority N] [-type task] [-identity name] [-file <path>] [-dry-run]\n"+
				"   or: wyk handoff -template   (print the runbook skeleton and exit)")
		return 64
	}

	// Validate the -create attributes at parse time, BEFORE the dry-run
	// branch: bd would reject these at write time, so letting them
	// through meant -dry-run vouched for an invocation that fails for
	// real (would-you-kindly-ure8).
	if *createTitle != "" {
		if !beads.IsValidPriority(*priority) {
			fmt.Fprintf(os.Stderr, "wyk handoff: invalid -priority %q (valid: 0-4 or P0-P4)\n", *priority)
			return 64
		}
		if !beads.IsValidIssueType(*issueType) {
			fmt.Fprintf(os.Stderr, "wyk handoff: invalid -type %q (valid: %s)\n",
				*issueType, strings.Join(beads.ValidIssueTypes, ", "))
			return 64
		}
	}

	// Reading from a TTY would block waiting for user input — easy to
	// hit by accident when invoked interactively without a redirect.
	// If the user then closes stdin with ^D, we'd silently wipe the
	// issue's description. Refuse unless they opted in. Treat a Stat
	// error as "unknown — refuse" rather than "assume non-TTY", so
	// the guard fails closed in the rare case Stat fails.
	if *file == "" && !*allowEmpty {
		stat, statErr := os.Stdin.Stat()
		isTTY := statErr != nil || (stat.Mode()&os.ModeCharDevice) != 0
		if isTTY {
			fmt.Fprintln(os.Stderr,
				"wyk handoff: stdin is a TTY (or its mode could not be determined). Pipe a runbook in, pass -file <path>, or use -allow-empty to deliberately clear the description.")
			return 64
		}
	}

	var runbookBytes []byte
	var err error
	if *file != "" {
		runbookBytes, err = os.ReadFile(*file)
	} else {
		runbookBytes, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk handoff:", err)
		return 1
	}
	runbook := strings.TrimRight(string(runbookBytes), "\n")
	if runbook == "" && !*allowEmpty {
		fmt.Fprintln(os.Stderr,
			"wyk handoff: empty runbook would clear the description. Pass -allow-empty to confirm.")
		return 64
	}

	// Labels for a -create'd issue: src:agent (BounceToHuman adds `human`
	// on top), plus the Claude session (like `wyk create`) so the TUI's
	// Session column is populated for handoff-filed issues too. Empty
	// session (outside Claude Code) records nothing.
	createLabels := []string{"src:agent"}
	if ident != "" {
		// Identity routing layers on top of the collective src:agent
		// umbrella (never replaces it). wyk-contract/v3.
		createLabels = append(createLabels, identityLabel(ident))
	}
	if sl := sessionLabelFromEnv(); sl != "" {
		createLabels = append(createLabels, sl)
	}

	// -dry-run short-circuits before any bd writes. Print the
	// plan and exit; nothing is created, no labels are flipped.
	// The plan covers both -create (would-create banner + the
	// CreateOptions that would be passed) and bare-id paths.
	if *dryRun {
		fmt.Println("DRY-RUN: no bd writes performed")
		if *createTitle != "" {
			// Canonical form, matching what the real write passes to bd —
			// dry-run reporting the raw flag value while the write sends
			// "P2" for "p2" would drift the very guarantee this banner
			// exists for (roborev #2048).
			fmt.Printf("would create: title=%q priority=%s type=%s labels=%v\n",
				*createTitle, beads.CanonicalPriority(*priority), *issueType, createLabels)
			fmt.Println("would hand off the new issue to human (label=human added, description replaced)")
			if ident != "" {
				fmt.Printf("would route the new issue to identity %q (label=%s)\n", ident, identityLabel(ident))
			}
		} else {
			fmt.Printf("would hand off %s to human (label=human added, description replaced)\n", fs.Arg(0))
			if ident != "" {
				fmt.Printf("would route to identity %q (label=%s added)\n", ident, identityLabel(ident))
			}
		}
		fmt.Printf("runbook (%d bytes):\n", len(runbook))
		fmt.Println("---")
		fmt.Println(runbook)
		fmt.Println("---")
		if *note != "" {
			fmt.Printf("would note: %s\n", *note)
		}
		return 0
	}

	client := beads.NewClient()
	client.Dir = *dir

	// -create mode: file the issue first, then hand off the resulting ID.
	var id string
	createdViaFlag := false
	if *createTitle != "" {
		newID, err := client.Create(context.Background(), beads.CreateOptions{
			Title:     *createTitle,
			Labels:    createLabels,
			Priority:  beads.CanonicalPriority(*priority),
			IssueType: *issueType,
		})
		if err != nil {
			return handoffErrExit(err, "wyk handoff: create:")
		}
		id = newID
		createdViaFlag = true
		fmt.Printf("created %s — %q\n", id, *createTitle)
	} else {
		id = fs.Arg(0)
	}

	if err := handoff.BounceToHuman(context.Background(), client, id, runbook); err != nil {
		// Non-transactional create+handoff: if Create succeeded but the
		// label / description writes failed, we leave behind an orphan
		// issue with src:agent and no human/runbook. Name it explicitly
		// so the user can clean it up (we don't auto-delete — losing
		// data on a transient bd hiccup would be worse than the orphan).
		if createdViaFlag {
			fmt.Fprintf(os.Stderr,
				"wyk handoff: WARNING: created %s but the handoff (label/description) failed.\n"+
					"  The issue exists with the src:agent label but no human label and no runbook.\n"+
					"  Clean up with: bd close %s --reason=handoff-failed --dolt-auto-commit=on\n"+
					"  Or retry with: wyk handoff %s < <runbook>\n",
				id, id, id)
		}
		return handoffErrExit(err, "wyk handoff:")
	}
	fmt.Printf("handed %s to human (%d-byte runbook)\n", id, len(runbook))

	// Confirm identity routing the same way in both modes. For -create the
	// label was set at creation time (in createLabels), so it's already
	// applied — just confirm it. A bare-id handoff of a pre-existing issue
	// needs the label added now, after the handoff landed.
	switch {
	case ident == "":
		// no routing requested
	case createdViaFlag:
		fmt.Printf("routed %s to identity %q\n", id, ident)
	default:
		applyIdentityRouting(context.Background(), client, id, ident)
	}

	// -note posts a bd note AFTER the handoff lands so the timeline
	// reads chronologically: runbook set → handed off → annotation.
	// A note failure is reported but not fatal — the handoff itself
	// succeeded, so exit 0 with a warning rather than 1.
	if *note != "" {
		if err := client.Note(context.Background(), id, *note); err != nil {
			fmt.Fprintf(os.Stderr, "wyk handoff: note failed (handoff itself succeeded): %v\n", err)
		} else {
			fmt.Printf("noted %s: %s\n", id, *note)
		}
	}
	return 0
}

// handoffErrExit centralises the error → exit-code mapping so both
// the create step and the BounceToHuman step report the same
// friendly messages for the two well-known sentinels.
func handoffErrExit(err error, prefix string) int {
	if code, msg, ok := classifyBDSentinel(err); ok {
		fmt.Fprintln(os.Stderr, msg)
		return code
	}
	fmt.Fprintln(os.Stderr, prefix, err)
	return 1
}

// runProbe fetches the human preset and prints a one-line summary
// per issue. Returns the process exit code: 0 on success (any count,
// including a partial multi-repo result), 2 if bd is missing or there's
// no workspace, 1 on other errors.
//
// In multi-repo mode it prefers MultiSource.FetchWithSubErrors over the
// plain Source.Fetch (which discards per-sub errors) so a repo that's
// down is named on stderr instead of silently dropping out — otherwise
// a short or empty list reads as "no work" when the real cause is "a
// repo failed." This matches the partial-failure handling in `wyk
// inbox` and `wyk stats`.
func runProbe(src tui.Source) int {
	ctx := context.Background()
	var (
		issues    []beads.Issue
		fetchErrs []tui.FetchError
		err       error
	)
	if ms, ok := src.(tui.MultiSource); ok {
		issues, fetchErrs, err = ms.FetchWithSubErrors(ctx, filter.PresetHuman)
	} else {
		issues, err = src.Fetch(ctx, filter.PresetHuman)
	}
	if err != nil {
		if code, msg, ok := classifyBDSentinel(err); ok {
			fmt.Fprintln(os.Stderr, msg)
			return code
		}
		fmt.Fprintln(os.Stderr, "wyk:", err)
		return 1
	}
	fmt.Printf("%d issue(s) flagged for human:\n", len(issues))
	for _, i := range issues {
		// Titles are untrusted bd content printed to a terminal — strip
		// escapes like the TUI does (would-you-kindly-5zlr).
		fmt.Printf("  %-24s P%d  %s\n", i.ID, i.Priority, sanitize.Inline(i.Title))
	}
	// Partial multi-repo failure: at least one repo responded (we're
	// past the total-failure err path above), but others didn't. Name
	// them on stderr so the count above isn't mistaken for the whole
	// registry. stdout stays clean for scripts that pipe the list.
	if len(fetchErrs) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d repo(s) failed (list may be incomplete):\n", len(fetchErrs))
		for _, fe := range fetchErrs {
			repo := fe.Repo
			if repo == "" {
				repo = "(unknown)"
			}
			fmt.Fprintf(os.Stderr, "  %s: %v\n", repo, fe.Err)
		}
	}
	return 0
}

// versionString returns the human-readable version line printed by
// `wyk --version`. Pulls from Go's build info so module-installed
// builds (go install ...@vX.Y.Z) carry their tag; source-tree
// builds (go build, go run) report "(devel)" — which is honest:
// they don't HAVE a tag. Includes the commit SHA and dirty marker
// when present in the build info's VCS stamps. No hand-maintained
// const to drift.
func versionString() string {
	const name = "wyk"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return name + " (unknown — build info missing)"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	// Go already appends "+dirty" to the pseudoversion when an
	// installed build had local modifications; strip it so we
	// don't double-stamp when vcs.modified is true below.
	v = strings.TrimSuffix(v, "+dirty")
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	suffix := ""
	if dirty {
		suffix = "-dirty"
	}
	if rev != "" {
		return fmt.Sprintf("%s %s (commit %s%s)", name, v, rev, suffix)
	}
	if dirty {
		return name + " " + v + suffix
	}
	return name + " " + v
}

// printTopLevelUsage is wired into flag.Usage so `wyk --help` (or any
// flag-parse failure) prints a structured help block instead of the
// bare flag list Go's default emits. Agent feedback flagged that the
// subcommands — especially `handoff`, the recommended path for filing
// a human task — were invisible from --help. Listing them here closes
// the discoverability gap that produced wrong-labelled bd issues.
func printTopLevelUsage() {
	w := flag.CommandLine.Output()
	fmt.Fprint(w, `wyk — terminal UI over the bd issue tracker, with a handoff convention
                for the agent ↔ human round-trip.

Usage:
  wyk [flags]               run the TUI (default)
  wyk <subcommand> [args]

Subcommands:
  handoff      hand a bd issue to a human (preferred over hand-rolling labels)
  create       file a bd issue, stamping the Claude session that created it
  inbox        list issues a human bounced back to the agent
  init         install the post-commit auto-close hook in this repo
  doctor       diagnose installation / registry / per-repo configuration
  stats        aggregate handoff metrics across registered repos
  dashboard    per-repo open/human/closed-this-week summary (-json for structured)
  export       JSON dump of every registered repo's full issue list + ready IDs
  import       restore from a 'wyk export' dump (-file path, -dry-run)
  activity     recently-touched issues across registered repos (-since 24h, -json)
  depgraph     cross-repo dependency graph as a text tree, -dot, or -json
  skills       install the wyk agent skills into ~/.claude/skills (list/print/uninstall)
  help         pointer to the in-TUI overlay; --markdown emits a keymap reference
  completion   emit bash/zsh/fish completion script (run: wyk completion <shell>)
  registry     list / remove / prune registered workspaces
  conventions  print the agent-facing label convention (-json for structured)
  update       check for and install a newer wyk release
  version      print the version string (--check polls the release feed)
  hook         internal: invoked by the installed post-commit hook

Top-level flags (TUI / --probe mode):
`)
	flag.PrintDefaults()
	fmt.Fprint(w, `
For the agent-facing labels (`+"`human`"+`, `+"`src:agent`"+`) and the inbox
query, run: wyk conventions
`)
}

// defaultMe resolves the current identity the way bd itself does:
// prefer git's configured user.email, then $USER. Empty string is a
// fine fallback — the "mine" preset degrades to "all open" when the
// identity is unknown.
func defaultMe() string {
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return os.Getenv("USER")
}
