// wyk (would-you-kindly) is a terminal UI over the bd (beads) issue
// tracker. It surfaces tasks an agent has handed to a human — see
// docs/CONTRACT.md for the convention it follows.
//
// Entry points:
//
//	wyk                      TUI (default)
//	wyk --probe              non-TTY one-shot listing the human-flagged issues
//	wyk <subcommand> [args]  one of the subcommands below
//
// Subcommands, by what they're for. This list is a map, not a
// reference — `wyk help` and docs/generated/cli.md carry the flags and
// are generated from cliSubcommandDocs, so they cannot drift.
//
//	Handoff loop:  handoff, create, inbox, conventions
//	Reporting:     stats, dashboard, activity, depgraph, export, import
//	Setup:         init, registry, config, skills, hook
//	Diagnostics:   doctor, bugreport, version, update
//	Help:          help, completion
//
// The dispatch table itself is subcommandHandlers below;
// TestWykSubcommandsMatchDispatch pins it against the completion list.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
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
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
	"github.com/jimbottle/would-you-kindly/internal/wyklog"
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
// color via the environment OR via config.json's color=never. Called
// once at startup (before subcommand dispatch, so it covers every
// surface); useful for screen readers, log capture, SSH into dumb
// terminals, and CI runs of `wyk --probe`. The --no-color flag (parsed
// later, on the TUI / probe path) is the explicit equivalent. The env
// check stays in noColorRequested (env-only, unit-tested); the config
// read is best-effort so a broken config can't block startup.
func applyNoColor(cfg wykconfig.Config) {
	if noColorRequested() || cfg.Color == wykconfig.ColorNever {
		disableColor()
	}
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
	"bugreport":   runBugreport,
	"registry":    runRegistry,
	"config":      runConfig,
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
	// Persist any panic that unwinds the main goroutine (every subcommand,
	// plus setup/teardown) to a crash log before exiting non-zero — so a
	// field crash leaves an artifact instead of a stderr trace lost behind
	// a closed alt-screen (would-you-kindly-w5bf.2). TUI panics are caught
	// by Bubble Tea (which restores the terminal) and recorded separately
	// from the ErrProgramPanic returned by p.Run below.
	defer captureCrash()
	// Read config.json once for the whole process: applyNoColor needs
	// it before dispatch (color applies to every surface), and the TUI
	// path below reuses the same value for the update-check guard — no
	// second disk read, and both observe one consistent file state.
	cfg := loadConfigBestEffort()
	applyNoColor(cfg)
	// Set up debug logging BEFORE subcommand dispatch so WYK_DEBUG /
	// WYK_LOG_FILE traces every command's bd calls, not just the TUI
	// (would-you-kindly-w5bf.1). The subcommand/version paths os.Exit
	// (skipping defers), so they route through exitWith to flush+close
	// the log; the TUI path falls through to the deferred cleanup below.
	setupDebugLogging()
	if debugLogCleanup != nil {
		defer debugLogCleanup()
	}
	// Wire the always-on bd-failure log before dispatch so EVERY command's
	// failed bd calls are recorded, independent of WYK_DEBUG
	// (would-you-kindly-w5bf.6).
	beads.ErrorSink = recordBDFailure
	if len(os.Args) >= 2 {
		if run, ok := subcommandHandlers[os.Args[1]]; ok {
			exitWith(run(os.Args[2:]))
		}
		switch os.Args[1] {
		case "--version", "-v":
			exitWith(runVersion(os.Args[2:]))
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
		exitWith(64)
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
		exitWith(64)
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
		exitWith(1)
	}

	if *probe {
		exitWith(runProbe(src))
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

	// Update-check surfaces — the cached nudge banner here and the
	// background refresh below — are suppressed entirely when the user
	// set disable_update_check in config.json (read once at the top of
	// main; a broken config falls back to checks-on, the historical
	// default).
	updateChecks := !cfg.DisableUpdateCheck

	// Read the cached update nudge once at startup so the banner
	// can render immediately if there's already a snapshot on
	// disk. The background goroutine below refreshes it for the
	// next run.
	if updateChecks {
		if nudge := readUpdateNudge(versionString()); nudge != "" {
			model = model.WithUpdateNudge(nudge)
		}
	}

	// Warm-start: seed m.all from the last-saved fetch so the
	// first frame paints rows instead of the empty "loading…"
	// stand-in. The live fetch dispatched by Init still runs in
	// parallel; the cached rows are replaced when it returns.
	// Best-effort: any cache failure (missing file, stale TTL,
	// unsupported schema) silently falls back to the cold path.
	// The snapshot is scoped to the workspaces this launch was built
	// against (registry or -C path): a cache written for a different
	// set is ignored rather than painted (would-you-kindly-mup1).
	if cachePath, err := tui.CacheDefaultPath(); err == nil {
		cache, _ := tui.LoadCache(cachePath)
		model = model.WithCacheScope(tui.CacheScope(repoPaths)).
			WithCacheSnapshot(cache, cachePath)
	}
	// No mouse capture: with mouse reporting off, the host terminal
	// handles native click-drag text selection in every view, so users
	// can select and copy the list columns (and detail body). The cost
	// is wheel-scroll / click-to-set-cursor — navigation is keyboard
	// only (j/k, PgUp/PgDn, g/G). Selecting text over the CLI was the
	// repeated ask; mouse nav had keyboard equivalents.
	// Debug logging is set up once at the top of main (setupDebugLogging),
	// before subcommand dispatch, so it covers every command — not just
	// this TUI path (would-you-kindly-w5bf.1).
	// Mouse capture: the startup state rides a Program option (which
	// also latches SGR coordinate encoding); runtime view-switching
	// goes through the late-bound ProgramMouse controller — direct
	// program calls, never tea.Cmds, which get delayed/dropped when
	// batched (the PR #24 live finding; would-you-kindly-5i0e).
	pm := &tui.ProgramMouse{}
	model = model.WithMouseController(pm)
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if model.StartWithMouseCapture() {
		opts = append(opts, tea.WithMouseCellMotion())
	} else {
		// Latch SGR mouse encoding (1006) even when starting
		// released: bubbletea's runtime Program.EnableMouseCellMotion
		// — the re-capture path ProgramMouse uses — writes only the
		// tracking mode (1002h), and without 1006 the terminal falls
		// back to legacy X10 encoding, which cannot report
		// coordinates past column/row 223 (roborev #2111). The
		// captured-start path latches 1006 via the program option;
		// this covers the released start. SGR alone is inert — no
		// events are reported until tracking is enabled — and tea's
		// exit teardown clears it.
		fmt.Print("\x1b[?1006h")
	}
	p := tea.NewProgram(model, opts...)
	pm.SetProgram(p)
	// Kick a best-effort live check in the background. We don't
	// post the result back into the running TUI — the snapshot
	// lands on disk and the NEXT wyk invocation reads it. This
	// keeps the TUI hot path free of network I/O entirely. Skipped
	// when disable_update_check is set.
	if updateChecks {
		go backgroundUpdateCheck()
	}
	if _, err := p.Run(); err != nil {
		// Bubble Tea caught a panic (it has already restored the terminal
		// and printed the stack); persist a crash record so there's a
		// durable artifact too. The returned error carries only the
		// sentinel, not the stack, so the on-screen trace is the fuller one.
		if errors.Is(err, tea.ErrProgramPanic) {
			if path := writeCrashRecord("tui", err, nil); path != "" {
				fmt.Fprintf(os.Stderr, "wyk: TUI panic recorded to %s\n", path)
			}
		}
		fmt.Fprintln(os.Stderr, "wyk:", err)
		// The TUI needs a terminal; when Bubble Tea can't open one
		// (CI, cron, a pipe with no controlling tty) point at the
		// non-TTY entry point instead of leaving a bare open error.
		if isTTYOpenErr(err) {
			fmt.Fprintln(os.Stderr, "wyk: the TUI needs a terminal — for scripts/CI use `wyk --probe` (or `wyk inbox -json`)")
		}
		exitWith(1)
	}
}

// isTTYOpenErr reports whether err looks like Bubble Tea failing to
// open a terminal (v1.3.10 words it "could not open a new TTY:
// open /dev/tty: …"). Two case-insensitive prongs — the outer
// message and the wrapped /dev/tty path — so a bubbletea upgrade
// that rewords one doesn't silently drop the --probe hint, while an
// unrelated error that merely contains "tty" (pretty, getty, a user
// path) doesn't trigger a misleading "needs a terminal" hint.
func isTTYOpenErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "open a new tty") || strings.Contains(s, "/dev/tty")
}

