package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// hookMarker identifies a wyk-installed hook so re-running init
// without -force can refuse safely when the existing hook is from
// some other source.
const hookMarker = "Installed by `wyk init`"

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
	force := fs.Bool("force", false, "overwrite an existing post-commit hook")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing the hook")
	skipBD := fs.Bool("skip-bd-init", false, "do not run `bd init` even if .beads is missing")
	skipRegister := fs.Bool("skip-register", false, "do not add this repo to ~/.config/wyk/repos.json")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk init [-force] [-dry-run] [-skip-bd-init] [-skip-register]")
		return 64
	}

	gitDir, repoRoot, err := findGitPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
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
	}

	hookPath := filepath.Join(gitDir, "hooks", "post-commit")

	// Step 2: install the post-commit hook. Each branch sets
	// `skipWrite` rather than returning early so step 3 (registry)
	// still runs — that's what makes init idempotent on repos where
	// the hook is already in place but the registry write previously
	// failed.
	skipWrite := false
	switch existing, err := os.ReadFile(hookPath); {
	case err == nil:
		if bytes.Contains(existing, []byte(hookMarker)) {
			if *dryRun {
				fmt.Printf("wyk init: would reinstall %s (existing hook is from a previous `wyk init`)\n", hookPath)
				skipWrite = true
			} else if !*force {
				fmt.Println("wyk init: post-commit hook already installed (use -force to reinstall)")
				skipWrite = true
			}
		} else {
			// Foreign hook. Dry-run is observation-only and must not
			// return the usage exit code — describe what the real run
			// would do and continue.
			if *dryRun {
				if *force {
					fmt.Printf("wyk init: would overwrite foreign hook at %s (-force)\n", hookPath)
				} else {
					fmt.Printf("wyk init: would refuse to overwrite foreign hook at %s (re-run with -force to replace)\n", hookPath)
				}
				skipWrite = true
			} else if !*force {
				fmt.Fprintf(os.Stderr,
					"wyk init: refusing to overwrite existing %s\n  (use -force to replace it)\n",
					hookPath)
				return 64
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(os.Stderr, "wyk init: stat hook:", err)
		return 1
	}

	if !skipWrite {
		if *dryRun {
			fmt.Printf("wyk init: would install %s\n", hookPath)
		} else {
			if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
				fmt.Fprintln(os.Stderr, "wyk init: mkdir hooks dir:", err)
				return 1
			}
			if err := os.WriteFile(hookPath, []byte(postCommitHook), 0o755); err != nil {
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
