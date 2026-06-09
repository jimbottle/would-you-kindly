package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
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

// TestBuildSource_BadDir checks that an invalid -C directory fails
// fast with a clean, path-naming message rather than letting bd's raw
// JSON error blob surface later through a failed Fetch.
func TestBuildSource_BadDir(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		_, _, _, err := buildSource(missing, "me")
		if err == nil {
			t.Fatal("expected an error for a non-existent -C dir")
		}
		if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("error %q should name the path and say it does not exist", err)
		}
		if strings.Contains(err.Error(), "{") {
			t.Errorf("error %q leaks raw bd JSON", err)
		}
	})
	t.Run("not a dir", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "afile")
		if werr := os.WriteFile(f, []byte("x"), 0o600); werr != nil {
			t.Fatal(werr)
		}
		_, _, _, err := buildSource(f, "me")
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("expected a 'not a directory' error, got %v", err)
		}
	})
}

// TestPrintTopLevelUsage_NoDashGlyphInFlagHint guards against the
// Unicode-dash regression: the top-level --help is the most-read
// screen, and a stray en-dash / minus-sign inside a "(-json …)" hint
// breaks a copy-paste (`flag provided but not defined`). We only flag
// a dash glyph immediately followed by a letter — that's the
// flag-shaped case — so the intentional spaced prose em-dashes
// ("wyk — terminal UI …") and the `↔` arrow pass cleanly.
func TestPrintTopLevelUsage_NoDashGlyphInFlagHint(t *testing.T) {
	var buf bytes.Buffer
	old := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(&buf)
	defer flag.CommandLine.SetOutput(old)

	printTopLevelUsage()

	// U+2010..U+2015 (hyphen, non-breaking hyphen, figure/en/em dashes,
	// horizontal bar) and U+2212 MINUS SIGN — all easy to paste in by
	// accident in place of an ASCII '-'.
	dashGlyphs := map[rune]bool{
		'‐': true, '‑': true, '‒': true, '–': true,
		'—': true, '―': true, '−': true,
	}
	runes := []rune(buf.String())
	for i, r := range runes {
		if dashGlyphs[r] && i+1 < len(runes) {
			next := runes[i+1]
			if (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') {
				t.Errorf("usage text has a non-ASCII dash glyph %U before %q — looks like a flag (e.g. `-json`); use ASCII '-' so it stays copy-pasteable", r, next)
			}
		}
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
