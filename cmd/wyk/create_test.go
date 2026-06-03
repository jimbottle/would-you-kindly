package main

import (
	"testing"
)

// withStubCreate swaps the bd-create seam for the duration of a test and
// captures what runCreate forwarded.
type capturedCreate struct {
	dir          string
	passthrough  []string
	sessionLabel string
	called       bool
}

func withStubCreate(t *testing.T, id string, err error) *capturedCreate {
	t.Helper()
	cap := &capturedCreate{}
	prev := runBDCreateWithSession
	runBDCreateWithSession = func(dir string, passthrough []string, sessionLabel string) (string, error) {
		cap.called = true
		cap.dir = dir
		cap.passthrough = passthrough
		cap.sessionLabel = sessionLabel
		return id, err
	}
	t.Cleanup(func() { runBDCreateWithSession = prev })
	return cap
}

func TestRunCreate_StampsSessionFromEnv(t *testing.T) {
	t.Setenv(sessionEnvVar, "abcd1234-5678-9012")
	cap := withStubCreate(t, "demo-xyz", nil)

	code := runCreate([]string{"--title", "A task", "--type=task"})
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !cap.called {
		t.Fatal("bd create seam was not invoked")
	}
	if cap.sessionLabel != sessionLabelPrefix+"abcd1234-5678-9012" {
		t.Errorf("session label = %q, want %q", cap.sessionLabel, sessionLabelPrefix+"abcd1234-5678-9012")
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

func TestRunCreate_NoSessionEnvStillCreates(t *testing.T) {
	t.Setenv(sessionEnvVar, "")
	cap := withStubCreate(t, "demo-xyz", nil)

	code := runCreate([]string{"Quick task"})
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if cap.sessionLabel != "" {
		t.Errorf("session label = %q, want empty when env unset", cap.sessionLabel)
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
}
