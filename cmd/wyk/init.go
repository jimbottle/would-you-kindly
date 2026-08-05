package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// postCommitHook is the shell script `wyk init` installs at
// .git/hooks/post-commit. It defers all the real logic to
// `wyk hook post-commit` so the parsing and bd-close behavior can
// be updated by upgrading the wyk binary alone — no need to
// reinstall the hook.
//
// `exec` replaces the shell process so the user sees wyk's output
// without an extra layer. `wyk` must be on PATH at commit time; if
// it isn't, git prints a clear error from the exec.
const postCommitHook = `#!/bin/sh
# Installed by ` + "`wyk init`" + `. Auto-closes bd issues referenced in
# the commit message (Closes: <id> / Fixes: <id> / Resolves: <id>).
#
# To uninstall: rm "$0"
exec wyk hook post-commit
`

// chainedPostCommitHook wraps a pre-existing post-commit hook
// alongside wyk's. The original is preserved at post-commit.pre-wyk
// and invoked first; wyk's logic runs after via exec, so its output
// reaches the user without an extra shell layer. The pre-wyk hook's
// exit code is intentionally NOT checked — wyk's auto-close shouldn't
// be blocked by an unrelated tool's hiccup, and vice versa.
const chainedPostCommitHook = `#!/bin/sh
# Installed by ` + "`wyk init -chain`" + `. Runs the pre-existing
# post-commit hook (preserved at post-commit.pre-wyk) THEN wyk's
# auto-close logic.
#
# To uninstall: rm "$0" and (optionally) restore .pre-wyk to post-commit.
PREWYK="$(dirname "$0")/post-commit.pre-wyk"
if [ -x "$PREWYK" ]; then
    "$PREWYK" "$@"
fi
exec wyk hook post-commit
`

// hookMarker identifies any wyk-installed hook (plain or chained).
// Either variant of the script contains this substring so the
// re-run detection works for both.
const hookMarker = "Installed by `wyk init"

// chainedHookMarker identifies the chained variant specifically —
// the wrapper that runs a preserved foreign hook (post-commit.pre-wyk)
// before exec'ing `wyk hook post-commit`. Used by `wyk doctor` to
// tell plain vs chained installs apart without ad-hoc substring
// matching that could false-PASS on foreign hooks mentioning "wyk".
const chainedHookMarker = "Installed by `wyk init -chain`"

// printInitUsage renders `wyk init -h`, leading with the bare bootstrap
// (the case ~everyone wants) before the alternate modes and the flag
// table (would-you-kindly-5rgd).
func printInitUsage(fs *flag.FlagSet) {
	fmt.Fprint(os.Stderr, `wyk init — bootstrap this repo for wyk

Common case:
  wyk init                run "bd init" if needed, install the post-commit
                          auto-close hook, register the repo, and seed the
                          agent enrichment (CLAUDE.md + bd-create-guard).
                          Idempotent — safe to re-run.

Preview / opt out of pieces:
  wyk init -dry-run           show what would change, write nothing
  wyk init -skip-claude-md    skip the CLAUDE.md / .claude/settings.json edits
  wyk init -skip-hook         register + enrich only; don't touch git hooks
  wyk init -chain             keep an existing post-commit hook, chain wyk after it
  wyk init -force             overwrite an existing post-commit hook (destructive)

Other modes (not the per-repo bootstrap):
  wyk init -scan <root>       register every bd workspace found under <root>
  wyk init -uninstall         remove wyk's hook from this repo
  wyk init -fix-foreign-hooks chain wyk after foreign hooks across the registry
  wyk init -skills            install wyk's agent skills into ~/.claude/skills

Flags:
`)
	fs.PrintDefaults()
}

// runInit implements `wyk init`: a one-stop bootstrap for using wyk
// in a repo. It (1) initialises a bd workspace if none exists,
// (2) installs the post-commit auto-close hook, and (3) registers
// the repo in ~/.config/wyk/repos.json so the multi-repo TUI sees
// it. Each step is independently idempotent — re-running on a
// fully-set-up repo is a no-op with status messages.
//
// Registration is deliberately NOT gated on the hook install: a foreign
// hook (or a broken core.hooksPath) makes wyk decline to write a hook,
// but it says nothing about whether the repo should be visible to the
// multi-repo views. It used to abort the whole run at exit 64, leaving
// the repo unregistered — so `wyk handoff` in that repo succeeded while
// `wyk inbox` / the dashboard / the TUI could never show the result, and
// a P1 handoff sat invisible for days (would-you-kindly-7kly). The hook
// step now runs LAST and its refusals are warnings.
//
// Exit codes:
//
//	0   installed / already installed / hook declined with a warning
//	    (or, with -dry-run, would have)
//	1   filesystem, git, or bd error
//	2   .git directory missing — not a git repo
//	64  usage error, or an explicit -chain that can't proceed (the
//	    .pre-wyk slot is occupied). Registration has already happened
//	    by then.
//
// perRepoInitFlags names the flags that only mean something on the
// per-repo install path, so every alternate mode (-scan, -uninstall,
// -fix-foreign-hooks) must reject all of them.
//
// It exists as ONE list because it used to be three hand-copied switch
// statements, and `-skills` — added later — was missed by all three:
// `wyk init -uninstall -skills` parsed fine and silently did nothing
// with the flag (would-you-kindly-6gjb). A new per-repo flag now has a
// single place to be registered.
var perRepoInitFlags = []string{
	"force", "chain", "skip-bd-init", "skip-register", "skip-claude-md", "skip-hook", "skills",
}

