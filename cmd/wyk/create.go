package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/pkg/handoff"
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

// debugLogPath resolves where the TUI should tee its debug log, or ""
// to disable. WYK_LOG_FILE sets an explicit path; otherwise WYK_DEBUG
// (any truthy value) enables logging to "wyk-debug.log" in the cwd.
// (would-you-kindly-2vyt)
func debugLogPath() string {
	if p := strings.TrimSpace(os.Getenv("WYK_LOG_FILE")); p != "" {
		return p
	}
	switch strings.TrimSpace(os.Getenv("WYK_DEBUG")) {
	case "", "0", "false", "no", "off":
		return ""
	default:
		return "wyk-debug.log"
	}
}

// createUsage describes `wyk create`. It is intentionally thin: every
// flag is forwarded verbatim to `bd create`, so the authoritative flag
// reference is bd's own (`bd create --help`).
func createUsage(w *os.File) {
	fmt.Fprintln(w, "usage: wyk create <bd create args...>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Thin wrapper over `bd create`: forwards every argument verbatim, then")
	fmt.Fprintln(w, "stamps the new issue with its provenance — `src:agent` when a Claude")
	fmt.Fprintln(w, "session filed it (else `src:human`), so it matches `wyk inbox` — plus a")
	fmt.Fprintln(w, "`session:<id>` label (from $"+sessionEnvVar+") for the TUI's Session")
	fmt.Fprintln(w, "column. Use it instead of `bd create` so both are applied.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, `  wyk create --title "Fix the flaky test" --type=bug -a alice`)
	fmt.Fprintln(w, `  wyk create "Quick task" --priority=1`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `bd create --help` for the full flag reference.")
}

// runBDCreateWithLabels is the seam `runCreate` calls to do the actual bd
// work: file the issue (returning its ID) then stamp each non-empty label
// in order. Production wires it to beads.Client; tests swap a stub so they
// can assert the forwarded args + labels without a real bd.
var runBDCreateWithLabels = realBDCreateWithLabels

func realBDCreateWithLabels(dir string, passthrough []string, labels []string) (string, error) {
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
	for _, l := range labels {
		if l == "" {
			continue
		}
		if lerr := c.AddLabel(ctx, id, l); lerr != nil {
			// The issue exists; only a label stamp failed. Surface which
			// label but return the ID so the caller can still report the
			// create itself succeeded (partial success).
			return id, fmt.Errorf("created %s but failed to stamp label %q: %w", id, l, lerr)
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
	// Every wyk-filed issue carries a src: provenance label (docs/CONTRACT.md):
	// src:agent when a Claude session filed it, src:human otherwise. Without
	// it an agent-filed task never matches `wyk inbox` (would-you-kindly-voef).
	// The session env var IS the agent-context discriminator. The src: label
	// goes first, then the session:<id> stamp (when in a session).
	labels := []string{srcLabelForSession(session)}
	if sl := sessionLabel(session); sl != "" {
		labels = append(labels, sl)
	}

	id, err := runBDCreateWithLabels("", args, labels)
	if id == "" {
		// The create itself failed — nothing was filed.
		fmt.Fprintln(os.Stderr, "wyk create:", err)
		return 1
	}
	// The issue exists. stdout uniformly carries the "created <id>" line
	// regardless of exit code, so a caller parses it the same way whether
	// or not the labels stamped; the error (if any) goes to stderr and only
	// the exit code distinguishes partial success.
	switch {
	case err != nil:
		// Partial success: created, but a label didn't stamp.
		fmt.Printf("wyk create: created %s (labels NOT fully stamped)\n", id)
		fmt.Fprintln(os.Stderr, "wyk create:", err)
		return 1
	default:
		fmt.Printf("wyk create: created %s (%s)\n", id, displayLabels(labels))
	}
	return 0
}

// displayLabels renders the stamped labels for the success line, shortening
// any session:<id> to its leading 8 chars so a full session ID doesn't land
// on stdout / in logs — the full value is still stamped on the issue. Keeps
// the short-display behavior a prior commit added while now also surfacing
// the src: provenance label.
func displayLabels(labels []string) string {
	out := make([]string, len(labels))
	for i, l := range labels {
		if id, ok := strings.CutPrefix(l, sessionLabelPrefix); ok && len(id) > 8 {
			out[i] = sessionLabelPrefix + id[:8]
			continue
		}
		out[i] = l
	}
	return strings.Join(out, ", ")
}

// srcLabelForSession returns the provenance label for a create: src:agent
// when a Claude session ID is present (an agent is filing), src:human
// otherwise. Mirrors the agent/person split in docs/CONTRACT.md.
func srcLabelForSession(session string) string {
	if session != "" {
		return handoff.SrcAgentLabel
	}
	return handoff.SrcHumanLabel
}
