package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// classifyTotalFetchFailure maps a total multi-repo fetch failure —
// every queried repo errored — to the exit code the multi-repo read
// commands (inbox, stats) document, printing the actionable message
// for the typed bd sentinels (2 = bd missing / no workspace, matching
// wyk handoff). total=false means at least one repo succeeded, or
// nothing was queried: the caller proceeds with its partial-success
// path. When total=true with code 1, the caller still owns emitting
// its command-specific failure payload (JSON envelope / zero stats
// object / plain text) before returning the code — the payloads
// differ per command on purpose, the classification must not.
//
// activity is deliberately NOT a caller: its contract is "always
// parseable output, exit 1 on total failure" with no sentinel
// mapping — see runActivity.
func classifyTotalFetchFailure(queried int, subErrs []subError) (code int, total bool) {
	if queried == 0 || len(subErrs) != queried {
		return 0, false
	}
	first := subErrs[0].err
	switch {
	case errors.Is(first, beads.ErrBDNotFound):
		fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
		return 2, true
	case errors.Is(first, beads.ErrNoWorkspace):
		fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
		return 2, true
	}
	return 1, true
}
