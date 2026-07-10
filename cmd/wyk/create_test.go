package main

import (
	"errors"
	"strings"
	"testing"
)

// withStubCreate swaps the bd-create seam for the duration of a test and
// captures what runCreate forwarded.
type capturedCreate struct {
	dir         string
	passthrough []string
	labels      []string
	called      bool
}

func (c *capturedCreate) hasLabel(l string) bool {
	for _, got := range c.labels {
		if got == l {
			return true
		}
	}
	return false
}

func withStubCreate(t *testing.T, id string, err error) *capturedCreate {
	t.Helper()
	cap := &capturedCreate{}
	prev := runBDCreateWithLabels
	runBDCreateWithLabels = func(dir string, passthrough []string, labels []string) (string, error) {
		cap.called = true
		cap.dir = dir
		cap.passthrough = passthrough
		cap.labels = labels
		return id, err
	}
	t.Cleanup(func() { runBDCreateWithLabels = prev })
	return cap
}

func TestRunCreate_StampsSrcAgentAndSessionInSession(t *testing.T) {
	t.Setenv(sessionEnvVar, "abcd1234-5678-9012")
	cap := withStubCreate(t, "demo-xyz", nil)

	code := runCreate([]string{"--title", "A task", "--type=task"})
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !cap.called {
		t.Fatal("bd create seam was not invoked")
	}
	// A Claude session filed it: src:agent (contract) + the session stamp.
	if !cap.hasLabel("src:agent") {
		t.Errorf("labels %v missing src:agent — wyk-filed agent issues must match `wyk inbox`", cap.labels)
	}
	if cap.hasLabel("src:human") {
		t.Errorf("labels %v should not carry src:human in a session", cap.labels)
	}
	if !cap.hasLabel(sessionLabelPrefix + "abcd1234-5678-9012") {
		t.Errorf("labels %v missing the session stamp", cap.labels)
	}
	// src: label comes first so provenance is stamped before the session.
	if len(cap.labels) == 0 || cap.labels[0] != "src:agent" {
		t.Errorf("labels %v: src:agent should be applied first", cap.labels)
	}
	// User args forwarded verbatim.
	want := []string{"--title", "A task", "--type=task"}
	if len(cap.passthrough) != len(want) {
		t.Fatalf("passthrough = %v, want %v", cap.passthrough, want)
	}
	for i := range want {
		if cap.passthrough[i] != want[i] {
			t.Errorf("passthrough[%d] = %q, want %q", i, cap.passthrough[i], want[i])
		}
	}
}

func TestRunCreate_StampsSrcHumanWithoutSession(t *testing.T) {
	t.Setenv(sessionEnvVar, "")
	cap := withStubCreate(t, "demo-xyz", nil)

	if code := runCreate([]string{"--title", "x"}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	// No session → a person is filing → src:human, and no session stamp.
	if !cap.hasLabel("src:human") {
		t.Errorf("labels %v missing src:human when no session is set", cap.labels)
	}
	if cap.hasLabel("src:agent") {
		t.Errorf("labels %v should not carry src:agent without a session", cap.labels)
	}
	for _, l := range cap.labels {
		if strings.HasPrefix(l, sessionLabelPrefix) {
			t.Errorf("labels %v should carry no session stamp when the env is unset", cap.labels)
		}
	}
}

func TestRunCreate_PartialSuccessReportsIDAndExits1(t *testing.T) {
	// Issue created (non-empty id) but the session label failed to stamp:
	// runCreate must exit 1 AND still report the created ID on stdout, in
	// the same `wyk create: created <id>` format as the success path.
	t.Setenv(sessionEnvVar, "sess-1234")
	withStubCreate(t, "demo-xyz", errors.New("label add failed"))

	var code int
	out := captureStdout(t, func() {
		code = runCreate([]string{"--title", "x"})
	})
	if code != 1 {
		t.Errorf("exit %d, want 1 on partial success", code)
	}
	if !strings.Contains(out, "demo-xyz") {
		t.Errorf("partial-success stdout should still report the created ID; got %q", out)
	}
	if !strings.Contains(out, "wyk create: created") {
		t.Errorf("partial-success stdout should use the standard created line; got %q", out)
	}
}

func TestRunCreate_NoArgsIsUsageError(t *testing.T) {
	withStubCreate(t, "", nil)
	if code := runCreate(nil); code != 64 {
		t.Errorf("exit %d, want 64 for no args", code)
	}
}

func TestRunCreate_HelpExitsZero(t *testing.T) {
	withStubCreate(t, "", nil)
	if code := runCreate([]string{"--help"}); code != 0 {
		t.Errorf("exit %d, want 0 for --help", code)
	}
}

func TestHasFlag(t *testing.T) {
	args := []string{"--title", "x", "--dolt-auto-commit=on", "--silent"}
	if !hasFlag(args, "--silent") {
		t.Error("want hasFlag --silent true")
	}
	if !hasFlag(args, "--dolt-auto-commit") {
		t.Error("want hasFlag --dolt-auto-commit true (matches --flag=value form)")
	}
	if hasFlag(args, "--priority") {
		t.Error("want hasFlag --priority false")
	}
	// Single-dash forms are detected too (Go's flag package accepts them),
	// so we don't append a duplicate that could override an explicit value.
	if !hasFlag([]string{"-silent"}, "--silent") {
		t.Error("want hasFlag to detect single-dash -silent")
	}
	if !hasFlag([]string{"-dolt-auto-commit=off"}, "--dolt-auto-commit") {
		t.Error("want hasFlag to detect single-dash -dolt-auto-commit=off")
	}
}