// stateFilePath resolves a wyk state-file location (logs are state, not
// config/cache): XDG_STATE_HOME/wyk/<name> first, then ~/.local/state/
// wyk/<name>, then a cwd "wyk-<name>" fallback if home is unresolvable.
func stateFilePath(name string) string {
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(dir, "wyk", name)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "wyk", name)
	}
	return "wyk-" + name
}

// crashLogPath is the always-on crash log (panic records), independent of
// the debug log so a panic is captured even with WYK_DEBUG off.
func crashLogPath() string { return stateFilePath("crash.log") }

// errorLogPath is the always-on bd-failure log: every failed bd
// invocation is appended here regardless of WYK_DEBUG (would-you-kindly-w5bf.6).
func errorLogPath() string { return stateFilePath("bd-errors.log") }

// recordBDFailure is wired into beads.ErrorSink so every failed bd call
// leaves a one-line record (timestamp, argv, dir, classified sentinel,
// error) in the always-on, size-bounded error log — best-effort, never
// blocking the caller on an I/O hiccup.
func recordBDFailure(args []string, dir string, err error) {
	path := errorLogPath()
	if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
		return
	}
	rotateLogIfLarge(path)
	f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if e != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sentinel := bdSentinelName(err)
	// Redact user-supplied argv values: this log is always-on (independent
	// of WYK_DEBUG), so a failed `bd create --title=<secret>` must not
	// persist that content to a long-lived plaintext file. The full argv
	// stays available only in the opt-in debug trace.
	argv := redactBDArgs(args)
	fmt.Fprintf(f, "%s\tbd %s\tdir=%q\tsentinel=%s\terr=%v\n",
		time.Now().Format(time.RFC3339), argv, dir, sentinel, err)
	// Also surface on the structured stream when logging is active (w5bf.4);
	// gated on Active so a failure isn't echoed to stderr when logging is off.
	if wyklog.Active() {
		slog.Error("bd failure", "argv", argv, "dir", dir, "sentinel", sentinel, "err", err)
	}
}

