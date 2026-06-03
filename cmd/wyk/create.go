package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// sessionEnvVar is the environment variable Claude Code exports with the
// current session's ID. `wyk create` reads it to stamp the new issue
// with which conversation filed it — the capture half of the TUI's
// Session column. Empty (e.g. outside Claude Code) means "no session to
// record", which is not an error: the issue is still created.
const sessionEnvVar = "CLAUDE_CODE_SESSION_ID"

// sessionLabelPrefix is the bd label namespace under which the Claude
// session ID is recorded (`session:<id>`). The TUI's Session column
// reads the same prefix (internal/tui.sessionLabelPrefix) — keep the two
// in sync.
const sessionLabelPrefix = "session:"

// sessionLabel returns the `session:<id>` label for a (trimmed) session
// ID, or "" when the ID is empty. Shared by `wyk create` and `wyk handoff
// -create` so any issue filed through wyk records which conversation
// filed it; an empty session (e.g. outside Claude Code) records nothing.
func sessionLabel(session string) string {
	if session == "" {
		return ""
	}
	return sessionLabelPrefix + session
}

// sessionLabelFromEnv is sessionLabel applied to $CLAUDE_CODE_SESSION_ID.
func sessionLabelFromEnv() string {
	return sessionLabel(strings.TrimSpace(os.Getenv(sessionEnvVar)))
}

// createUsage describes `wyk create`. It is intentionally thin: every
// flag is forwarded verbatim to `bd create`, so the authoritative flag
// reference is bd's own (`bd create --help`).
func createUsage(w *os.File) {
	fmt.Fprintln(w, "usage: wyk create <bd create args...>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Thin wrapper over `bd create`: forwards every argument verbatim, then")
	fmt.Fprintln(w, "stamps the new issue with a `session:<id>` label recording the Claude")
	fmt.Fprintln(w, "session that filed it (from $"+sessionEnvVar+"). Use it instead of")
	fmt.Fprintln(w, "`bd create` so the TUI's Session column is populated.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, `  wyk create --title "Fix the flaky test" --type=bug -a alice`)
	fmt.Fprintln(w, `  wyk create "Quick task" --priority=1`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `bd create --help` for the full flag reference.")
}

// runBDCreateWithSession is the seam `runCreate` calls to do the actual
// bd work: file the issue (returning its ID) and, when sessionLabel is
// non-empty, stamp it. Production wires it to beads.Client; tests swap a
// stub so they can assert the forwarded args + label without a real bd.
var runBDCreateWithSession = realBDCreateWithSession

func realBDCreateWithSession(dir string, passthrough []string, sessionLabel string) (string, error) {
	c := beads.NewClient()
	c.Dir = dir
	ctx := context.Background()

	// Forward the user's args verbatim. --silent makes bd emit only the
	// new ID on stdout (clean to capture); --dolt-auto-commit=on is the
	// project's mandatory write flag. Add each only if absent so a user
	// who passed it themselves doesn't get a duplicate.
	args := append([]string{"create"}, passthrough...)
	if !hasFlag(passthrough, "--silent") {
		args = append(args, "--silent")
	}
	if !hasFlag(passthrough, "--dolt-auto-commit") {
		args = append(args, "--dolt-auto-commit=on")
	}

	out, err := c.RawRun(ctx, args)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("bd create returned no issue ID")
	}
	if sessionLabel != "" {
		if lerr := c.AddLabel(ctx, id, sessionLabel); lerr != nil {
			// The issue exists; only the session stamp failed. Surface it
			// but return the ID so the caller can still report success of
			// the create itself.
			return id, fmt.Errorf("created %s but failed to stamp the session label: %w", id, lerr)
		}
	}
	return id, nil
}

// hasFlag reports whether args contains the given flag in any accepted
// form — -flag, --flag, -flag=value, or --flag=value. Leading dashes on
// both `flag` and each arg are normalised away before comparison, so the
// single-dash form (which Go's flag package also accepts) is detected
// too and we don't append a duplicate that could override an explicit
// value (e.g. a user's `-dolt-auto-commit=off`).
func hasFlag(args []string, flag string) bool {
	name := strings.TrimLeft(flag, "-")
	for _, a := range args {
		bare := strings.TrimLeft(a, "-")
		if bare == name || strings.HasPrefix(bare, name+"=") {
			return true
		}
	}
	return false
}

// runCreate implements `wyk create`: a thin `bd create` wrapper that
// records the Claude session in a `session:` label so the TUI can show
// which conversation filed each task.
//
// Exit codes: 0 created (session stamped when available); 1 bd error or
// session-stamp failure; 64 usage (-h/--help).
func runCreate(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			createUsage(os.Stdout)
			return 0
		}
	}
	if len(args) == 0 {
		createUsage(os.Stderr)
		return 64
	}

	session := strings.TrimSpace(os.Getenv(sessionEnvVar))
	label := sessionLabel(session)

	id, err := runBDCreateWithSession("", args, label)
	if id == "" {
		// The create itself failed — nothing was filed.
		fmt.Fprintln(os.Stderr, "wyk create:", err)
		return 1
	}
	// The issue exists. stdout uniformly carries the "created <id>" line
	// regardless of exit code, so a caller parses it the same way whether
	// or not the session stamp succeeded; the error (if any) goes to
	// stderr and only the exit code distinguishes partial success.
	switch {
	case err != nil:
		// Partial success: created, but the session label didn't stamp.
		fmt.Printf("wyk create: created %s (session NOT recorded)\n", id)
		fmt.Fprintln(os.Stderr, "wyk create:", err)
		return 1
	case label != "":
		fmt.Printf("wyk create: created %s (session %s)\n", id, shortSession(session))
	default:
		fmt.Printf("wyk create: created %s (no %s in env — session not recorded)\n", id, sessionEnvVar)
	}
	return 0
}

// shortSession trims a session ID to its leading 8 chars for display in
// the success line; the full value lives in the label.
func shortSession(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