// setIncompatibleFlags returns the "-name" forms of the per-repo-only
// flags (plus any alsoBad, for the alternate modes that exclude each
// other) that the user ACTUALLY set. fs.Visit walks only the flags
// present on the command line, so a flag left at its default never
// triggers the error, and it walks them lexicographically, so the
// message is deterministic.
func setIncompatibleFlags(fs *flag.FlagSet, alsoBad ...string) []string {
	reject := make(map[string]bool, len(perRepoInitFlags)+len(alsoBad))
	for _, n := range perRepoInitFlags {
		reject[n] = true
	}
	for _, n := range alsoBad {
		reject[n] = true
	}
	var bad []string
	fs.Visit(func(f *flag.Flag) {
		if reject[f.Name] {
			bad = append(bad, "-"+f.Name)
		}
	})
	return bad
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing post-commit hook (destructive — drops the existing hook entirely)")
	chain := fs.Bool("chain", false, "preserve an existing post-commit hook and chain wyk's logic after it (preferred over -force when the existing hook is from another tool like roborev)")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing the hook")
	skipBD := fs.Bool("skip-bd-init", false, "do not run 'bd init' even if .beads is missing")
	skipRegister := fs.Bool("skip-register", false, "do not add this repo to ~/.config/wyk/repos.json")
	skipClaudeMD := fs.Bool("skip-claude-md", false, "do not seed the agent enrichment: wyk's conventions block in CLAUDE.md AND the bd-create-guard PreToolUse hook in .claude/settings.json (which redirects 'bd create' to 'wyk create')")
	skipHook := fs.Bool("skip-hook", false, "do not touch git hooks at all — register and enrich only. Use when another tool owns post-commit and you don't want wyk's auto-close (commits with 'Closes: <id>' then won't close anything)")
	scanRoot := fs.String("scan", "", "scan this directory tree for existing bd workspaces and register every one found (skips repos already registered, hidden dirs, node_modules, vendor); mutually exclusive with the per-repo init path")
	uninstall := fs.Bool("uninstall", false, "remove wyk's post-commit hook (restoring post-commit.pre-wyk if present); refuses on foreign hooks")
	fixForeignHooks := fs.Bool("fix-foreign-hooks", false, "scan the registered repos for foreign post-commit hooks and chain wyk after each (idempotent; wyk-installed and missing hooks are left alone)")
	installSkills := fs.Bool("skills", false, "also install wyk's agent skills into ~/.claude/skills (idempotent; like 'wyk skills install'). Modified skills are left alone.")
	fs.SetOutput(os.Stderr)
	// Lead the help with the bare happy path so the common case isn't
	// buried under the alternate modes and the alphabetical flag dump
	// (would-you-kindly-5rgd). The alternate modes (-scan / -uninstall /
	// -fix-foreign-hooks) would read more naturally as subcommands; that
	// is a deliberate post-1.0 change (it reshapes the CLI surface), so
	// for now they stay flags but are grouped distinctly here.
	fs.Usage = func() { printInitUsage(fs) }
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 0 {
		// Sourced from cliSubcommandDocs like every other subcommand. The
		// four hand-written lines this replaces had drifted exactly the way
		// usageLine exists to prevent: they omitted -skills — the same flag
		// whose omission from three copied switch statements motivated
		// perRepoInitFlags (roborev #3045).
		fmt.Fprintln(os.Stderr, "usage: "+usageLine("init"))
		return 64
	}
	if *force && *chain {
		fmt.Fprintln(os.Stderr, "wyk init: -force and -chain are mutually exclusive")
		return 64
	}
	// -skip-hook says "don't touch hooks"; -force / -chain say "touch them
	// this specific way". Silently honouring one over the other would leave
	// the user guessing which won.
	if *skipHook && (*force || *chain) {
		which := "-force"
		if *chain {
			which = "-chain"
		}
		fmt.Fprintf(os.Stderr, "wyk init: -skip-hook and %s are mutually exclusive\n", which)
		return 64
	}
	if *fixForeignHooks {
		// -fix-foreign-hooks is a registry-wide alternate mode; reject
		// combinations that only make sense in the per-repo install
		// path so the user gets a clear error rather than silent
		// ignores. (-scan and -uninstall are the other alternate modes.)
		bad := setIncompatibleFlags(fs, "scan", "uninstall")
		if len(bad) > 0 {
			fmt.Fprintf(os.Stderr,
				"wyk init: -fix-foreign-hooks is incompatible with %s\n",
				strings.Join(bad, ", "))
			return 64
		}
		return runFixForeignHooks(*dryRun)
	}
	if *uninstall {
		// -uninstall is the inverse path; reject combinations that
		// only make sense in the install direction so the user gets
		// a clear "either X or Y, not both" instead of silent ignores.
		bad := setIncompatibleFlags(fs, "scan")
		if len(bad) > 0 {
			fmt.Fprintf(os.Stderr,
				"wyk init: -uninstall is incompatible with %s\n",
				strings.Join(bad, ", "))
			return 64
		}
		return runUninstall(*dryRun)
	}

	// -scan short-circuits the per-repo init path; it only registers.
	// Reject flag combinations that don't make sense with -scan so
	// the user gets a clear error instead of silently-ignored options.
	if *scanRoot != "" {
		bad := setIncompatibleFlags(fs)
		if len(bad) > 0 {
			fmt.Fprintf(os.Stderr,
				"wyk init: -scan is incompatible with %s (the scan path only registers; per-repo flags apply to per-repo init)\n",
				strings.Join(bad, ", "))
			return 64
		}
		return runScanAndRegister(*scanRoot, *dryRun)
	}

	_, repoRoot, err := findGitPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
	}

	// The hook path is resolved once, in the hook step at the bottom.
	// It used to be resolved up front too, purely so an out-of-repo
	// core.hooksPath could abort the run before anything was mutated —
	// but aborting is exactly the behaviour that left repos unregistered
	// and handoffs invisible (would-you-kindly-7kly). Nothing needs
	// protecting from a hook problem any more, so the early probe (and
	// its duplicate warning) is gone.

	// Step 1: bootstrap a bd workspace if there isn't one.
	if !*skipBD {
		beadsDir := filepath.Join(repoRoot, ".beads")
		if _, err := os.Stat(beadsDir); errors.Is(err, os.ErrNotExist) {
			if *dryRun {
				fmt.Println("wyk init: would run `bd init` (no .beads directory present)")
			} else {
				if code := runBDInit(repoRoot); code != 0 {
					return code
				}
			}
		} else if err == nil {
			if *dryRun {
				fmt.Println("wyk init: bd workspace already present, skipping bd init")
			}
		} else {
			fmt.Fprintln(os.Stderr, "wyk init: stat .beads:", err)
			return 1
		}

		// Step 1.5: teach the project's bd workspace about wyk's
		// labels. `bd remember` persists a memory that `bd prime`
		// surfaces at the start of every agent session — so the
		// next agent that opens this repo sees the human/src:agent
		// convention without having to read source comments or
		// docs/CONTRACT.md. Idempotent via --key. Best-effort:
		// bd remember failure is WARNed to stderr but doesn't gate
		// the rest of init (the hook install is the load-bearing
		// part).
		if *dryRun {
			fmt.Println("wyk init: would store handoff convention via `bd remember` (visible to agents via `bd prime`)")
		} else {
			if err := teachBDConvention(repoRoot); err != nil {
				fmt.Fprintln(os.Stderr, "wyk init: bd remember (handoff convention) failed:", err)
				fmt.Fprintln(os.Stderr, "wyk init: continuing — this enrichment is best-effort, the post-commit hook is the load-bearing install step")
			} else {
				fmt.Println("wyk init: stored handoff convention via `bd remember` (visible to agents via `bd prime`)")
			}
		}
	}

	// Step 1.6: seed wyk's conventions into the repo's CLAUDE.md so the
	// next AGENT that opens this repo is wyk-aware, not just bd-aware.
	// Without this, "build the plan in wyk" is a no-op — there is no
	// `wyk create`, so an agent with only bd boilerplate has no local
	// definition mapping that phrase to `bd create` issues. The block is
	// marker-delimited and refreshed in place, so re-running init keeps
	// it current. Best-effort: a failure WARNs but doesn't gate the
	// load-bearing hook install.
	//
	// enrichmentOK tracks whether BOTH halves landed, so the hook-decline
	// footer can list the enrichment among the things this run set up only
	// when it actually did. Both are best-effort, so either failing leaves
	// it false without stopping init.
	enrichmentOK := false
	if !*skipClaudeMD {
		enrichmentOK = true
		action, err := seedWykConventions(repoRoot, *dryRun)
		if err != nil {
			enrichmentOK = false
			fmt.Fprintln(os.Stderr, "wyk init: CLAUDE.md:", err)
			fmt.Fprintln(os.Stderr, "wyk init: continuing — this enrichment is best-effort, the post-commit hook is the load-bearing install step")
		} else {
			fmt.Println("wyk init:", action)
		}
		// Step 1.7: register the bd-create-guard PreToolUse hook so an
		// agent that runs `bd create` is redirected to `wyk create` (which
		// records the session). Enforces the convention at the harness
		// level rather than leaving it to docs an agent can skip.
		if sAction, sErr := seedClaudeSettings(repoRoot, *dryRun); sErr != nil {
			enrichmentOK = false
			fmt.Fprintln(os.Stderr, "wyk init: .claude/settings.json:", sErr)
			fmt.Fprintln(os.Stderr, "wyk init: continuing — best-effort enrichment")
		} else {
			fmt.Println("wyk init:", sAction)
		}
	}

	// Step 2: register the repo so wyk's multi-repo views find it.
	//
	// This runs BEFORE the hook step, and that ordering is the whole fix
	// for would-you-kindly-7kly. Registration is what makes a repo visible
	// to `wyk inbox`, `wyk dashboard`, `wyk stats` and the TUI; the hook is
	// an optional auto-close convenience. When registration sat downstream
	// of the hook step, a single foreign post-commit hook aborted init at
	// exit 64 and the repo stayed invisible — so agents handed work to a
	// human, saw "handed <id> to human", and the human structurally could
	// not receive it. Nothing about the hook decides whether this repo
	// should be listed, so nothing about the hook may gate the listing.
	if !*skipRegister {
		if *dryRun {
			// Preview must match what the real run would print —
			// distinguish "already registered" from "would register"
			// so the dry-run is genuinely observational.
			previewRegister(repoRoot)
		} else if code := registerRepo(repoRoot); code != 0 {
			return code
		}
	}

	// What the hook-decline notices are allowed to say about this repo's
	// visibility. Reaching here without -skip-register means registerRepo
	// returned 0 (a failure returns above), so the repo is in the registry.
	// With the flag, this run registered nothing — but the repo may well
	// already BE registered, and `wyk doctor -fix` passes -skip-register
	// for repos it read straight out of the registry. Keying the claim off
	// the flag would tell exactly those users their registered repo is
	// invisible, so ask the registry instead of the flag.
	visibility := regVisible
	switch {
	case *skipRegister:
		visibility = repoRegistryVisibility(repoRoot)
	case *dryRun:
		visibility = regWillRegister
	}

	// What the footer may name as established. The flags say what a REAL
	// run does, which is the right answer for the "Would set up:" preview
	// branch but not for the past-tense one.
	bdWorkspace := !*skipBD || beadsWorkspaceExists(repoRoot)
	if *dryRun && *skipRegister {
		// The one combination that writes nothing yet still lands in the
		// past-tense branch: -skip-register means visibility comes from
		// the registry, so an already-registered repo reads "Set up: …"
		// for a run that created none of it. Ask the disk instead of the
		// flags — BOTH components, not just the workspace: a repo whose
		// enrichment is genuinely in place shouldn't be reported as
		// lacking it any more than one whose .beads is.
		bdWorkspace = beadsWorkspaceExists(repoRoot)
		enrichmentOK = wykEnrichmentPresent(repoRoot)
	}

	// Step 3: install the post-commit hook (unless -skip-hook). Last,
	// and non-fatal by default: see installPostCommitHook.
	hookCode := 0
	if *skipHook {
		fmt.Println("wyk init: skipping the post-commit hook (-skip-hook); `Closes: <id>` won't auto-close here")
	} else {
		hookCode = installPostCommitHook(repoRoot, hookInstallOpts{
			dryRun:      *dryRun,
			force:       *force,
			chain:       *chain,
			visibility:  visibility,
			bdWorkspace: bdWorkspace,
			enrichment:  enrichmentOK,
		})
	}

	// Step 4 (opt-in): install wyk's agent skills into ~/.claude/skills.
	// User-global, so this is a one-time convenience offered during
	// per-repo setup; idempotent and leaves modified skills alone.
	if *installSkills {
		dir, derr := userSkillsDir()
		if derr != nil {
			fmt.Fprintln(os.Stderr, "wyk init: skills:", derr)
			return 1
		}
		written, werr := installMissingSkills(dir, *dryRun)
		if werr != nil {
			fmt.Fprintln(os.Stderr, "wyk init: skills:", werr)
			return 1
		}
		switch {
		case len(written) == 0:
			fmt.Printf("wyk init: agent skills already installed in %s\n", dir)
		case *dryRun:
			fmt.Printf("wyk init: would install %d skill(s) to %s: %s\n", len(written), dir, strings.Join(written, ", "))
		default:
			fmt.Printf("wyk init: installed %d skill(s) to %s: %s\n", len(written), dir, strings.Join(written, ", "))
		}
	}
	// The hook step's exit code is reported here rather than returned
	// inline, so a hook problem never skips the skills install either.
	return hookCode
}

