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

// runInit implements `wyk init`: installs a post-commit hook into
// the current git repository so commits like "Closes: bd-42" auto-
// close the referenced beads issue.
//
// Exit codes:
//
//	0   installed (or, with -dry-run, would have installed)
//	1   filesystem or git error
//	2   .git directory missing — not a git repo
//	64  usage error or refusal to overwrite a foreign hook without -force
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing post-commit hook")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing the hook")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk init [-force] [-dry-run]")
		return 64
	}

	gitDir, err := findGitDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk init:", err)
		return 2
	}
	hookPath := filepath.Join(gitDir, "hooks", "post-commit")

	switch existing, err := os.ReadFile(hookPath); {
	case err == nil:
		if bytes.Contains(existing, []byte(hookMarker)) {
			if !*force && !*dryRun {
				fmt.Println("wyk init: post-commit hook already installed (use -force to reinstall)")
				return 0
			}
		} else if !*force {
			fmt.Fprintf(os.Stderr,
				"wyk init: refusing to overwrite existing %s\n  (use -force to replace it)\n",
				hookPath)
			return 64
		}
	case !errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(os.Stderr, "wyk init: stat hook:", err)
		return 1
	}

	if *dryRun {
		fmt.Printf("wyk init: would install %s\n", hookPath)
		return 0
	}

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
	return 0
}

// findGitDir returns the absolute path to the current repo's .git
// directory (or the worktree's gitdir file's target). Uses git
// rev-parse so worktrees and detached gitdirs work without bespoke
// path-walking.
func findGitDir() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = err.Error()
		}
		return "", fmt.Errorf("not a git repository (%s)", errOut)
	}
	dir := strings.TrimSpace(stdout.String())
	if dir == "" {
		return "", errors.New("git rev-parse returned empty git-dir")
	}
	// git rev-parse may emit a relative path; resolve against cwd.
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		dir = filepath.Join(cwd, dir)
	}
	return dir, nil
}
