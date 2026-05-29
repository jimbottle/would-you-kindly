package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunCompletion_EmitsScriptPerShell(t *testing.T) {
	// Each shell's script has a different shape; pin a sentinel
	// per shell so a regression that swaps the emitter (e.g. bash
	// path serving zsh content) gets caught.
	cases := []struct {
		shell    string
		sentinel string
	}{
		{"bash", "complete -F _wyk wyk"},
		{"zsh", "#compdef wyk"},
		{"fish", "__fish_wyk_no_subcommand"},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			out := captureRunStdout(t, func() int { return runCompletion([]string{tc.shell}) })
			if !strings.Contains(out, tc.sentinel) {
				t.Errorf("%s script should contain %q; got:\n%s", tc.shell, tc.sentinel, out)
			}
			// Every script should mention at least one subcommand;
			// pin "doctor" because it's a deeper one (catches a
			// script that only emitted the first few entries).
			if !strings.Contains(out, "doctor") {
				t.Errorf("%s script should enumerate subcommands; got:\n%s", tc.shell, out)
			}
		})
	}
}

func TestRunCompletion_RejectsBadArgs(t *testing.T) {
	// Redirect stderr so the usage line doesn't leak into the
	// test runner output.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()

	cases := []struct {
		name string
		args []string
	}{
		{"missing shell", nil},
		{"unknown shell", []string{"powershell"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runCompletion(tc.args); got != 64 {
				t.Errorf("exit = %d, want 64", got)
			}
		})
	}
}

// captureRunStdout runs fn (a runX-style returning int) while
// redirecting stdout to a buffer and returns whatever fn wrote.
// Distinct from captureStdout in update_test.go (which takes
// func()); the run-functions returning an exit code shouldn't
// share the same helper.
func captureRunStdout(t *testing.T, fn func() int) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	doneCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		doneCh <- buf.String()
	}()
	_ = fn()
	_ = w.Close()
	os.Stdout = old
	return <-doneCh
}
