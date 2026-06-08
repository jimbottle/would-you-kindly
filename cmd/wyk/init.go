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
// Exit codes:
//
//	0   installed / already installed (or, with -dry-run, would have)
//	1   filesystem, git, or bd error
//	2   .git directory missing — not a git repo
//	64  usage error or refusal to overwrite a foreign hook without -force
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing post-commit hook (destructive — drops the existing hook entirely)")
	chain := fs.Bool("chain", false, "preserve an existing post-commit hook and chain wyk's logic after it (preferred over -force when the existing hook is from another tool like roborev)")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing the hook")
	skipBD := fs.Bool("skip-bd-init", false, "do not run `bd init` even if .beads is missing")
	skipRegister := fs.Bool("skip-register", false, "do not add this repo to ~/.config/wyk/repos.json")
	skipClaudeMD := fs.Bool("skip-claude-md", false, "do not seed the agent enrichment: wyk's conventions block in CLAUDE.md AND the bd-create-guard PreToolUse hook in .claude/settings.json (which redirects `bd create` to `wyk create`)")
	scanRoot := fs.String("scan", "", "scan this directory tree for existing bd workspaces and register every one found (skips repos already registered, hidden dirs, node_modules, vendor); mutually exclusive with the per-repo init path")
	uninstall := fs.Bool("uninstall", false, "remove wyk's post-commit hook (restoring post-commit.pre-wyk if present); refuses on foreign hooks")
	fixForeignHooks := fs.Bool("fix-foreign-hooks", false, "scan the registered repos for foreign post-commit hooks and chain wyk after each (idempotent; wyk-installed and missing hooks are left alone)")
	installSkills := fs.Bool("skills", false, "also install wyk's agent skills into ~/.claude/skills (idempotent; like `wyk skills install`). Modified skills are left alone.")
	fs.SetOutput(os.Stderr)
	// Lead the help with the bare happy path so the common case isn't
	// buried under the alternate modes and the alphabetical flag dump
	// (would-you-kindly-5rgd). The alternate modes (-scan / -uninstall /
	// -fix-foreign-hooks) would read more naturally as subcommands; that
	// is a deliberate post-1.0 change (it reshapes the CLI surface), so
	// for now they stay flags but are grouped distinctly here.
	fs.Usage = func() { printInitUsage(fs) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk init [-force | -chain] [-dry-run] [-skip-bd-init] [-skip-register] [-skip-claude-md]")
		fmt.Fprintln(os.Stderr, "   or: wyk init -scan <root> [-dry-run]")
		fmt.Fprintln(os.Stderr, "   or: wyk init -uninstall [-dry-run]")
		fmt.Fprintln(os.Stderr, "   or: wyk init -fix-foreign-hooks [-dry-run]")
		return 64
	}
	if *force && *chain {
		fmt.Fprintln(os.Stderr, "wyk init: -force and -chain are mutually exclusive")
		return 64
	}
	if *fixForeignHooks {
		// -fix-foreign-hooks is a registry-wide alternate mode; reject
		// combinations that only make sense in the per-repo install
		// path so the user gets a clear error rather than silent
		// ignores.
		var bad []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "force", "chain", "skip-bd-init", "skip-register", "skip-claude-md", "scan", "uninstall":
				bad = append(bad, "-"+f.Name)
			}
		})
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
		var bad []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "force", "chain", "skip-bd-init", "skip-register", "skip-claude-md", "scan":
				bad = append(bad, "-"+f.Name)
			}
		})
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
		var bad []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "force", "chain", "skip-bd-init", "skip-register", "skip-claude-md":
				bad = append(bad, "-"+f.Name)
			}
		})
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

	// Resolve the hooks dir git ACTUALLY runs, up front. resolveGitHookPath
	// follows core.hooksPath, so an in-repo redirect (e.g. bd's .beads/hooks)
	// gets wyk's hook where it'll fire instead of a dead file in .git/hooks;
	// worktrees / gitlinks resolve correctly too. With core.hooksPath unset
	// this is the usual .git/hooks/post-commit. Doing it BEFORE bd-init and
	// the enrichment steps means an out-of-repo (stale) core.hooksPath is
	// refused before any state is mutated, not half-way through. coreHooksPath
	// gates the refusal so a normal worktree (whose shared hooks legitimately
	// live outside the worktree root, but with no core.hooksPath set) isn't
	// mistaken for a redirect.
	// Up-front: run the guard for its side effect — refuse a pre-existing
	// out-of-repo stale redirect BEFORE mutating any state. The resolved
	// path is intentionally discarded; the authoritative resolution
	// happens after `bd init` (which can repoint core.hooksPath).
	if _, code := resolveAndGuardHookPath(repoRoot); code != 0 {
		return code
	}

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
	if !*skipClaudeMD {
		action, err := seedWykConventions(repoRoot, *dryRun)
		if err != nil {
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
			fmt.Fprintln(os.Stderr, "wyk init: .claude/settings.json:", sErr)
			fmt.Fprintln(os.Stderr, "wyk init: continuing — best-effort enrichment")
		} else {
			fmt.Println("wyk init:", sAction)
		}
	}

	// Re-resolve the active hook path NOW, after bd init. The up-front
	// resolution (used only to refuse a pre-existing out-of-repo stale
	// redirect before we mutate anything) predates `bd init`, which
	// points core.hooksPath at .beads/hooks. On a fresh repo that means
	// the up-front path is .git/hooks/post-commit while git now reads
	// from .beads/hooks — so writing there would install wyk's auto-close
	// hook into a directory git ignores, and Closes:/Fixes: would
	// silently never fire. Re-resolving makes the hook land where git
	// actually looks. Re-run the within-repo guard in case bd (or a
	// pre-existing config) pointed the redirect outside the repo: better
	// to fail loud than install into a bypassed path.
	hookPath, code := resolveAndGuardHookPath(repoRoot)
	if code != 0 {
		return code
	}

	// hookPath now reflects git's active hooks dir (re-resolved above,
	// after bd init may have set core.hooksPath).
	preWykPath := hookPath + ".pre-wyk"

	// Step 2: install the post-commit hook. Each branch sets
	// `skipWrite` rather than returning early so step 3 (registry)
	// still runs — that's what makes init idempotent on repos where
	// the hook is already in place but the registry write previously
	// failed. `chainMove` is set when we need to move an existing
	// foreign hook to its .pre-wyk preservation slot.
	skipWrite := false
	chainMove := false
	switch existing, err := os.ReadFile(hookPath); {
	case err == nil:
		if bytes.Contains(existing, []byte(hookMarker)) {
			if *dryRun {
				fmt.Printf("wyk init: would reinstall %s (existing hook is from a previous `wyk init`)\n", hookPath)
				skipWrite = true
			} else if !*force && !*chain {
				fmt.Println("wyk init: post-commit hook already installed (use -force to reinstall)")
				skipWrite = true
			}
		} else {
			// Foreign hook. Three options: refuse (default), overwrite
			// (-force, destructive), or chain (-chain, preserves the
			// original at .pre-wyk and runs both).
			if *dryRun {
				switch {
				case *chain:
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
				case *force:
					fmt.Printf("wyk init: would overwrite foreign hook at %s (-force)\n", hookPath)
				default:
					fmt.Printf("wyk init: would refuse to overwrite foreign hook at %s\n", hookPath)
					fmt.Println("  Re-run with -chain to keep both hooks, or -force to replace.")
				}
				skipWrite = true
			} else if *chain {
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
			} else if !*force {
				fmt.Fprintf(os.Stderr,
					"wyk init: refusing to overwrite existing %s\n  Use -chain to keep both hooks, or -force to replace.\n",
					hookPath)
				return 64
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

	// Pick the hook script body to write: chained wrapper (when -chain
	// was just applied OR a previously-chained install is being
	// re-applied) or the plain hook.
	hookBody := postCommitHook
	if chainMove || preWykExists(preWykPath) {
		hookBody = chainedPostCommitHook
	}

	if !skipWrite {
		if *dryRun {
			fmt.Printf("wyk init: would install %s\n", hookPath)
		} else {
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
		}
	}

	// Step 3: register the repo so wyk's multi-repo TUI finds it.
	// Runs on EVERY init, including when the hook step was skipped —
	// that's the idempotency guarantee the doc promises.
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
	return 0
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
	if !filepath.IsAbs(gitDir) {
		// `git rev-parse --git-dir` may emit a relative path when run
		// from inside the working tree; resolve against cwd.
		cwd, werr := os.Getwd()
		if werr != nil {
			return "", "", fmt.Errorf("getwd: %w", werr)
		}
		gitDir = filepath.Join(cwd, gitDir)
	}
	if gitDir == "" || repoRoot == "" {
		return "", "", errors.New("git rev-parse returned empty paths")
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
const rememberedConventionMemory = "wyk convention: a human task carries label=human + label=src:agent; agent-owned just label=src:agent. Inbox = `" + agentInboxQuery + "` (run `wyk inbox`) — WORK returned items, don't just note them. Skip HUMAN-BLOCK rows (agent task with a human-flagged dep) and AGENT-HANDOFF rows (label=agent-handoff = another agent's; a human coordinates). File/hand off a human task with `wyk handoff <id>` (or `wyk handoff -create \"<title>\"`), never hand-rolled labels. Statuses: open(default)/in_progress/blocked(+--add-dependency)/deferred(subsystem not ready, hidden from bd ready)/closed; prefer deferred over holding a task open. Full text + runbook format: `wyk conventions`."

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
		if werr := os.WriteFile(path, []byte(claudeMDPreamble+wykConventionsBlock+"\n"), 0o644); werr != nil {
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
		if werr := os.WriteFile(path, []byte(updated), 0o644); werr != nil {
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
	if werr := os.WriteFile(path, []byte(prefix+wykConventionsBlock+"\n"), 0o644); werr != nil {
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
	root := map[string]any{}
	switch b, err := os.ReadFile(path); {
	case errors.Is(err, os.ErrNotExist):
		// fresh file
	case err != nil:
		return "", err
	default:
		if uerr := json.Unmarshal(b, &root); uerr != nil {
			return "", fmt.Errorf("parse %s: %w", path, uerr)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	if claudeSettingsHasHook(root, claudeSettingsHook) {
		return "bd-create-guard hook already in .claude/settings.json", nil
	}
	if dryRun {
		return "would register the bd-create-guard PreToolUse hook in .claude/settings.json", nil
	}
	addPreToolUseHook(root, claudeSettingsHook)
	out, merr := json.MarshalIndent(root, "", "  ")
	if merr != nil {
		return "", merr
	}
	out = append(out, '\n')
	if err := writeFileAtomic(path, out, 0o644); err != nil {
		return "", err
	}
	return "registered the bd-create-guard PreToolUse hook in .claude/settings.json", nil
}

// writeFileAtomic writes data to path via a sibling temp file + rename so
// an interrupted write can't truncate or corrupt an existing file —
// matching the durability the registry/session writers use. This matters
// for settings.json, which already holds bd's SessionStart/PreCompact
// hooks the merge works to preserve. Creates the parent dir on demand.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings.json.*")
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

// claudeSettingsHasHook reports whether any PreToolUse hook command in the
// parsed settings equals cmd — the idempotency check for seedClaudeSettings.
func claudeSettingsHasHook(root map[string]any, cmd string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
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

// addPreToolUseHook appends a Bash-matched PreToolUse entry running cmd,
// creating the hooks / PreToolUse containers as needed. Preserves any
// existing hook entries (e.g. bd's SessionStart/PreCompact).
func addPreToolUseHook(root map[string]any, cmd string) {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	entries, _ := hooks["PreToolUse"].([]any)
	entries = append(entries, map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": cmd,
		}},
	})
	hooks["PreToolUse"] = entries
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

// resolveAndGuardHookPath resolves repoRoot's active post-commit hook
// path and refuses an out-of-repo core.hooksPath redirect. It returns
// (path, 0) to continue, or ("", code) where code is the exit status
// runInit should return (1 = resolve failure, 64 = stale out-of-repo
// redirect). runInit calls it twice — once up front (to refuse a
// pre-existing stale redirect before mutating state) and once after
// `bd init` (which can repoint core.hooksPath at .beads/hooks) — so the
// resolution and the guard live in one place rather than drifting across
// two near-identical copies.
func resolveAndGuardHookPath(repoRoot string) (string, int) {
	hookPath, herr := resolveGitHookPath(repoRoot, "post-commit")
	if herr != nil {
		fmt.Fprintln(os.Stderr, "wyk init: resolve hook path:", herr)
		return "", 1
	}
	if _, set := coreHooksPath(repoRoot); set && !pathWithin(repoRoot, filepath.Dir(hookPath)) {
		fmt.Fprintf(os.Stderr, "wyk init: git's core.hooksPath points outside this repo:\n          %s\n", filepath.Dir(hookPath))
		fmt.Fprintln(os.Stderr, "          That's almost certainly stale — clear it and re-run wyk init:")
		fmt.Fprintln(os.Stderr, "          git -C "+repoRoot+" config --unset core.hooksPath")
		return "", 64
	}
	return hookPath, 0
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
// post-commit hook wyk installs in .git/hooks. installDir is wyk's
// install target (the repo's default hooks dir). Returns redirected =
// true when git will run hooks from somewhere wyk's hook is NOT, with a
// human-facing reason and whether the active dir is inside the repo
// (which decides the remediation: install-there vs unset-the-stale-config).
func hooksPathRedirect(repoDir string) (active string, redirected, insideRepo, wykHookActive bool) {
	hp, set := coreHooksPath(repoDir)
	if !set {
		return "", false, false, false
	}
	activePost, err := resolveGitHookPath(repoDir, "post-commit")
	if err != nil {
		return hp, true, false, false
	}
	activeDir := filepath.Dir(activePost)
	insideRepo = pathWithin(repoDir, activeDir)
	if body, rerr := os.ReadFile(activePost); rerr == nil {
		wykHookActive = bytes.Contains(body, []byte(hookMarker))
	}
	return activeDir, true, insideRepo, wykHookActive
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

	found, err := scanForBeadsRepos(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init -scan: walk:", err)
		return 1
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
func scanForBeadsRepos(root string) ([]string, error) {
	var out []string
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Permission errors and unreadable directories: skip
			// silently rather than abort the whole scan. The user
			// can fix and re-run.
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
	return out, err
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