// regVisibility is what a hook-decline notice is allowed to say about
// whether this repo reaches `wyk inbox`, the dashboard and the TUI.
//
// It is deliberately NOT a bool derived from -skip-register. That flag
// describes what this RUN did; the notice needs to describe what the
// REGISTRY holds, and the two differ on the most common path there is —
// `wyk doctor -fix` passes -skip-register for repos it just read out of
// the registry.
type regVisibility int

const (
	// regVisible: the repo is in the registry, whether this run put it
	// there or it was already.
	regVisible regVisibility = iota
	// regWillRegister: a dry run that would register it.
	regWillRegister
	// regAbsent: the registry was readable and this repo is not in it.
	regAbsent
	// regUnknown: the registry couldn't be read, so neither claim is
	// safe to make.
	regUnknown
)

// repoRegistryVisibility asks the registry whether repoRoot is already
// listed. Called only when -skip-register means this run registered
// nothing and the answer can't be inferred from the flag.
func repoRegistryVisibility(repoRoot string) regVisibility {
	path, err := registry.DefaultPath()
	if err != nil {
		return regUnknown
	}
	reg, err := registry.Load(path)
	if err != nil {
		return regUnknown
	}
	if reg.Has(repoRoot) {
		return regVisible
	}
	return regAbsent
}

// hookInstallOpts carries the per-run state the hook step needs. It's a
// struct rather than positional args because `visibility` is easy to
// transpose with the bools, and a wrong value there makes wyk lie to the
// user about whether their repo is reachable.
type hookInstallOpts struct {
	dryRun bool
	force  bool
	chain  bool
	// visibility gates what the decline notices may claim about this
	// repo reaching the multi-repo views.
	visibility regVisibility
	// bdWorkspace / enrichment gate the other two things the decline
	// footer used to assert unconditionally. Each is listed only when
	// this run established it.
	bdWorkspace bool
	enrichment  bool
}

// beadsWorkspaceExists reports whether repoRoot holds a .beads directory.
// Used to decide whether the decline footer may list the bd workspace
// among the things that are set up when init didn't establish one itself
// — either -skip-bd-init meant it never looked, or -dry-run meant it only
// previewed. Claiming it on the flag alone would vouch for a workspace
// that may have been deleted, which is itself a way handoffs go missing
// (the TUI reports such a repo as a per-sub fetch failure).
func beadsWorkspaceExists(repoRoot string) bool {
	fi, err := os.Stat(filepath.Join(repoRoot, beadsDirName))
	return err == nil && fi.IsDir()
}