// redactBDArgs renders a bd argv for the always-on error log with all
// user-supplied VALUES stripped: only the subcommand verb (args[0]) and
// flag NAMES survive, e.g. ["create","--title=secret","--notes","x","-a","al"]
// → "create --title=<redacted> --notes <redacted> -a <redacted>". Keeps
// enough to see which operation + flags failed without leaking issue text.
func redactBDArgs(args []string) string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		switch {
		case i == 0:
			out = append(out, a)
		case looksLikeFlag(a):
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				out = append(out, a[:eq]+"=<redacted>")
			} else {
				out = append(out, a)
			}
		default:
			out = append(out, "<redacted>")
		}
	}
	return strings.Join(out, " ")
}

// looksLikeFlag reports whether a is shaped like a flag NAME (^--?[A-Za-z])
// rather than a value. Requiring a letter after the leading dash(es) means
// a dash-prefixed VALUE — a markdown bullet or dash-led title/note like
// "- fixes the crash" — is NOT mistaken for a flag and so gets redacted
// instead of leaking into the always-on error log (roborev on w5bf.6).
func looksLikeFlag(a string) bool {
	rest := strings.TrimLeft(a, "-")
	dashes := len(a) - len(rest)
	if dashes < 1 || dashes > 2 || rest == "" {
		return false
	}
	c := rest[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// bdSentinelName classifies a bd error for the error log so a reader can
// scan for timeouts / missing-workspace / missing-binary without parsing
// the free-text message.
func bdSentinelName(err error) string {
	switch {
	case errors.Is(err, beads.ErrTimedOut):
		return "timed-out"
	case errors.Is(err, beads.ErrNoWorkspace):
		return "no-workspace"
	case errors.Is(err, beads.ErrBDNotFound):
		return "bd-not-found"
	default:
		return "-"
	}
}

// writeCrashRecord appends a timestamped panic record (wyk version, argv,
// the panic value, and the stack when available) to the crash log and
// returns the path written, or "" on failure. source distinguishes the
// main-goroutine recover ("main") from the Bubble Tea path ("tui"), whose
// stack is printed to the terminal by the renderer rather than passed here.
func writeCrashRecord(source string, r any, stack []byte) string {
	path := crashLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ""
	}
	rotateLogIfLarge(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, "\n===== wyk %s panic %s =====\nversion: %s\nargs: %v\npanic: %v\n",
		source, time.Now().Format(time.RFC3339), versionString(), os.Args, r)
	if len(stack) > 0 {
		fmt.Fprintf(f, "\n%s\n", stack)
	} else {
		fmt.Fprintln(f, "(stack printed to the terminal by the TUI renderer)")
	}
	// Mirror onto the structured stream when logging is active (w5bf.4).
	if wyklog.Active() {
		slog.Error("panic", "source", source, "panic", fmt.Sprint(r))
	}
	return path
}

// captureCrash is main's deferred recover: it persists the panic + stack
// to the crash log, prints them to stderr, and exits non-zero. Re-raising
// would re-print the stack but bury our friendly "recorded to <path>"
// line, so we own the exit here. Only fires for panics that unwind the
// main goroutine — Bubble Tea handles its own (see p.Run above).
func captureCrash() {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	path := writeCrashRecord("main", r, stack)
	if path != "" {
		fmt.Fprintf(os.Stderr, "wyk: panic recorded to %s\n", path)
	}
	fmt.Fprintf(os.Stderr, "wyk: panic: %v\n\n%s\n", r, stack)
	os.Exit(1)
}

// debugLogCleanup flushes and closes the debug log file when debug
// logging is enabled; nil otherwise. setupDebugLogging assigns it. The
// subcommand-dispatch paths os.Exit (which skips deferred cleanup), so
// they call it via exitWith; the TUI path relies on the deferred call in
// main.
var debugLogCleanup func()

// setupDebugLogging wires WYK_DEBUG / WYK_LOG_FILE for the WHOLE process.
// When a log path resolves it installs a slog sink on that file (see
// internal/wyklog) at the level from WYK_LOG_LEVEL (default Debug = the
// full bd-call trace) and also points the stdlib logger there for Bubble
// Tea's own output. Off by default: no file is opened, wyklog stays
// inactive, and slog's default (Info) leaves the Debug trace disabled —
// zero overhead. Called before dispatch so non-TUI subcommands (inbox,
// stats, export, hook, …) trace their bd calls too, not just the TUI
// (would-you-kindly-w5bf.1, w5bf.4).
func setupDebugLogging() {
	logPath := debugLogPath()
	if logPath == "" {
		return
	}
	// Bound the file at startup (per-launch): rotate before opening so a
	// chatty session doesn't reopen an already-huge log. Not re-checked
	// within a session — the handle stays open — so a single long-running
	// session can still grow past the cap; the bound is across launches
	// (would-you-kindly-w5bf.5).
	rotateLogIfLarge(logPath)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wyk: could not open debug log %q: %v\n", logPath, err)
		return
	}
	debugLogCleanup = func() { _ = f.Close() }
	// Write the startup banner directly to the file BEFORE installing the
	// leveled handler, so the version / log-path header anchoring the trace
	// is always present even at WYK_LOG_LEVEL=error/warn (roborev: a banner
	// emitted via slog.Info would be filtered out at those levels).
	fmt.Fprintf(f, "wyk %s starting; debug logging enabled (%s)\n", versionString(), logPath)
	// Install slog as the structured sink (would-you-kindly-w5bf.4); the bd
	// trace, bd-failure, and crash records all flow through it at a level.
	level := wyklog.ParseLevel(os.Getenv("WYK_LOG_LEVEL"), slog.LevelDebug)
	wyklog.Setup(f, level)
	// Also point the stdlib logger at the file so Bubble Tea's internal
	// log output (which uses the standard logger) is captured alongside.
	log.SetOutput(f)
	slog.Info("wyk starting", "version", versionString(), "log", logPath, "level", level.String())
}

