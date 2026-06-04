package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureHandoffStdout runs fn with os.Stdout redirected, drains
// the pipe in a goroutine to avoid buffer-fill blocking, and
// returns whatever fn wrote.
func captureHandoffStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestHandoff_CreateAndPositionalAreMutuallyExclusive(t *testing.T) {
	// The two modes of `wyk handoff` are -create (file a new issue)
	// and positional <id> (act on an existing issue). Both at once
	// would be ambiguous — runHandoff must refuse with the usage
	// exit code 64 before reading stdin or touching bd.
	code := runHandoff([]string{"-create", "some title", "would-you-kindly-42"})
	if code != 64 {
		t.Errorf("expected exit 64 when both -create and positional id given; got %d", code)
	}
}

func TestHandoff_MissingArgsReturnsUsageCode(t *testing.T) {
	// No -create, no positional id → usage error (64), no stdin read,
	// no bd contact. Pure flag-parsing validation.
	code := runHandoff([]string{})
	if code != 64 {
		t.Errorf("expected exit 64 when no <id> and no -create; got %d", code)
	}
}

// writeRunbook drops the given runbook body into a tempfile and
// returns its path. -file <path> in the dry-run tests bypasses
// the stdin TTY guard, so we read from a file instead of piping
// — keeps the tests deterministic.
func writeRunbook(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "runbook.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write runbook: %v", err)
	}
	return path
}

func TestHandoff_DryRunBareIDPrintsPlanWithoutWriting(t *testing.T) {
	path := writeRunbook(t, "1. step one\n2. step two")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-file", path, "wyk-42"}); code != 0 {
			t.Errorf("dry-run exit %d, want 0", code)
		}
	})
	for _, want := range []string{
		"DRY-RUN: no bd writes performed",
		"would hand off wyk-42 to human",
		"runbook (",
		"---",
		"1. step one",
		"2. step two",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, out)
		}
	}
}

func TestHandoff_DryRunCreateWithIdentityAddsRoutingLabel(t *testing.T) {
	t.Setenv(sessionEnvVar, "")  // deterministic labels (no session stamp)
	t.Setenv(identityEnvVar, "") // no ambient identity; -identity drives it
	path := writeRunbook(t, "do the thing")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{
			"-dry-run", "-create", "Rotate creds", "-identity", "alice", "-file", path,
		}); code != 0 {
			t.Errorf("dry-run -create -identity exit %d, want 0", code)
		}
	})
	// The routing label is layered on the collective src:agent umbrella.
	want := `labels=[src:agent src:agent:alice]`
	if !strings.Contains(out, want) {
		t.Errorf("dry-run -create -identity output missing %q; got:\n%s", want, out)
	}
}

func TestHandoff_DryRunBareIDWithIdentityShowsRouting(t *testing.T) {
	t.Setenv(identityEnvVar, "")
	path := writeRunbook(t, "1. step")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-identity", "claude", "-file", path, "wyk-9"}); code != 0 {
			t.Errorf("dry-run bare-id -identity exit %d, want 0", code)
		}
	})
	for _, want := range []string{
		"would hand off wyk-9 to human",
		`would route to identity "claude" (label=src:agent:claude added)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, out)
		}
	}
}

func TestHandoff_MalformedIdentityIsUsageError(t *testing.T) {
	t.Setenv(identityEnvVar, "")
	path := writeRunbook(t, "1. step")
	if code := runHandoff([]string{"-identity", "Bad Name", "-file", path, "wyk-1"}); code != 64 {
		t.Errorf("handoff -identity 'Bad Name' = %d, want 64 (usage error)", code)
	}
}

func TestHandoff_DryRunCreatePrintsCreatePlanWithoutWriting(t *testing.T) {
	t.Setenv(sessionEnvVar, "") // deterministic labels (no session stamp)
	path := writeRunbook(t, "do the thing")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{
			"-dry-run", "-create", "Rotate the staging DB password",
			"-priority", "1", "-type", "task", "-file", path,
		}); code != 0 {
			t.Errorf("dry-run -create exit %d, want 0", code)
		}
	})
	for _, want := range []string{
		"DRY-RUN: no bd writes performed",
		`would create: title="Rotate the staging DB password" priority=1 type=task labels=[src:agent]`,
		"would hand off the new issue to human",
		"do the thing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run -create output missing %q; got:\n%s", want, out)
		}
	}
}

func TestHandoff_CreateStampsSessionLabel(t *testing.T) {
	// `wyk handoff -create` records the Claude session like `wyk create`,
	// so handoff-filed issues populate the TUI's Session column too.
	t.Setenv(sessionEnvVar, "sess-9999")
	path := writeRunbook(t, "do the thing")
	out := captureHandoffStdout(t, func() {
		runHandoff([]string{
			"-dry-run", "-create", "A handoff", "-file", path,
		})
	})
	if !strings.Contains(out, "session:sess-9999") {
		t.Errorf("dry-run -create should plan the session label; got:\n%s", out)
	}
}