// wykEnrichmentPresent reports whether repoRoot already carries BOTH
// halves of the agent enrichment: the current conventions block in
// CLAUDE.md and the bd-create-guard hook in .claude/settings.json. It
// asks the same questions seedWykConventions / seedClaudeSettings ask to
// decide "already current", so a run that only PREVIEWED the seeding can
// still report accurately on what a previous run left behind.
func wykEnrichmentPresent(repoRoot string) bool {
	b, err := os.ReadFile(filepath.Join(repoRoot, "CLAUDE.md"))
	if err != nil || !bytes.Contains(b, []byte(wykConventionsBlock)) {
		return false
	}
	root, err := loadClaudeSettings(filepath.Join(repoRoot, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	return claudeSettingsHasHook(root, claudeSettingsHook)
}

// installPostCommitHook is `wyk init`'s hook step: resolve the hooks dir
// git ACTUALLY runs and install wyk's auto-close post-commit hook there,
// or explain why it didn't.
//
// It resolves AFTER `bd init` deliberately: bd points core.hooksPath at
// .beads/hooks, so a path resolved earlier would be .git/hooks — writing
// there would install into a directory git ignores and `Closes:` would
// silently never fire. resolveGitHookPath follows core.hooksPath, so
// worktrees and gitlinks land correctly too.
//
// DECLINING is a warning; FAILING is not. wyk choosing not to clobber
// another tool's hook (or not to write through a stale out-of-repo
// redirect) is wyk working as designed, and the caller has already
// registered the repo, so there's nothing to abort. A git or filesystem
// error is a real failure and still reports as one — collapsing the two
// would let `wyk doctor -fix` tally hooks it never wrote. Exit codes:
//
//	0   installed, already installed, or declined with a warning
//	1   git/filesystem error (already reported)
//	64  an explicit -chain that cannot proceed (.pre-wyk is occupied) —
//	    the user asked for something wyk can't deliver
func installPostCommitHook(repoRoot string, opts hookInstallOpts) int {
	dryRun, force, chain := opts.dryRun, opts.force, opts.chain

	hookPath, herr := resolveGitHookPath(repoRoot, "post-commit")
	if herr != nil {
		// Not a decline: git couldn't tell us where hooks live. Callers
		// that count installs (doctor -fix) must see this as a failure.
		fmt.Fprintln(os.Stderr, "wyk init: resolve hook path:", herr)
		return 1
	}
	// coreHooksPath gates the out-of-repo check so a normal worktree
	// (whose shared hooks legitimately live outside the worktree root,
	// but with no core.hooksPath set) isn't mistaken for a redirect.
	if _, set := coreHooksPath(repoRoot); set && !pathWithin(repoRoot, filepath.Dir(hookPath)) {
		warnStaleHooksPathRedirect(repoRoot, filepath.Dir(hookPath), opts)
		return 0
	}
	preWykPath := hookPath + ".pre-wyk"

	// Each branch sets `skipWrite` rather than returning early, so the
	// write decision is made in one place below.
	skipWrite := false
	chainMove := false
	switch existing, err := os.ReadFile(hookPath); {
	case err == nil:
		if bytes.Contains(existing, []byte(hookMarker)) {
			if dryRun {
				fmt.Printf("wyk init: would reinstall %s (existing hook is from a previous `wyk init`)\n", hookPath)
				skipWrite = true
			} else if !force && !chain {
				fmt.Println("wyk init: post-commit hook already installed (use -force to reinstall)")
				skipWrite = true
			}
		} else {
			// Foreign hook. Three options: decline (default), overwrite
			// (-force, destructive), or chain (-chain, preserves the
			// original at .pre-wyk and runs both).
			if dryRun {
				switch {
				case chain:
					// The real -chain run refuses if .pre-wyk already
					// exists (would clobber a previously-preserved
					// hook). Mirror that here so the dry-run accurately
					// previews the outcome.
					if _, err := os.Stat(preWykPath); err == nil {
						fmt.Printf("wyk init: would refuse to chain at %s (because %s already exists — would clobber a previously-preserved hook)\n",
							hookPath, preWykPath)
					} else {
						fmt.Printf("wyk init: would chain foreign hook at %s (move to %s, install wyk wrapper)\n",
							hookPath, preWykPath)
					}
				case force:
					fmt.Printf("wyk init: would overwrite foreign hook at %s (-force)\n", hookPath)
				default:
					warnForeignHookLeftAlone(hookPath, opts)
				}
				skipWrite = true
			} else if chain {
				// Preservation slot already in use? Refuse — we don't
				// want to silently clobber a previously-chained hook.
				if _, err := os.Stat(preWykPath); err == nil {
					fmt.Fprintf(os.Stderr,
						"wyk init: -chain refused: %s already exists\n  (the foreign hook would overwrite a previously-preserved hook)\n",
						preWykPath)
					return 64
				} else if !errors.Is(err, os.ErrNotExist) {
					fmt.Fprintln(os.Stderr, "wyk init: stat .pre-wyk:", err)
					return 1
				}
				chainMove = true
			} else if !force {
				warnForeignHookLeftAlone(hookPath, opts)
				skipWrite = true
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(os.Stderr, "wyk init: stat hook:", err)
		return 1
	}

	// If -chain decided to preserve the existing hook, do the move
	// before writing the wrapper. The wrapper script reads its
	// dirname at runtime, so the .pre-wyk filename matters.
	if chainMove {
		if err := os.Rename(hookPath, preWykPath); err != nil {
			fmt.Fprintln(os.Stderr, "wyk init: preserve foreign hook:", err)
			return 1
		}
		fmt.Printf("wyk init: preserved existing hook → %s\n", preWykPath)
	}

	if skipWrite {
		return 0
	}

	// Pick the hook script body to write: chained wrapper (when -chain
	// was just applied OR a previously-chained install is being
	// re-applied) or the plain hook.
	hookBody := postCommitHook
	if chainMove || preWykExists(preWykPath) {
		hookBody = chainedPostCommitHook
	}

	if dryRun {
		fmt.Printf("wyk init: would install %s\n", hookPath)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: mkdir hooks dir:", err)
		return 1
	}
	if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: write hook:", err)
		return 1
	}
	fmt.Printf("wyk init: installed post-commit hook at %s\n", hookPath)
	fmt.Println("  Commits whose message includes `Closes: <id>`, `Fixes: <id>`, or")
	fmt.Println("  `Resolves: <id>` will now auto-close the referenced bd issue.")
	return 0
}

// warnForeignHookLeftAlone prints the "we didn't touch your hook" notice.
//
// This used to be a fatal `return 64` that read, to a human, like a
// benign skip of an optional feature — while actually aborting init
// before the registry write. It is now a warning, and its job is to be
// unmistakable about two things at once: the auto-close hook is NOT
// installed, and whether the repo is nonetheless visible.
func warnForeignHookLeftAlone(hookPath string, opts hookInstallOpts) {
	verb, tail := "is not installed", "Re-run with"
	if opts.dryRun {
		verb, tail = "would not be installed", "Re-run (without -dry-run) with"
	}
	fmt.Fprintf(os.Stderr, "wyk init: WARNING: the post-commit auto-close hook %s\n", verb)
	fmt.Fprintf(os.Stderr, "  %s already exists and isn't wyk's, so wyk left it alone.\n", hookPath)
	fmt.Fprintln(os.Stderr, "  Commits with `Closes: <id>` will NOT auto-close issues in this repo.")
	fmt.Fprintln(os.Stderr, "  "+tail+" -chain to keep both hooks, or -force to replace.")
	hookDeclineFooter(opts)
}

// warnStaleHooksPathRedirect prints the notice for a core.hooksPath that
// points outside the repo. wyk won't write there — that's how a stale or
// cross-repo config silently swallows the auto-close hook — but, like
// every other decline, it's a warning, not an abort.
func warnStaleHooksPathRedirect(repoRoot, activeDir string, opts hookInstallOpts) {
	verb := "is not installed"
	if opts.dryRun {
		verb = "would not be installed"
	}
	fmt.Fprintf(os.Stderr, "wyk init: WARNING: the post-commit auto-close hook %s\n", verb)
	fmt.Fprintf(os.Stderr, "  git's core.hooksPath points outside this repo:\n    %s\n", activeDir)
	fmt.Fprintln(os.Stderr, "  wyk won't write a hook there. Commits with `Closes: <id>` will NOT auto-close.")
	fmt.Fprintln(os.Stderr, "  That path is almost certainly stale. Clear it and re-run wyk init:")
	fmt.Fprintln(os.Stderr, "    git -C "+repoRoot+" config --unset core.hooksPath")
	hookDeclineFooter(opts)
}

// hookDeclineFooter closes out every "wyk didn't install a hook" notice
// with the state of the thing that actually matters: whether this repo
// is visible to the multi-repo views.
//
// Splitting this out is not cosmetic. The reassurance was once printed
// unconditionally, so `wyk init -skip-register` on a repo with a foreign
// hook promised registration that never happened — an agent reading "IS
// visible to `wyk inbox`" files a handoff no human receives. Keying it
// off the flag then produced the mirror-image lie: `doctor -fix` passes
// -skip-register for registry-sourced repos, so it told users their
// REGISTERED repo was invisible and to `wyk registry add` it (a no-op).
// Each branch below states only what has actually been established.
//
// The capitalised IS / WOULD BE is spelled out per branch rather than
// interpolated: it's the emphasis that makes the notice scannable, and
// it doesn't survive a %s.
func hookDeclineFooter(opts hookInstallOpts) {
	switch opts.visibility {
	case regAbsent:
		fmt.Fprintln(os.Stderr, "  This repo is NOT registered (this run skipped it: -skip-register), so it is")
		fmt.Fprintln(os.Stderr, "  invisible to `wyk inbox`, the dashboard and the TUI — run `wyk registry add`.")
	case regUnknown:
		fmt.Fprintln(os.Stderr, "  This run didn't touch the registry (-skip-register) and the registry couldn't")
		fmt.Fprintln(os.Stderr, "  be read — check `wyk registry list`; an unregistered repo is invisible to")
		fmt.Fprintln(os.Stderr, "  `wyk inbox`, the dashboard and the TUI.")
	case regWillRegister:
		fmt.Fprintf(os.Stderr, "  Would set up: %s.\n", bootstrapComponents(opts))
		fmt.Fprintln(os.Stderr, "  This repo WOULD BE visible to `wyk inbox`, the dashboard and the TUI.")
	default: // regVisible
		fmt.Fprintf(os.Stderr, "  Set up: %s.\n", bootstrapComponents(opts))
		fmt.Fprintln(os.Stderr, "  This repo IS visible to `wyk inbox`, the dashboard and the TUI.")
	}
}

// bootstrapComponents names the pieces of the bootstrap this run
// established, for the decline footer's leading clause.
//
// It enumerates rather than asserting "everything else" because that
// phrasing was the same over-claim `-skip-register` was fixed for, one
// layer down: the footer named the bd workspace and the agent enrichment
// with no visibility into -skip-bd-init / -skip-claude-md, and
// `doctor -fix` passes -skip-bd-init on every call. A component whose
// step didn't run (or failed) is omitted, never claimed.
//
// The registry entry is unconditional here because the only callers are
// the regVisible / regWillRegister branches, which are exactly the ones
// that established it.
func bootstrapComponents(opts hookInstallOpts) string {
	parts := make([]string, 0, 3)
	if opts.bdWorkspace {
		parts = append(parts, "bd workspace")
	}
	parts = append(parts, "registry entry")
	if opts.enrichment {
		parts = append(parts, "agent enrichment")
	}
	return strings.Join(parts, ", ")
}

// previewRegister inspects the current registry and prints the same
// "already registered" / "would register" message the real run
// would produce. Errors loading the registry are surfaced inline
// (and don't abort init — the real run is the source of truth).
func previewRegister(repoRoot string) {
	path, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: resolve registry path:", err)
		return
	}
	reg, err := registry.Load(path)
	if err != nil {
		// Pre-flight load failed; the real run would error here too,
		// but for a dry-run we just describe the intended action.
		fmt.Printf("wyk init: would register %s in %s (current registry unreadable: %v)\n",
			repoRoot, path, err)
		return
	}
	if reg.Has(repoRoot) {
		fmt.Printf("wyk init: already registered in %s\n", path)
		return
	}
	fmt.Printf("wyk init: would register %s in %s\n", repoRoot, path)
}

// runBDInit invokes `bd init` in the given repo root and returns an
// exit code for runInit. bd's own stdout/stderr passes through so the
// user sees what bd did.
func runBDInit(repoRoot string) int {
	fmt.Printf("wyk init: running `bd init` in %s\n", repoRoot)
	cmd := exec.Command("bd", "init")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintln(os.Stderr, "wyk init: bd is not installed (or not on PATH)")
			return 1
		}
		fmt.Fprintln(os.Stderr, "wyk init: bd init failed:", err)
		return 1
	}
	return 0
}

// registerRepo adds the repo root to ~/.config/wyk/repos.json. The
// add is idempotent — repeat invocations don't duplicate entries.
func registerRepo(repoRoot string) int {
	path, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: resolve registry path:", err)
		return 1
	}
	reg, err := registry.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: load registry:", err)
		return 1
	}
	already := reg.Has(repoRoot)
	if err := reg.Add(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: add to registry:", err)
		return 1
	}
	if err := reg.Save(path); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: save registry:", err)
		return 1
	}
	if already {
		fmt.Printf("wyk init: already registered in %s\n", path)
	} else {
		fmt.Printf("wyk init: registered %s in %s\n", repoRoot, path)
	}
	return 0
}

