package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// swapStdin replaces os.Stdin with the given file for the duration of
// the test. runHandoff reads the package-level os.Stdin directly, so
// the tests steer it the same way a shell redirect would.
func swapStdin(t *testing.T, f *os.File) {
	t.Helper()
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old })
}

// stubStdinTerminal forces the terminal check to the given answer.
// Tests always run with a pipe/file stdin, so exercising the TTY
// branch requires the stub; restoring is registered as cleanup.
func stubStdinTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return isTerminal }
	t.Cleanup(func() { stdinIsTerminal = old })
}

// captureHandoffStderr mirrors captureHandoffStdout for the stderr
// stream, where the guard and timeout diagnostics land.
func captureHandoffStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestHandoff_DevNullStdinIsNotRejectedAsTerminal(t *testing.T) {
	clearAmbientIdentity(t)
	// `wyk handoff <id> </dev/null` is the conventional non-interactive
	// "no input". The old char-device guard rejected it as "a TTY";
	// it must instead read instant EOF and fail on the EMPTY-runbook
	// check — the accurate, actionable diagnostic.
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	swapStdin(t, devnull)

	var code int
	errOut := captureHandoffStderr(t, func() {
		code = runHandoff([]string{"-dry-run", "wyk-42"})
	})
	if code != 64 {
		t.Errorf("exit = %d, want 64 (empty runbook without -allow-empty)", code)
	}
	if !strings.Contains(errOut, "empty runbook") {
		t.Errorf("stderr should complain about the empty runbook, got:\n%s", errOut)
	}
	if strings.Contains(errOut, "terminal") || strings.Contains(errOut, "TTY") {
		t.Errorf("/dev/null must not be diagnosed as a terminal, got:\n%s", errOut)
	}
}

func TestHandoff_DevNullWithAllowEmptySucceeds(t *testing.T) {
	clearAmbientIdentity(t)
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	swapStdin(t, devnull)

	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-allow-empty", "wyk-42"}); code != 0 {
			t.Errorf("exit = %d, want 0 for </dev/null with -allow-empty", code)
		}
	})
	if !strings.Contains(out, "would hand off wyk-42 to human") {
		t.Errorf("dry-run plan missing from output:\n%s", out)
	}
}

func TestHandoff_TerminalStdinRefusedWithoutAllowEmpty(t *testing.T) {
	clearAmbientIdentity(t)
	stubStdinTerminal(t, true)

	var code int
	errOut := captureHandoffStderr(t, func() {
		code = runHandoff([]string{"-dry-run", "wyk-42"})
	})
	if code != 64 {
		t.Errorf("exit = %d, want 64 for terminal stdin without -allow-empty", code)
	}
	if !strings.Contains(errOut, "stdin is a terminal") {
		t.Errorf("stderr should name the terminal refusal, got:\n%s", errOut)
	}
}

func TestHandoff_SilentPipeStdinTimesOutInsteadOfHanging(t *testing.T) {
	clearAmbientIdentity(t)
	// An inherited pipe/socket with no writer used to pass the guard
	// and block io.ReadAll forever — an unkillable (SIGALRM-immune)
	// silent hang under agent/CI harnesses. It must now fail with a
	// clear timeout within the WYK_STDIN_TIMEOUT bound.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer w.Close() // releases the abandoned reader goroutine
	defer r.Close()
	swapStdin(t, r)
	t.Setenv("WYK_STDIN_TIMEOUT", "100ms")

	var code int
	start := time.Now()
	errOut := captureHandoffStderr(t, func() {
		code = runHandoff([]string{"-dry-run", "wyk-42"})
	})
	elapsed := time.Since(start)
	if code != 1 {
		t.Errorf("exit = %d, want 1 on stdin timeout", code)
	}
	if !strings.Contains(errOut, "timed out") || !strings.Contains(errOut, "WYK_STDIN_TIMEOUT") {
		t.Errorf("stderr should report the timeout and its override, got:\n%s", errOut)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timed out in %s; the 100ms deadline clearly did not bound the read", elapsed)
	}
}

func TestHandoff_PipeWithContentStillWorks(t *testing.T) {
	clearAmbientIdentity(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	swapStdin(t, r)
	go func() {
		_, _ = w.WriteString("1. rotate the key\n2. paste it at the known path\n")
		_ = w.Close()
	}()

	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "wyk-42"}); code != 0 {
			t.Errorf("exit = %d, want 0 for piped runbook", code)
		}
	})
	if !strings.Contains(out, "rotate the key") {
		t.Errorf("piped runbook missing from dry-run output:\n%s", out)
	}
}

func TestStdinTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultStdinTimeout},
		{"45s", 45 * time.Second},
		{"2m", 2 * time.Minute},
		{"7", 7 * time.Second},
		{"bogus", defaultStdinTimeout},
		{"-3", defaultStdinTimeout},
		{"0", defaultStdinTimeout},
	}
	for _, c := range cases {
		t.Setenv("WYK_STDIN_TIMEOUT", c.raw)
		if got := stdinTimeoutFromEnv(); got != c.want {
			t.Errorf("WYK_STDIN_TIMEOUT=%q → %s, want %s", c.raw, got, c.want)
		}
	}
}
