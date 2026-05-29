package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/updater"
)

// captureStderr mirrors captureStdout for the stderr path that
// runVersionCheck writes to on failure.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestRunVersion_BarePrintsLine(t *testing.T) {
	out := captureStdout(t, func() {
		if code := runVersion(nil); code != 0 {
			t.Errorf("runVersion exit %d, want 0", code)
		}
	})
	if !strings.Contains(out, "wyk") {
		t.Errorf("expected version line to mention wyk; got %q", out)
	}
}

func TestRunVersion_UnknownFlagReturns64(t *testing.T) {
	// Parse errors print usage to stderr; capture so the test log
	// stays clean.
	_ = captureStderr(t, func() {
		if code := runVersion([]string{"--bogus"}); code != 64 {
			t.Errorf("runVersion --bogus exit %d, want 64", code)
		}
	})
}

func TestRunVersion_TrailingArgReturns64(t *testing.T) {
	_ = captureStderr(t, func() {
		if code := runVersion([]string{"--check", "extra"}); code != 64 {
			t.Errorf("runVersion trailing-arg exit %d, want 64", code)
		}
	})
}

func TestRunVersionCheck_FetchFailureReturns2(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return nil, errors.New("network unreachable")
	}
	defer func() { liveFetcher = prev }()

	stderr := captureStderr(t, func() {
		if code := runVersionCheck(); code != 2 {
			t.Errorf("runVersionCheck exit %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "network unreachable") {
		t.Errorf("expected stderr to relay fetch error; got %q", stderr)
	}
}

func TestRunVersionCheck_EmptyFeedReturns2(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return nil, nil
	}
	defer func() { liveFetcher = prev }()

	_ = captureStderr(t, func() {
		if code := runVersionCheck(); code != 2 {
			t.Errorf("empty feed exit %d, want 2", code)
		}
	})
}

func TestRunVersionCheck_NewerAvailableReturns1(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return []updater.Release{{TagName: "v999.0.0", Prerelease: false}}, nil
	}
	defer func() { liveFetcher = prev }()

	out := captureStdout(t, func() {
		if code := runVersionCheck(); code != 1 {
			t.Errorf("newer-available exit %d, want 1", code)
		}
	})
	if !strings.Contains(out, "v999.0.0") {
		t.Errorf("expected output to name the newer tag; got %q", out)
	}
}

func TestRunVersionCheck_StableChannelSkipsPrerelease(t *testing.T) {
	// Stable-pinned user with a prerelease at [0] and older stable
	// below: must compare against the stable, not the prerelease.
	// If the current tag is v0.0.0-dev (unset build), v0.9.0 is
	// still newer, so exit 1 — but the *message* must name the
	// stable tag, not the alpha.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// Plant a "stable" channel preference in the cache.
	if err := updater.PersistLatest([]updater.Release{
		{TagName: "v0.0.1", Prerelease: false},
	}, "stable"); err != nil {
		t.Fatalf("PersistLatest: %v", err)
	}

	prev := liveFetcher
	liveFetcher = func(_ context.Context) ([]updater.Release, error) {
		return []updater.Release{
			{TagName: "v999.9.9-alpha", Prerelease: true},
			{TagName: "v999.0.0", Prerelease: false},
		}, nil
	}
	defer func() { liveFetcher = prev }()

	out := captureStdout(t, func() {
		_ = runVersionCheck()
	})
	if strings.Contains(out, "alpha") {
		t.Errorf("stable channel must not nudge to a prerelease; got %q", out)
	}
	if !strings.Contains(out, "v999.0.0") {
		t.Errorf("expected stable tag in output; got %q", out)
	}
}