// findGitPaths returns (gitDir, repoRoot) in a single `git rev-parse`
// invocation. Both paths are absolute. Returns an error if cwd is
// not inside a git repository.
func findGitPaths() (gitDir, repoRoot string, err error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if rerr := cmd.Run(); rerr != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = rerr.Error()
		}
		return "", "", fmt.Errorf("not a git repository (%s)", errOut)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("git rev-parse returned unexpected output: %q", stdout.String())
	}
	gitDir = strings.TrimSpace(lines[0])
	repoRoot = strings.TrimSpace(lines[1])
	// Check emptiness BEFORE normalising: filepath.Join(cwd, "") returns
	// cwd, so an empty gitDir would silently become the working directory
	// and the check below it could never fire.
	if gitDir == "" || repoRoot == "" {
		return "", "", errors.New("git rev-parse returned empty paths")
	}
	if !filepath.IsAbs(gitDir) {
		// `git rev-parse --git-dir` may emit a relative path when run
		// from inside the working tree; resolve against cwd.
		cwd, werr := os.Getwd()
		if werr != nil {
			return "", "", fmt.Errorf("getwd: %w", werr)
		}
		gitDir = filepath.Join(cwd, gitDir)
	}
	return gitDir, repoRoot, nil
}

// bdRememberRunner is the test seam for teachBDConvention's bd
// shell-out. Production points at runBDRemember (real exec); tests
// swap in a stub so they can assert the args/keys without needing
// bd on PATH or a real workspace. Mirrors the probeBDFunc pattern
// elsewhere in this file.
var bdRememberRunner = runBDRemember

// runBDRemember invokes `bd remember --key <key> --dolt-auto-commit=on <memory>`
// in repoRoot. Trims stderr into the returned error message when
// non-empty so the user sees bd's specific complaint rather than the
// generic "exit status 1".
func runBDRemember(repoRoot, key, memory string) error {
	cmd := exec.Command("bd", "remember", "--key", key, "--dolt-auto-commit=on", memory)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// rememberedConventionKey is the bd-remember --key for wyk's
// convention memory. Exposed as a const so tests can assert it
// without duplicating the magic string.
const rememberedConventionKey = "wyk-handoff-convention"

// rememberedConventionMemory is the memory text wyk init stores
// via bd remember. Kept as a const so a test can assert the labels
// are present.
const rememberedConventionMemory = "wyk convention: a human task carries label=human + label=src:agent; agent-owned just label=src:agent. Inbox = `" + agentInboxQuery + "` (run `wyk inbox`) — WORK returned items, don't just note them. Skip HUMAN-BLOCK rows (agent task with a human-flagged dep) and AGENT-HANDOFF rows (label=agent-handoff = another agent's; a human coordinates). File/hand off a human task with `wyk handoff <id>` (or `wyk handoff -create \"<title>\"`), never hand-rolled labels. Multi-agent: route with `wyk handoff --identity <name>` (adds src:agent:<name>), read with `wyk inbox --identity <name>` or $WYK_AGENT_IDENTITY. Statuses: open(default)/in_progress/blocked(+--add-dependency)/deferred(subsystem not ready, hidden from bd ready)/closed; prefer deferred over holding a task open. Full text + runbook format: `wyk conventions`."

// teachBDConvention writes a single bd memory describing the wyk
// label convention into repoRoot's bd workspace. The --key makes
// the call idempotent — repeated `wyk init` runs update in place
// rather than duplicating. bd prime surfaces memories at session
// start, so this is the channel by which the convention reaches
// agents working in repos wyk init has touched.
//
// We pass --dolt-auto-commit=on per the project's bd write
// convention (otherwise the memory lives in Dolt's working set
// and won't survive `bd dolt push`).
func teachBDConvention(repoRoot string) error {
	return bdRememberRunner(repoRoot, rememberedConventionKey, rememberedConventionMemory)
}

// wykConventionsBeginMarker / wykConventionsEndMarker delimit the block
// `wyk init` manages inside CLAUDE.md. They mirror bd's own
// `<!-- BEGIN BEADS INTEGRATION -->` markers so the two installers
// coexist without stepping on each other, and so re-running init can
// refresh wyk's block in place rather than appending duplicates. The
// `v:1` lets a future version detect and migrate an older block.
const (
	wykConventionsBeginMarker = "<!-- BEGIN WYK CONVENTIONS v:1 -->"
	wykConventionsEndMarker   = "<!-- END WYK CONVENTIONS -->"
	// wykConventionsBeginPrefix matches ANY version of the begin marker
	// (the v:N suffix varies). Detection and refresh key off this prefix
	// so re-running init upgrades an older block in place — and so a
	// hand-rolled block without a version is recognised — rather than
	// appending a duplicate.
	wykConventionsBeginPrefix = "<!-- BEGIN WYK CONVENTIONS"
)

// wykConventionsBlock is the marker-delimited section wyk init writes
// into CLAUDE.md. It is the canonical, repo-local statement of what
// "wyk" means to an agent: there is no `wyk create`, planning is
// `bd create`, the owner column is label-driven, and human work is
// handed off via `wyk handoff`. Kept as one const so the refresh path
// can compare the existing block byte-for-byte and skip a no-op write.
const wykConventionsBlock = wykConventionsBeginMarker + `
## wyk — planning & handoff over bd

This repo uses **wyk**, a view + handoff layer over **bd (beads)**. "Plan
it in wyk" = **file the plan as bd issues** (deps via ` + "`bd dep add`" + `), not
markdown/TodoWrite. File with **` + "`wyk create`" + `** (same flags as ` + "`bd create`" + `,
forwarded verbatim) — it also stamps the Claude session so the TUI's
Session column traces work back to a conversation. A PreToolUse hook
blocks raw ` + "`bd create`" + ` and tells you to switch; that's expected — just
re-run as ` + "`wyk create`" + `.

**Owner column** — whose move it is, label-driven (NOT bd's owner/assignee):
- ` + "`human`" + ` → **HUMAN** (a human must act).
- ` + "`agent-handoff`" + ` → **AGENT-HANDOFF**: another agent owns it; don't touch,
  a human coordinates. Excluded from ` + "`wyk inbox`" + `.
- agent task blocked by a ` + "`human`" + `-flagged dep → **HUMAN-BLOCK** (skip it).
- else → **AGENT** (the default; a null owner is never blank — so a task
  that needs a human MUST be handed off, or the human never sees it).

**Hand off to a human**: ` + "`wyk handoff <id>`" + ` (or ` + "`wyk handoff -create \"<title>\"`" + `)
sets ` + "`human`" + ` + writes the runbook. Never hand-roll labels; ` + "`-a`/`--claim`" + `
are bd's status, not the badge.

**Pick up work**: ` + "`wyk inbox`" + ` FIRST (items bounced back to you — WORK them),
then ` + "`wyk`" + ` / ` + "`bd ready`" + `. ` + "`wyk conventions`" + ` prints the full contract.

**Something wrong? Act — don't shrug.** If a wyk/bd command errors, a
convention looks broken, or the workflow rubs wrong, file a bd issue (with
an owner) and fix or hand it off — don't route around it silently.
Friction with wyk is product data; surfacing it is the job.
` + wykConventionsEndMarker

// claudeMDPreamble heads a CLAUDE.md that wyk init creates from scratch
// (no file present). When CLAUDE.md already exists we never touch
// anything outside our markers, so this is only used on first creation.
const claudeMDPreamble = `# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

`

// seedWykConventions ensures repoRoot/CLAUDE.md carries wyk's
// conventions block. It returns a short human-readable description of
// what it did (for the init log) and never touches content outside the
// BEGIN/END markers. Behaviour:
//
//   - no CLAUDE.md            → create it (preamble + block)
//   - block present, current  → no-op ("already current")
//   - block present, stale    → replace the block in place
//   - block absent            → append the block (one blank line before)
//   - BEGIN without END       → refuse (don't corrupt a malformed file)
//
// With dryRun, it reports the action it would take and writes nothing.
func seedWykConventions(repoRoot string, dryRun bool) (string, error) {
	path := filepath.Join(repoRoot, "CLAUDE.md")
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if dryRun {
			return "would create CLAUDE.md with wyk conventions", nil
		}
		if werr := writeFileAtomic(path, []byte(claudeMDPreamble+wykConventionsBlock+"\n"), 0o644); werr != nil {
			return "", werr
		}
		return "created CLAUDE.md with wyk conventions", nil
	case err != nil:
		return "", err
	}

	content := string(existing)
	if i := strings.Index(content, wykConventionsBeginPrefix); i >= 0 {
		end := strings.Index(content, wykConventionsEndMarker)
		if end < i {
			return "", fmt.Errorf("CLAUDE.md has %q without a matching %q after it; leaving it untouched",
				wykConventionsBeginPrefix, wykConventionsEndMarker)
		}
		end += len(wykConventionsEndMarker)
		if content[i:end] == wykConventionsBlock {
			return "wyk conventions already current in CLAUDE.md", nil
		}
		if dryRun {
			return "would refresh the wyk conventions block in CLAUDE.md", nil
		}
		updated := content[:i] + wykConventionsBlock + content[end:]
		if werr := writeFileAtomic(path, []byte(updated), 0o644); werr != nil {
			return "", werr
		}
		return "refreshed the wyk conventions block in CLAUDE.md", nil
	}

	if dryRun {
		return "would append wyk conventions to CLAUDE.md", nil
	}
	// Guarantee exactly one blank line between the existing content and
	// our block, regardless of how the file currently ends.
	prefix := content
	if !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	if !strings.HasSuffix(prefix, "\n\n") {
		prefix += "\n"
	}
	if werr := writeFileAtomic(path, []byte(prefix+wykConventionsBlock+"\n"), 0o644); werr != nil {
		return "", werr
	}
	return "appended wyk conventions to CLAUDE.md", nil
}

