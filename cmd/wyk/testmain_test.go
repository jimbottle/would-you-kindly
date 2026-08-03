package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points $XDG_CONFIG_HOME at a throwaway directory for the whole
// package unless the caller already set one.
//
// Several commands now register the cwd's bd workspace as a side effect of
// a write (`wyk create`, `wyk handoff`, `-C`, `wyk doctor -fix` — see
// would-you-kindly-afo3), and the test binary runs inside this repo, which
// IS a bd workspace. Without this, running `go test ./cmd/wyk` would write
// to the developer's real ~/.config/wyk/repos.json.
//
// Tests that need their own registry still call t.Setenv themselves; this
// only establishes a safe floor. Individual t.Setenv calls override it and
// are restored per-test as usual.
func TestMain(m *testing.M) {
	// os.Exit skips defers, so the run happens in a helper that can clean
	// up through its own — TestMain does nothing but propagate the code.
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		dir, err := os.MkdirTemp("", "wyk-test-xdg-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "TestMain: create temp XDG_CONFIG_HOME:", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
			fmt.Fprintln(os.Stderr, "TestMain: set XDG_CONFIG_HOME:", err)
			return 1
		}
	}
	return m.Run()
}
