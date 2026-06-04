package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/tui"
)

// probeMultiStub implements both tui.Source and tui.MultiSource so the
// probe partial-failure path is exercised without a real bd.
type probeMultiStub struct {
	issues  []beads.Issue
	subErrs []tui.FetchError
	topErr  error
}

func (p probeMultiStub) Fetch(ctx context.Context, preset filter.Preset) ([]beads.Issue, error) {
	return p.issues, p.topErr
}

func (p probeMultiStub) FetchWithSubErrors(ctx context.Context, preset filter.Preset) ([]beads.Issue, []tui.FetchError, error) {
	return p.issues, p.subErrs, p.topErr
}

// captureOutErr runs fn with os.Stdout and os.Stderr redirected to
// pipes and returns what each received.
func captureOutErr(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	wOut.Close()
	wErr.Close()
	bOut, _ := io.ReadAll(rOut)
	bErr, _ := io.ReadAll(rErr)
	return string(bOut), string(bErr)
}

func TestRunProbe_PartialFailureWarnsOnStderr(t *testing.T) {
	src := probeMultiStub{
		issues:  []beads.Issue{{ID: "repoA-1", Priority: 1, Title: "do the thing"}},
		subErrs: []tui.FetchError{{Repo: "repoB", Err: errors.New("bd timed out")}},
	}
	var code int
	stdout, stderr := captureOutErr(t, func() { code = runProbe(src) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (partial failure is still success)", code)
	}
	if !strings.Contains(stdout, "repoA-1") || !strings.Contains(stdout, "1 issue(s) flagged") {
		t.Errorf("stdout missing the issue line: %q", stdout)
	}
	if !strings.Contains(stderr, "repoB") || !strings.Contains(stderr, "1 repo(s) failed") {
		t.Errorf("stderr should name the failed repo: %q", stderr)
	}
}

func TestRunProbe_NoSubErrorsNoStderrNoise(t *testing.T) {
	src := probeMultiStub{
		issues: []beads.Issue{{ID: "repoA-1", Priority: 2, Title: "ok"}},
	}
	var code int
	_, stderr := captureOutErr(t, func() { code = runProbe(src) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.Contains(stderr, "repo(s) failed") {
		t.Errorf("stderr should be quiet when no repo failed: %q", stderr)
	}
}

func TestNoColorRequested(t *testing.T) {
	cases := []struct {
		name    string
		noColor string
		wykNo   string
		want    bool
	}{
		{"both unset", "", "", false},
		{"NO_COLOR=1", "1", "", true},
		{"NO_COLOR=true", "true", "", true},
		// no-color.org spec: any non-empty value counts.
		{"NO_COLOR= (single space)", " ", "", true},
		{"WYK_NO_COLOR=1", "", "1", true},
		{"both set", "1", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("WYK_NO_COLOR", tc.wykNo)
			if got := noColorRequested(); got != tc.want {
				t.Errorf("noColorRequested() = %v, want %v (NO_COLOR=%q WYK_NO_COLOR=%q)", got, tc.want, tc.noColor, tc.wykNo)
			}
		})
	}
}

func TestVersionString_NonEmpty(t *testing.T) {
	// Sanity check: the function always produces SOMETHING usable,
	// regardless of how the binary was built (go install, go build,
	// go run, vendored). Bug reports are useless without a version
	// string and the function should never return "".
	got := versionString()
	if got == "" {
		t.Fatal("versionString returned empty string — bug reports lose the version line")
	}
	if !strings.HasPrefix(got, "wyk ") {
		t.Errorf("expected version string to start with 'wyk '; got %q", got)
	}
}

func TestVersionString_NoDoubleDirty(t *testing.T) {
	// Regression: an earlier draft printed "+dirty" (from Go's
	// pseudoversion suffix) AND "-dirty" (from our vcs.modified
	// inspection) when both signals were present. Whatever the
	// build state, the word "dirty" should appear at most once.
	got := versionString()
	if strings.Count(got, "dirty") > 1 {
		t.Errorf("'dirty' appears more than once in version string: %q", got)
	}
}