// claudeSettingsHook is the command wyk init registers as a PreToolUse
// hook so an agent that runs `bd create` is redirected to `wyk create`
// (see runHookBDCreateGuard).
const claudeSettingsHook = "wyk hook bd-create-guard"

// seedClaudeSettings ensures repoRoot/.claude/settings.json registers the
// bd-create-guard PreToolUse hook. It merges into any existing settings
// (bd init writes SessionStart/PreCompact hooks there) and is idempotent
// — re-running doesn't duplicate the entry. Returns a short action
// description for the init log. A malformed existing file is reported as
// an error rather than overwritten.
func seedClaudeSettings(repoRoot string, dryRun bool) (string, error) {
	path := filepath.Join(repoRoot, ".claude", "settings.json")
	root, err := loadClaudeSettings(path)
	if err != nil {
		return "", err
	}
	if claudeSettingsHasHook(root, claudeSettingsHook) {
		return "bd-create-guard hook already in .claude/settings.json", nil
	}
	if dryRun {
		return "would register the bd-create-guard PreToolUse hook in .claude/settings.json", nil
	}
	addPreToolUseHook(root, claudeSettingsHook)
	if err := writeClaudeSettings(path, root); err != nil {
		return "", err
	}
	return "registered the bd-create-guard PreToolUse hook in .claude/settings.json", nil
}

// loadClaudeSettings parses a settings.json into a generic map, treating a
// missing file as an empty object so callers can add to it unconditionally.
// A malformed file is an error rather than silently overwritten.
func loadClaudeSettings(path string) (map[string]any, error) {
	root := map[string]any{}
	switch b, err := os.ReadFile(path); {
	case errors.Is(err, os.ErrNotExist):
		// fresh file
	case err != nil:
		return nil, err
	default:
		if uerr := json.Unmarshal(b, &root); uerr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, uerr)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	return root, nil
}

// writeClaudeSettings marshals root and writes it atomically (creating the
// .claude dir if needed, via writeFileAtomic).
func writeClaudeSettings(path string, root map[string]any) error {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeFileAtomic(path, out, 0o644)
}

// removeHookForEvent drops every hook whose command equals cmd from the
// given event, pruning entries (and the event itself) left empty. Reports
// whether anything was removed.
func removeHookForEvent(root map[string]any, event, cmd string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	entries, _ := hooks[event].([]any)
	var kept []any
	removed := false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		var keptInner []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == cmd {
				removed = true
				continue
			}
			keptInner = append(keptInner, h)
		}
		if len(keptInner) == 0 {
			continue // entry had only our hook; drop it
		}
		entry["hooks"] = keptInner
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	return removed
}

// writeFileAtomic writes data to path via a sibling temp file + rename so
// an interrupted write can't truncate or corrupt an existing file —
// matching the durability the registry/session writers use. This matters
// for settings.json (which already holds bd's SessionStart/PreCompact
// hooks the merge works to preserve) and for the user's CLAUDE.md, which
// seedWykConventions rewrites in place. Creates the parent dir on demand.
//
// Two existing-file behaviors are deliberately preserved from the
// os.WriteFile semantics this replaced: a symlinked path (the common
// CLAUDE.md → AGENTS.md setup) is resolved first so the rename lands on
// the link's target instead of replacing the link with a regular file —
// including a DANGLING link, whose target is created just as a
// write-through would have — and an existing file keeps its own mode;
// perm applies only when the file is being created. The create-parent-
// dirs-on-demand behavior applies only to the path as given: a dangling
// link pointing into a missing directory fails like os.WriteFile's
// ENOENT would, rather than silently materializing directory trees the
// link's author never created.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	resolved, err := resolveWriteTarget(path)
	if err != nil {
		return err
	}
	if resolved == path {
		return writeResolvedAtomic(path, data, perm, true)
	}
	// A failure under the resolved target would otherwise surface a
	// path the caller never wrote (e.g. CreateTemp's hidden temp name
	// under a link's missing dir) — name the link they asked about.
	if err := writeResolvedAtomic(resolved, data, perm, false); err != nil {
		return fmt.Errorf("writing through symlink %s: %w", path, err)
	}
	return nil
}

// writeResolvedAtomic is writeFileAtomic's write half, operating on an
// already-resolved path. mkdirParent creates the parent on demand —
// disabled for symlink-resolved targets (see writeFileAtomic's doc).
func writeResolvedAtomic(path string, data []byte, perm os.FileMode, mkdirParent bool) error {
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if mkdirParent {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// resolveWriteTarget walks symlinks on the final path component (Lstat +
// Readlink, relative targets resolved against the link's directory) and
// returns where a write should actually land. Unlike filepath.EvalSymlinks
// it resolves a DANGLING link too — returning the nonexistent target so
// the caller creates it there, exactly as an os.WriteFile write-through
// would have — instead of erroring and letting the rename clobber the
// link itself.
func resolveWriteTarget(path string) (string, error) {
	orig := path
	// 40 hops matches the traversal limit kernels use for symlink
	// chains; a longer chain is a loop in practice.
	for range 40 {
		fi, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return path, nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	// Name the path the caller asked about, not the final hop — a
	// chain of relative targets can wander to a joined path the user
	// never wrote, which would make the loop hard to locate.
	return "", fmt.Errorf("too many levels of symbolic links resolving %s", orig)
}

// settingsHasHookForEvent reports whether any hook command under the given
// Claude Code event (PreToolUse, Stop, …) in the parsed settings equals cmd.
func settingsHasHookForEvent(root map[string]any, event, cmd string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == cmd {
				return true
			}
		}
	}
	return false
}

// claudeSettingsHasHook reports whether any PreToolUse hook command in the
// parsed settings equals cmd — the idempotency check for seedClaudeSettings.
func claudeSettingsHasHook(root map[string]any, cmd string) bool {
	return settingsHasHookForEvent(root, "PreToolUse", cmd)
}

// addHookForEvent appends an entry running cmd under the given Claude Code
// event, creating the hooks / <event> containers as needed and preserving any
// existing entries (e.g. bd's SessionStart/PreCompact). A non-empty matcher
// scopes the entry (tool-call events like PreToolUse); session events like
// Stop take no matcher, so pass "" to omit the key.
func addHookForEvent(root map[string]any, event, matcher, cmd string) {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": cmd,
		}},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	entries, _ := hooks[event].([]any)
	hooks[event] = append(entries, entry)
}

// addPreToolUseHook appends a Bash-matched PreToolUse entry running cmd.
func addPreToolUseHook(root map[string]any, cmd string) {
	addHookForEvent(root, "PreToolUse", "Bash", cmd)
}

// resolveGitHookPath returns the absolute path to <hook> inside
// repoDir's resolved git hooks directory. Shells out to
// `git -C <repoDir> rev-parse --git-path hooks/<hook>` so gitlinks
// (a `.git` file containing `gitdir: <path>`, common for submodules
// and worktree-style subdirectory registrations), worktrees, and
// custom GIT_DIR layouts all land on the same hook the installer
// would have written. Callers should treat any non-nil error as
// "couldn't resolve the path" and surface it; missing-hook is then
// distinguishable from path-resolution-failure.
func resolveGitHookPath(repoDir, hook string) (string, error) {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--git-path", "hooks/"+hook)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git rev-parse --git-path hooks/%s: %s", hook, msg)
	}
	p := strings.TrimSpace(stdout.String())
	if p == "" {
		return "", fmt.Errorf("git rev-parse --git-path hooks/%s returned empty path", hook)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoDir, p)
	}
	return p, nil
}

// coreHooksPath returns git's configured core.hooksPath for repoDir
// (value, true) or ("", false) when unset. When set, git runs hooks
// from there instead of the default .git/hooks — silently bypassing the
// post-commit hook wyk installs in .git/hooks unless wyk also installed
// into that dir. This is the root of the "Closes: auto-close did
// nothing" class of failure when another tool (e.g. bd) points
// core.hooksPath at its own managed hooks dir.
func coreHooksPath(repoDir string) (string, bool) {
	out, err := exec.Command("git", "-C", repoDir, "config", "--get", "core.hooksPath").Output()
	if err != nil {
		return "", false // exit status 1 == key unset
	}
	v := strings.TrimSpace(string(out))
	return v, v != ""
}

