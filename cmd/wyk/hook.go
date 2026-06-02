package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// closeRefRE matches lines of a commit message that signal an
// auto-close. The convention follows what most issue trackers
// recognise — Closes / Fixes / Resolves, case-insensitive, optionally
// suffixed with ":" or "#", followed by an ID that looks like a bd
// issue (lowercase + digits + hyphens, with optional ".N" suffixes
// for hierarchical IDs like would-you-kindly-ma5.4).
//
// Anchoring to line-start (multiline mode) keeps a stray "closes:"
// inside a code block or sentence from triggering a real close. The
// trailing `\s*$` anchor is intentional: it enforces ONE ID PER LINE.
// A trailer like "Closes: bd-1, bd-2" matches nothing — to close
// both, use two separate Closes lines. This avoids false positives
// from prose like "Closes: bd-1 (we'll handle bd-2 next week)" where
// the second token isn't really a close target.
var closeRefRE = regexp.MustCompile(`(?im)^[\s>]*(?:closes|fixes|resolves)[:\s#]+([a-z][a-z0-9-]*(?:\.[a-z0-9-]+)*)\s*$`)

// parseCloseRefs returns the issue IDs the commit message asks the
// hook to auto-close, in the order they appear, with duplicates
// removed. A purely lexical scan — no validation against bd happens
// here.
func parseCloseRefs(commitMessage string) []string {
	matches := closeRefRE.FindAllStringSubmatch(commitMessage, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	var out []string
	for _, m := range matches {
		id := strings.ToLower(m[1])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// runHook is the top-level dispatcher for `wyk hook <subcommand>`.
// Only post-commit is implemented today; the indirection leaves room
// for pre-commit / pre-push variants without renaming the public CLI.
func runHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk hook <post-commit> [args]")
		return 64
	}
	switch args[0] {
	case "post-commit":
		return runHookPostCommit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "wyk hook: unknown subcommand %q\n", args[0])
		return 64
	}
}

// runHookPostCommit is invoked by .git/hooks/post-commit (installed
// by `wyk init`). It reads the commit message from HEAD (or a
// supplied SHA, useful for testing), extracts auto-close references,
// and calls `bd close` on each.
//
// The function never returns non-zero on per-issue close failures —
// a post-commit hook running after the commit has already landed
// should never make the user's terminal look "failed". Each failure
// is printed and we move on.
//
// Exit codes:
//
//	0   success (or partial success — see above)
//	1   the commit-message read itself failed
//	2   bd missing or no workspace (rare — bd would have already failed
//	    the commit if it cared, but kept for parity with other modes)
//	64  usage error
func runHookPostCommit(args []string) int {
	fs := flag.NewFlagSet("hook post-commit", flag.ContinueOnError)
	dir := fs.String("C", "", "run as if bd had been started in this directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}

	ref := "HEAD"
	if fs.NArg() == 1 {
		ref = fs.Arg(0)
	} else if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: wyk hook post-commit [-C <dir>] [<commit-sha>]")
		return 64
	}

	// -C steers both the bd workspace AND the git repo we read the
	// commit message from. They must match — closing IDs from one
	// repo's git log against another repo's bd workspace is exactly
	// the kind of cross-talk the hook should never do.
	msg, err := commitMessage(*dir, ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk hook post-commit: read commit message:", err)
		return 1
	}

	ids := parseCloseRefs(msg)
	if len(ids) == 0 {
		// Silent on the "nothing to do" path so the post-commit hook
		// doesn't clutter normal commits.
		return 0
	}

	client := beads.NewClient()
	client.Dir = *dir

	for _, id := range ids {
		if err := client.Close(context.Background(), id); err != nil {
			switch {
			case errors.Is(err, beads.ErrBDNotFound):
				fmt.Fprintln(os.Stderr, "wyk hook: bd is not installed")
				return 2
			case errors.Is(err, beads.ErrNoWorkspace):
				fmt.Fprintln(os.Stderr, "wyk hook: no beads workspace here")
				return 2
			default:
				// Per-issue failures (already closed, unknown ID, …)
				// shouldn't fail the hook. bd's stderr is already in
				// the error text.
				fmt.Fprintf(os.Stderr, "wyk hook: close %s: %v\n", id, err)
				continue
			}
		}
		fmt.Printf("wyk hook: closed %s\n", id)
	}
	return 0
}

// commitMessage reads the full message of the given git ref. Uses
// `git show -s --format=%B` because it returns the body cleanly
// without needing to parse other show output. If dir is non-empty,
// git itself is invoked with -C <dir> so the read targets the same
// repo the bd client will write against.
func commitMessage(dir, ref string) (string, error) {
	args := []string{"show", "-s", "--format=%B", ref}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
