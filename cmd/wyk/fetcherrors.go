package main

import (
	"errors"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// Sentinel guidance shared by every site that maps a typed bd error
// to a user-facing message — handoffErrExit, runProbe, and the
// multi-repo total-failure path all print these verbatim. hook.go
// keeps its own shorter variants on purpose (hook output is terse
// and prefixed). Hoisted so the `bd init` hint can't drift between
// sites (roborev #2039).
const (
	msgBDNotFound  = "wyk: bd is not installed (or not on PATH)"
	msgNoWorkspace = "wyk: no beads workspace here — run `bd init`"
)

// classifyBDSentinel maps the typed bd sentinels to their documented
// exit code and message. ok=false means err is not a sentinel and the
// caller owns its own generic handling. Pure — printing is the
// caller's job, which is what lets handoffErrExit, runProbe, and
// classifyTotalFetchFailure share it and lets tests assert the text.
func classifyBDSentinel(err error) (code int, msg string, ok bool) {
	switch {
	case errors.Is(err, beads.ErrBDNotFound):
		return 2, msgBDNotFound, true
	case errors.Is(err, beads.ErrNoWorkspace):
		return 2, msgNoWorkspace, true
	}
	return 0, "", false
}

// classifyTotalFetchFailure maps a total multi-repo fetch failure —
// every queried repo errored — to the exit code the multi-repo read
// commands (inbox, stats) document. total=false means at least one
// repo succeeded, or nothing was queried: the caller proceeds with
// its partial-success path. msg is non-empty only for the sentinel
// codes; for generic total failures (code 1, msg "") the caller owns
// emitting its command-specific failure payload (JSON envelope /
// zero stats object / plain text). Pure: the caller prints.
//
// Only the FIRST sub-error is inspected: a mixed total (generic
// first, sentinel later) classifies as generic exit 1. That
// preserves the behavior of the copy-pasted blocks this replaced,
// and bd-missing normally fails every repo identically anyway.
//
// activity is deliberately NOT a caller: its contract is "always
// parseable output, exit 1 on total failure" with no sentinel
// mapping — see runActivity.
func classifyTotalFetchFailure(queried int, subErrs []subError) (code int, msg string, total bool) {
	if queried == 0 || len(subErrs) != queried {
		return 0, "", false
	}
	if code, msg, ok := classifyBDSentinel(subErrs[0].err); ok {
		return code, msg, true
	}
	return 1, "", true
}