// canonPath resolves p to a canonical absolute form, following symlinks
// through the deepest existing ancestor and re-appending any missing
// tail. This matches registry.normalizePath's symlink-resolution intent
// so macOS's /var → /private/var shortcut (or any user symlink) can't
// make two representations of the same location compare unequal — while
// still working for a path (like a core.hooksPath dir) that doesn't
// exist yet.
func canonPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	tail := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // reached the root without resolving — use the abs form
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// pathWithin reports whether child is parent itself or nested under it.
// Used to tell an in-repo core.hooksPath (e.g. .beads/hooks — a real
// setup wyk should install into) from one pointing outside the repo
// (almost always stale/misconfigured — wyk must not write there). Both
// operands are symlink-canonicalised first so a /var vs /private/var
// (or other symlink) representation gap can't misclassify an in-repo
// dir as external.
func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(canonPath(parent), canonPath(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// hooksPathRedirect classifies how git's core.hooksPath affects the
// post-commit hook wyk installs in .git/hooks. Returns redirected = true
// when git will run hooks from somewhere wyk's hook is NOT, plus enough
// detail for the caller to name a remediation that actually works:
// whether the active dir is inside the repo (install-there vs
// unset-the-stale-config) and whether a foreign hook already occupies the
// active slot (which decides -chain/-force vs a bare `wyk init`).
func hooksPathRedirect(repoDir string) (active string, redirected, insideRepo, wykHookActive, foreignHookActive bool) {
	hp, set := coreHooksPath(repoDir)
	if !set {
		return "", false, false, false, false
	}
	activePost, err := resolveGitHookPath(repoDir, "post-commit")
	if err != nil {
		return hp, true, false, false, false
	}
	activeDir := filepath.Dir(activePost)
	insideRepo = pathWithin(repoDir, activeDir)
	if body, rerr := os.ReadFile(activePost); rerr == nil {
		if bytes.Contains(body, []byte(hookMarker)) {
			wykHookActive = true
		} else {
			foreignHookActive = true
		}
	}
	return activeDir, true, insideRepo, wykHookActive, foreignHookActive
}

// scanProbeTimeout caps each candidate's bd-readiness probe. Tight
// enough that a registry with N dud workspaces doesn't multiply
// total scan time by N*5s, loose enough that a real workspace on a
// slow filesystem (NFS, Time Machine snapshot) gets a fair shot.
// Declared as `var` (not const) so tests can swap in a tighter
// value via scanProbeTimeoutForTest.
var scanProbeTimeout = 2 * time.Second

// probeBDFunc is the seam the scan path uses to ask "can bd actually
// read this workspace?". The default implementation shells out to
// `bd query`; tests inject a stub so they don't depend on a real bd
// binary or a real DB.
type probeBDFunc func(ctx context.Context, dir string) error

// defaultProbeBD runs a cheap `bd query status!=closed` against dir.
// Success means bd can read the workspace at all — distinguishing
// real workspaces from jsonl-only exports, abandoned shells, and
// workspaces whose Dolt DB is otherwise unreadable. The query
// expression is the cheapest one that exercises the full bd-load
// path; we discard the returned issues.
func defaultProbeBD(ctx context.Context, dir string) error {
	c := beads.NewClient()
	c.Dir = dir
	_, err := c.Query(ctx, "status!=closed")
	return err
}

// runUninstall is the inverse of the per-repo install path: remove
// wyk's post-commit hook. If a post-commit.pre-wyk file exists
// (chained install), restore it so the original tool's hook
// resumes. If no .pre-wyk file is present (plain install), just
// delete post-commit. Refuses outright when the installed hook
// isn't wyk's, so a foreign hook isn't silently wiped — the
// caller can inspect, then run `mv` manually or pass through
// `wyk init -force` if they really want.
//
// Exit codes:
//
//	0  hook removed (or, with dryRun, would be) — or already absent
//	1  filesystem error (read/write/rename)
//	2  no git repo here (findGitPaths failed)
//	64 hook present but not wyk's — refused
func runUninstall(dryRun bool) int {
	gitDir, repoRoot, err := findGitPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
	}
	// The active hooks dir (follows core.hooksPath) is where the current
	// wyk installs; .git/hooks is where a pre-core.hooksPath install left
	// one. Clean both so a repo that gained a core.hooksPath redirect after
	// an older install doesn't keep an orphaned (bypassed) wyk hook behind.
	active, herr := resolveGitHookPath(repoRoot, "post-commit")
	if herr != nil {
		fmt.Fprintln(os.Stderr, "wyk init: resolve hook path:", herr)
		return 1
	}
	defaultPath := filepath.Join(gitDir, "hooks", "post-commit")

	code, removed := uninstallWykHookAt(active, dryRun)
	if code == 1 {
		return 1
	}
	// A foreign hook in the active dir isn't ours to remove — but it must
	// NOT abort the sweep of an orphaned wyk hook in .git/hooks. That's the
	// common migration shape: core.hooksPath points at another tool's dir
	// (e.g. bd's .beads/hooks) that already holds that tool's own hook, so
	// the active hook is foreign while a bypassed wyk hook still sits in
	// .git/hooks. Warn and carry on.
	foreignActive := code == 64
	if foreignActive {
		fmt.Fprintln(os.Stderr,
			"wyk init: post-commit hook at", active, "is not wyk's — leaving it untouched.")
	}

	// Sweep a stale orphan from the default .git/hooks when it's a
	// different physical file. canonPath both sides so a /var↔/private/var
	// or symlink representation gap doesn't make us process the same hook
	// twice. A foreign hook there isn't ours either, so its 64 is ignored.
	if canonPath(active) != canonPath(defaultPath) {
		switch c2, r2 := uninstallWykHookAt(defaultPath, dryRun); {
		case c2 == 1:
			return 1
		case r2:
			removed = true
		}
	}

	switch {
	case removed:
		return 0
	case foreignActive:
		// Only a foreign hook and nothing of ours anywhere — preserve the
		// "refused to touch a non-wyk hook" exit signal.
		return 64
	default:
		fmt.Println("wyk init: no post-commit hook installed — nothing to uninstall")
		return 0
	}
}

// uninstallWykHookAt removes wyk's post-commit hook at path: restores a
// chained .pre-wyk if present, else deletes the plain hook. Returns
// (exitCode, removed): (0,false) = no hook here; (0,true) = removed or
// restored; (1,_) = a filesystem error was already reported; (64,false)
// = a hook is present but isn't wyk's (the caller decides whether to
// refuse). Honours dryRun by printing the action without performing it.
func uninstallWykHookAt(path string, dryRun bool) (int, bool) {
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 0, false
	case err != nil:
		fmt.Fprintln(os.Stderr, "wyk init: read hook:", err)
		return 1, false
	}
	if !bytes.Contains(existing, []byte(hookMarker)) {
		return 64, false
	}
	preWykPath := path + ".pre-wyk"
	if preWykExists(preWykPath) {
		if dryRun {
			fmt.Printf("wyk init: would restore %s → %s (chained install detected)\n", preWykPath, path)
			return 0, true
		}
		if err := os.Rename(preWykPath, path); err != nil {
			fmt.Fprintln(os.Stderr, "wyk init: restore .pre-wyk:", err)
			return 1, false
		}
		fmt.Printf("wyk init: restored %s → %s\n", preWykPath, path)
		return 0, true
	}
	if dryRun {
		fmt.Printf("wyk init: would remove %s\n", path)
		return 0, true
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init: remove hook:", err)
		return 1, false
	}
	fmt.Printf("wyk init: removed %s\n", path)
	return 0, true
}

// chainHookIntoRepo runs `wyk init -chain` inside dir. Initialized
// from an init() function (not a top-level var) to avoid a
// package-init cycle: chainHookIntoRepo → runInit →
// runFixForeignHooks → chainHookIntoRepo would otherwise be
// flagged by Go's static initializer analysis. The function value
// is the test seam — tests assign their own implementation before
// calling runFixForeignHooks, then restore.
var chainHookIntoRepo func(dir string) int

func init() {
	chainHookIntoRepo = func(dir string) int {
		prev, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk init: getwd:", err)
			return 1
		}
		defer func() { _ = os.Chdir(prev) }()
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, "wyk init: chdir:", err)
			return 1
		}
		return runInit([]string{"-chain", "-skip-bd-init", "-skip-register"})
	}
}

