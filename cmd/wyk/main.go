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

// applyNoColor forces lipgloss's default renderer into ASCII when
// the user has opted out of color. Badges, chips, and status
// styles then render plain text. Called once at startup; useful
// for screen readers, log capture, SSH into dumb terminals, and
// CI runs of `wyk --probe`.
func applyNoColor() {
	if !noColorRequested() {
		return
	}
	lipgloss.SetColorProfile(termenv.Ascii)
}

func main() {
	applyNoColor()
	// Subcommand dispatch happens before flag.Parse so each subcommand
	// can own its own FlagSet without interfering with the top-level
	// flags. The TUI/probe path keeps the existing flat flag layout.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "handoff":
			os.Exit(runHandoff(os.Args[2:]))
		case "init":
			os.Exit(runInit(os.Args[2:]))
		case "hook":
			os.Exit(runHook(os.Args[2:]))
		case "inbox":
			os.Exit(runInbox(os.Args[2:]))
		case "stats":
			os.Exit(runStats(os.Args[2:]))
		case "doctor":
			os.Exit(runDoctor(os.Args[2:]))
		case "registry":
			os.Exit(runRegistry(os.Args[2:]))
		case "conventions":
			os.Exit(runConventions(os.Args[2:]))
		case "update":
			os.Exit(runUpdate(os.Args[2:]))
		case "dashboard":
			os.Exit(runDashboard(os.Args[2:]))
		case "export":
			os.Exit(runExport(os.Args[2:]))
		case "import":
			os.Exit(runImport(os.Args[2:]))
		case "activity":
			os.Exit(runActivity(os.Args[2:]))
		case "help":
			os.Exit(runHelp(os.Args[2:]))
		case "completion":
			os.Exit(runCompletion(os.Args[2:]))
		case "version", "--version", "-v":
			os.Exit(runVersion(os.Args[2:]))
		}
	}

	flag.Usage = printTopLevelUsage
	dir := flag.String("C", "", "run as if bd had been started in this directory")
	me := flag.String("me", "", "current user, used by the 'mine' preset (default: git user.email or $USER)")
	probe := flag.Bool("probe", false, "non-TTY: print the human-flagged issues and exit (useful in scripts/CI)")
	startupPreset := flag.String("preset", "", "launch into a specific preset (all, ready, human, mine, blocked)")
	flag.Parse()
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
			model = model.WithHiddenColumns(cfg.HiddenSet(), uiPath)
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
	// WithMouseCellMotion lets the model receive tea.MouseMsg with
	// cell-level coordinates so a click on a row sets the cursor
	// and the scroll wheel moves up/down. Cell motion (not all
	// motion) is enough — we don't track drags or hovers.
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
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
	dryRun := fs.Bool("dry-run", false,
		"print the runbook, labels, and destination ID that would be written without invoking bd; useful for verifying a runbook is well-formed before committing the human to it")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// --help is a successful request; flag printed usage already.
			return 0
		}
		return 64
	}

	// Validate the two modes.
	switch {
	case *createTitle != "" && fs.NArg() > 0:
		fmt.Fprintln(os.Stderr, "wyk handoff: -create and a positional <issue-id> are mutually exclusive")
		return 64
	case *createTitle == "" && fs.NArg() != 1:
		fmt.Fprintln(os.Stderr,
			"usage: wyk handoff [-C <dir>] [-file <path>] [-allow-empty] [-note <text>] [-dry-run] <issue-id>\n"+
				"   or: wyk handoff -create \"<title>\" [-priority N] [-type task] [-file <path>] [-dry-run]")
		return 64
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

	// -dry-run short-circuits before any bd writes. Print the
	// plan and exit; nothing is created, no labels are flipped.
	// The plan covers both -create (would-create banner + the
	// CreateOptions that would be passed) and bare-id paths.
	if *dryRun {
		fmt.Println("DRY-RUN: no bd writes performed")
		if *createTitle != "" {
			fmt.Printf("would create: title=%q priority=%s type=%s labels=[src:agent]\n",
				*createTitle, *priority, *issueType)
			fmt.Println("would hand off the new issue to human (label=human added, description replaced)")
		} else {
			fmt.Printf("would hand off %s to human (label=human added, description replaced)\n", fs.Arg(0))
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
			Labels:    []string{"src:agent"}, // BounceToHuman will add `human` on top
			Priority:  *priority,
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
	switch {
	case errors.Is(err, beads.ErrBDNotFound):
		fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
		return 2
	case errors.Is(err, beads.ErrNoWorkspace):
		fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
		return 2
	default:
		fmt.Fprintln(os.Stderr, prefix, err)
		return 1
	}
}

// runProbe fetches the human preset and prints a one-line summary
// per issue. Returns the process exit code: 0 on success (any count),
// 2 if bd is missing or there's no workspace, 1 on other errors.
func runProbe(src tui.Source) int {
	issues, err := src.Fetch(context.Background(), filter.PresetHuman)
	if err != nil {
		switch {
		case errors.Is(err, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(err, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "wyk:", err)
			return 1
		}
	}
	fmt.Printf("%d issue(s) flagged for human:\n", len(issues))
	for _, i := range issues {
		fmt.Printf("  %-24s P%d  %s\n", i.ID, i.Priority, i.Title)
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
  inbox        list issues a human bounced back to the agent
  init         install the post-commit auto-close hook in this repo
  doctor       diagnose installation / registry / per-repo configuration
  stats        aggregate handoff metrics across registered repos
  dashboard    per-repo open/human/closed-this-week summary (−json for structured)
  export       JSON dump of every registered repo's full issue list + ready IDs
  import       restore from a 'wyk export' dump (-file path, -dry-run)
  activity     recently-touched issues across registered repos (-since 24h, -json)
  help         pointer to the in-TUI overlay; --markdown emits a keymap reference
  completion   emit bash/zsh/fish completion script (run: wyk completion <shell>)
  registry     list / remove / prune registered workspaces
  conventions  print the agent-facing label convention (–json for structured)
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
