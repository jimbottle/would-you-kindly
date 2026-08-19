package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// stdinIsTerminal reports whether stdin is an interactive terminal.
// A variable so tests can simulate a TTY without owning one. This is
// a real terminal test (x/term's isatty), NOT a char-device check:
// os.ModeCharDevice is true for /dev/null (safe to read — immediate
// EOF) and false for pipes and sockets (which can block forever), so
// the old guard rejected the conventional `</dev/null` while waving
// an inherited harness socket straight into an unbounded ReadAll
// (would-you-kindly-l51f).
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// defaultStdinTimeout bounds the runbook read when stdin is NOT a
// terminal. A pipe or socket with no writer otherwise blocks forever
// with no output — and a wedged Go process ignores SIGALRM, so
// non-interactive callers (agents, CI) had no cheap outer bound.
// Generous enough for any real `generator | wyk handoff` pipeline;
// overridable via WYK_STDIN_TIMEOUT for the pathological ones.
const defaultStdinTimeout = 30 * time.Second

// stdinTimeoutFromEnv resolves the stdin read deadline from
// WYK_STDIN_TIMEOUT — a Go duration ("45s", "2m") or a bare number
// of seconds ("45") — mirroring how WYK_BD_TIMEOUT is parsed. An
// unparseable or non-positive value falls back to the default.
func stdinTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("WYK_STDIN_TIMEOUT"))
	if raw == "" {
		return defaultStdinTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultStdinTimeout
}

// readStdinRunbook reads the handoff runbook from stdin. A terminal
// read is left unbounded — the caller only reaches here interactively
// via an explicit -allow-empty, and a human typing a runbook must not
// be cut off mid-thought. Every other stdin (pipe, socket, file,
// /dev/null) gets a deadline, because the failure mode of an inherited
// never-closing fd is an unkillable silent hang.
func readStdinRunbook() ([]byte, error) {
	if stdinIsTerminal() {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return b, nil
	}
	return readAllTimeout(os.Stdin, stdinTimeoutFromEnv())
}

// readAllTimeout is io.ReadAll with a wall-clock bound. The read runs
// in a goroutine because SetReadDeadline is not reliable here: an
// inherited stdin is usually a blocking-mode fd the runtime poller
// never registered, where SetReadDeadline reports success or
// ErrNoDeadline depending on platform and fd type. A timer race works
// for every fd type. On timeout the reader goroutine is abandoned
// still blocked in read(2) — acceptable, since the process exits
// immediately after.
func readAllTimeout(r io.Reader, timeout time.Duration) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(r)
		ch <- result{b, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("read stdin: %w", res.err)
		}
		return res.b, nil
	case <-timer.C:
		return nil, fmt.Errorf(
			"timed out after %s waiting for a runbook on stdin — pipe content in, pass -file <path>, or use -allow-empty (WYK_STDIN_TIMEOUT overrides the deadline)",
			timeout)
	}
}