// runFixForeignHooks walks the registry and runs `wyk init -chain`
// in every repo whose post-commit hook is foreign (exists but
// doesn't carry the wyk marker). Repos with a wyk-installed hook
// (plain or chained), and repos missing a hook entirely, are left
// alone — the missing case is what `wyk doctor -fix` handles; the
// already-wyk case is a no-op.
//
// The inverse of doctor -fix: doctor's auto-install refuses to
// touch foreign hooks, while this command targets exactly that
// set. Uses the chainHookIntoRepo seam (not installHookIn, which
// installs a plain wyk hook) so tests can substitute the chain
// side effect.
//
// Exit codes:
//
//	0  every foreign hook chained (or, with dryRun, would be)
//	1  per-repo failure for at least one repo (partial work still done)
//	2  registry resolvable but unreadable, or no registered repos
func runFixForeignHooks(dryRun bool) int {
	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk init: no repos registered — nothing to fix")
		return 2
	}

	hadError := false
	chained, alreadyWyk, missing := 0, 0, 0
	for _, r := range reg.Repos {
		hookPath, herr := resolveGitHookPath(r.Path, "post-commit")
		if herr != nil {
			fmt.Fprintf(os.Stderr, "wyk init: %s: resolve hook path: %v\n", r.Name, herr)
			hadError = true
			continue
		}
		body, rerr := os.ReadFile(hookPath)
		switch {
		case errors.Is(rerr, os.ErrNotExist):
			// Missing hook isn't a foreign hook; doctor -fix is the
			// command for that case. Leave it alone here.
			missing++
		case rerr != nil:
			fmt.Fprintf(os.Stderr, "wyk init: %s: read hook: %v\n", r.Name, rerr)
			hadError = true
		case bytes.Contains(body, []byte(hookMarker)):
			alreadyWyk++
		default:
			// Foreign hook — chain wyk after it.
			if dryRun {
				fmt.Printf("wyk init: would chain wyk into %s (%s)\n", r.Name, r.Path)
				chained++
				continue
			}
			if code := chainHookIntoRepo(r.Path); code != 0 {
				fmt.Fprintf(os.Stderr, "wyk init: %s: chain failed (exit %d)\n", r.Name, code)
				hadError = true
				continue
			}
			chained++
		}
	}

	prefix := "init -fix-foreign-hooks"
	verb := "chained"
	if dryRun {
		prefix += " (dry-run)"
		verb = "to chain"
	}
	fmt.Printf("%s: %d %s, %d already-wyk, %d missing (run `wyk doctor -fix` to install those)\n",
		prefix, chained, verb, alreadyWyk, missing)
	if hadError {
		return 1
	}
	return 0
}

// runScanAndRegister walks the filesystem under root, finds every
// .beads/ directory, probes each unregistered candidate with bd to
// confirm it's a usable workspace, then registers the survivors
// into ~/.config/wyk/repos.json. Candidates that fail the probe
// (bd errors, jsonl-only export, abandoned shell) are SKIPPED with
// a stderr line — the alternative was silently registering duds
// that the user then has to clean up via `wyk registry remove`.
//
// Skipped during traversal: any .git, .cache, .beads itself,
// node_modules, vendor, and any other hidden directory.
//
// Exit codes:
//
//	0  one or more new repos registered (or, with -dry-run, would be)
//	1  filesystem / registry error (e.g. unreadable root, permission denied)
//	2  root does not exist or isn't a directory
func runScanAndRegister(root string, dryRun bool) int {
	return runScanAndRegisterWithProbe(root, dryRun, defaultProbeBD)
}

// runScanAndRegisterWithProbe is the test seam — the real entry
// point passes defaultProbeBD.
func runScanAndRegisterWithProbe(root string, dryRun bool, probe probeBDFunc) int {
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan:", err)
		return 1
	}
	st, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(os.Stderr, "wyk init -scan:", err)
		return 2
	case err != nil:
		// Other stat failures (permission denied, etc.) are
		// "filesystem error" per the exit-code contract.
		fmt.Fprintln(os.Stderr, "wyk init -scan:", err)
		return 1
	case !st.IsDir():
		fmt.Fprintln(os.Stderr, "wyk init -scan: not a directory:", abs)
		return 2
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan: resolve registry path:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan: load registry:", err)
		return 1
	}

	found, skippedPaths, err := scanForBeadsRepos(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan: walk:", err)
		return 1
	}
	// Name what the scan couldn't read. Without this a tree that is half
	// permission-denied reports "0 new repos" and looks conclusive.
	for _, s := range skippedPaths {
		fmt.Fprintln(os.Stderr, "wyk init -scan: skipped unreadable path:", s)
	}

	var newOnes, alreadyRegistered, skipped []string
	for _, path := range found {
		if reg.Has(path) {
			alreadyRegistered = append(alreadyRegistered, path)
			continue
		}
		// Probe before adding to newOnes — a candidate that bd
		// can't read shouldn't pollute the registry just because
		// it has a .beads/ subdir. Stderr the reason so the user
		// can see what's being rejected.
		ctx, cancel := context.WithTimeout(context.Background(), scanProbeTimeout)
		perr := probe(ctx, path)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		if perr != nil {
			// bd missing from PATH would otherwise turn every
			// candidate into a skip-with-identical-reason and
			// exit 0 — a silent no-op for an environmental
			// problem, not a "no usable workspaces" result.
			// Bail once with exit 1 instead.
			if errors.Is(perr, beads.ErrBDNotFound) {
				fmt.Fprintln(os.Stderr, "wyk init -scan: bd is not installed (or not on PATH); cannot probe candidates. Install from https://github.com/gastownhall/beads and retry.")
				return 1
			}
			reason := perr.Error()
			if timedOut {
				reason = fmt.Sprintf("bd query timed out after %s", scanProbeTimeout)
			}
			fmt.Fprintf(os.Stderr, "wyk init -scan: skipping %s (bd query failed: %s)\n", path, reason)
			skipped = append(skipped, path)
			continue
		}
		newOnes = append(newOnes, path)
	}

	fmt.Printf("wyk init -scan: searched %s\n", abs)
	fmt.Printf("  found %d bd workspace(s): %d new, %d already registered, %d skipped (bd failed)\n",
		len(found), len(newOnes), len(alreadyRegistered), len(skipped))

	if dryRun {
		if len(newOnes) == 0 {
			fmt.Println("  (dry-run) nothing new to register.")
			return 0
		}
		fmt.Println("  (dry-run) would register:")
		for _, p := range newOnes {
			fmt.Printf("    + %s\n", p)
		}
		return 0
	}

	if len(newOnes) == 0 {
		fmt.Println("  nothing new to register.")
		return 0
	}
	for _, p := range newOnes {
		if err := reg.Add(p); err != nil {
			fmt.Fprintf(os.Stderr, "wyk init -scan: add %s: %v\n", p, err)
			return 1
		}
		fmt.Printf("  + %s\n", p)
	}
	if err := reg.Save(regPath); err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan: save registry:", err)
		return 1
	}
	fmt.Printf("  registered %d new repo(s) in %s\n", len(newOnes), regPath)
	return 0
}

// scanForBeadsRepos walks root looking for .beads/ directories. The
// repo root for each match is the directory containing .beads/. We
// stop descending into hidden directories (e.g. .git, .cache) and
// into common heavy directories (node_modules, vendor) to keep the
// walk responsive on large project trees. We never descend into a
// found .beads/ itself either — bd's own internals aren't repos.
// It returns the workspace roots it found, the paths it had to skip
// (each with the reason), and a fatal error.
//
// Skipping an unreadable SUBTREE is right — a permission-denied
// directory shouldn't abort a whole home-directory scan — but skipping
// it silently is not: the user gets "0 repos found" with no hint that
// half the tree was unreadable. Those paths now come back for the
// caller to report. A failure on the scan ROOT is still fatal, which is
// what makes the documented exit-1 filesystem-error branch reachable at
// all (would-you-kindly-6gjb).
func scanForBeadsRepos(root string) (found, skippedPaths []string, err error) {
	var out []string
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
	}
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == root {
				// Nothing below this to salvage: an empty result here
				// would read as "scanned fine, found nothing".
				return err
			}
			skippedPaths = append(skippedPaths, fmt.Sprintf("%s: %v", path, err))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		// Skip hidden directories (except the root itself, which
		// might legitimately be named e.g. ~/.config/foo and contain
		// repos).
		if path != root && strings.HasPrefix(name, ".") {
			if name == ".beads" {
				// This IS a bd workspace marker — record the parent
				// and don't descend into the bd internals.
				repoRoot, _ := filepath.EvalSymlinks(filepath.Dir(path))
				if repoRoot == "" {
					repoRoot = filepath.Dir(path)
				}
				out = append(out, repoRoot)
			}
			return filepath.SkipDir
		}
		if skipDirs[name] {
			return filepath.SkipDir
		}
		return nil
	})
	return out, skippedPaths, walkErr
}

// preWykExists reports whether a .pre-wyk preservation file is
// already in place at path. Used to decide whether to write the
// chained wrapper variant of the post-commit hook even when we're
// not moving anything this run (idempotent re-install of a chained
// hook).
func preWykExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
