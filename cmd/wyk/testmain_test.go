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

	// Neuter the hook installer for the whole package. The $XDG_CONFIG_HOME
	// floor above only protects repos.json; installHookIn chdirs into a real
	// repo and runs runInit, which writes .git/hooks/post-commit, the
	// conventions block in CLAUDE.md, and .claude/settings.json — inside the
	// developer's own working tree.
	//
	// That became reachable when `wyk doctor -fix` started registering the
	// cwd's workspace: a -fix test that had never touched the hook loop (the
	// registry was empty) suddenly had one entry, the repo under test
	// (roborev #3041). Individual tests still override this to observe the
	// calls; the default just guarantees no test can reach the real thing by
	// accident.
	//
	// It returns 0 while writing NOTHING, which is deliberate and is why it
	// must not be relied on as "the install succeeded": runDoctorFix now
	// verifies the wyk marker actually landed, so a test that reaches the
	// install branch under this default gets a verification failure (exit 1)
	// for reasons unrelated to what it is testing. Any -fix test that lets
	// the hook loop reach a hookless repo must install its own stub via
	// stubInstallHookIn (which writes the marker). The default stays inert
	// because it cannot know whether `dir` is a throwaway repo or the
	// developer's own checkout, and writing into the latter is precisely
	// what this line exists to prevent.
	installHookIn = func(string, ...string) int { return 0 }

	return m.Run()
}