// defaultLogMaxBytes caps the debug and crash logs. Past this size the
// active file is rotated to a single ".1" backup, so disk usage stays
// bounded at ~2x the cap. Override with WYK_LOG_MAX_BYTES (bytes).
const defaultLogMaxBytes = 10 << 20 // 10 MiB

// logMaxBytes is the effective rotation threshold: WYK_LOG_MAX_BYTES when
// it parses to a positive integer, else the built-in default. A knob is
// handy both for users on tight disks and for exercising rotation in tests.
func logMaxBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("WYK_LOG_MAX_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultLogMaxBytes
}

// rotateLogIfLarge renames path to path+".1" (replacing any prior backup)
// once it reaches the size cap, so the active log reopens empty and disk
// stays bounded at ~2x the cap — current file plus one rotated generation.
// Best-effort: a missing file or any stat/rename error leaves things as-is.
func rotateLogIfLarge(path string) {
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < logMaxBytes() {
		return
	}
	_ = os.Rename(path, path+".1")
}

// exitWith flushes the debug log (if any) before os.Exit, since os.Exit
// skips deferred cleanup. The single exit point for the dispatch paths.
func exitWith(code int) {
	if debugLogCleanup != nil {
		debugLogCleanup()
	}
	os.Exit(code)
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

// validateDashC stats a -C directory up front so a bad path produces
// a clean one-liner instead of bd's raw JSON error blob surfacing
// through a failed query ("bd query …: { \"error\": … }"). A common
// typo deserves a one-liner, not a stack of escaped JSON. Shared by
// the TUI/--probe path (buildSource), the multi-repo subcommands
// (reposToQuery), and wyk handoff.
func validateDashC(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("-C directory %q does not exist", dir)
		}
		return fmt.Errorf("-C directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("-C %q is not a directory", dir)
	}
	return nil
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
		if err := validateDashC(dir); err != nil {
			return nil, nil, "", err
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
	fs.Usage = subcommandUsage(fs, "handoff")
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
		"print the required 3-section runbook skeleton to stdout and exit (no bd writes); pipe it into your editor, fill it in, then run: wyk handoff <id> < filled.md")
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

	// Validate -C like the other arg checks below: a typo'd path is a
	// usage error caught here, not bd argv noise from the first write.
	if *dir != "" {
		if err := validateDashC(*dir); err != nil {
			fmt.Fprintln(os.Stderr, "wyk handoff:", err)
			return 64
		}
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
		// Single-sourced from cliSubcommandDocs — a hand-written copy
		// here was the third place the handoff usage lived, free to
		// drift from -h and cli.md (roborev #2060). Nil-guarded so a
		// renamed doc entry degrades to a terse line instead of a
		// panic on the error path (roborev #2063); the test pins the
		// entry's existence.
		if doc := findCLIDoc("handoff"); doc != nil {
			fmt.Fprintln(os.Stderr, "usage: "+doc.Usage)
		} else {
			fmt.Fprintln(os.Stderr, "usage: wyk handoff [flags] <issue-id>")
		}
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

	// Reading from a terminal would block waiting for user input —
	// easy to hit by accident when invoked interactively without a
	// redirect. If the user then closes stdin with ^D, we'd silently
	// wipe the issue's description. Refuse unless they opted in.
	// stdinIsTerminal is a real TTY test; the non-terminal read below
	// is deadline-bounded, so neither arm can hang a non-interactive
	// caller (would-you-kindly-l51f).
	if *file == "" && !*allowEmpty && stdinIsTerminal() {
		fmt.Fprintln(os.Stderr,
			"wyk handoff: stdin is a terminal. Pipe a runbook in, pass -file <path>, or use -allow-empty to deliberately clear the description.")
		return 64
	}

	var runbookBytes []byte
	var err error
	if *file != "" {
		runbookBytes, err = os.ReadFile(*file)
	} else {
		runbookBytes, err = readStdinRunbook()
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
	createLabels := []string{handoff.SrcAgentLabel}
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

	// A handoff into an unregistered workspace never reaches a human:
	// `wyk inbox`, `wyk dashboard`, and the TUI all read the registry, so
	// the issue is correctly labelled and completely invisible — the exact
	// failure that lost a P0 (would-you-kindly-afo3). Register the
	// workspace first, so the notice precedes the created/handed lines and
	// still lands if the bd writes below fail. Best-effort: it never
	// changes this command's exit code. Deliberately after -dry-run's
	// return above — a dry run performs no writes of any kind.
	maybeAutoRegister("wyk handoff", *dir, os.Stderr)

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

// injectedVersion is stamped at link time by goreleaser via
// `-ldflags "-X main.injectedVersion=vX.Y.Z"`. goreleaser builds with
// `go build` from a clean checkout, so debug.ReadBuildInfo reports
// Main.Version as "(devel)" — the tag never reaches the binary on its
// own. When set, it takes precedence so prebuilt-binary and Homebrew
// installs carry their real tag. Empty for go install / go build, which
// already get an honest version from build info, so this changes nothing
// for those paths.
var injectedVersion string

// versionString returns the human-readable version line printed by
// `wyk --version`. Prefers the link-time injectedVersion (goreleaser
// builds); otherwise pulls from Go's build info so module-installed
// builds (go install ...@vX.Y.Z) carry their tag; source-tree
// builds (go build, go run) report "(devel)" — which is honest:
// they don't HAVE a tag. Includes the commit SHA and dirty marker
// when present in the build info's VCS stamps. No hand-maintained
// const to drift.
func versionString() string {
	const name = "wyk"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		if injectedVersion != "" {
			return name + " " + injectedVersion
		}
		return name + " (unknown — build info missing)"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	if injectedVersion != "" {
		v = injectedVersion
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
  bugreport    one-shot pasteable capture (version, env, doctor, config, logs)
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
  config       get/set machine-wide settings (e.g. default_scope: all|cwd)
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
