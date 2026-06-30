package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
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

func TestVersionString_PrefersInjectedVersion(t *testing.T) {
	// goreleaser stamps injectedVersion via -ldflags -X because its
	// `go build` from a clean tag checkout otherwise reports "(devel)".
	// When set, the injected tag must win over the build-info version so
	// prebuilt-binary / Homebrew installs report their real tag.
	prev := injectedVersion
	t.Cleanup(func() { injectedVersion = prev })

	injectedVersion = "v9.9.9"
	got := versionString()
	if !strings.Contains(got, "v9.9.9") {
		t.Errorf("injected version not reflected: got %q, want it to contain %q", got, "v9.9.9")
	}
	if strings.Contains(got, "(devel)") {
		t.Errorf("injected version should override the (devel) marker: got %q", got)
	}
}

// resetDebugLogging restores the global debug-logging state a test
// mutated: beads.Debug, the cleanup hook, and the stdlib log sink
// (tea.LogToFile redirects it process-wide).
func resetDebugLogging(t *testing.T, origDebug bool, origCleanup func()) {
	t.Helper()
	if debugLogCleanup != nil {
		debugLogCleanup()
	}
	beads.Debug = origDebug
	debugLogCleanup = origCleanup
	log.SetOutput(os.Stderr)
}

// TestSetupDebugLogging_EnablesGlobally pins would-you-kindly-w5bf.1:
// setupDebugLogging (now called before subcommand dispatch) turns on
// beads.Debug and writes the startup banner whenever a log path resolves,
// so EVERY command — not just the TUI — traces its bd calls.
func TestSetupDebugLogging_EnablesGlobally(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dbg.log")
	t.Setenv("WYK_LOG_FILE", logPath)
	t.Setenv("WYK_DEBUG", "")
	origDebug, origCleanup := beads.Debug, debugLogCleanup
	beads.Debug, debugLogCleanup = false, nil
	t.Cleanup(func() { resetDebugLogging(t, origDebug, origCleanup) })

	setupDebugLogging()
	if !beads.Debug {
		t.Fatal("setupDebugLogging should set beads.Debug when a log path resolves")
	}
	if debugLogCleanup == nil {
		t.Fatal("setupDebugLogging should install a cleanup hook")
	}
	debugLogCleanup() // flush + close so the banner is on disk
	debugLogCleanup = nil
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(b), "debug logging enabled") {
		t.Fatalf("startup banner missing from log: %q", b)
	}
}

// TestSetupDebugLogging_OffByDefault: with no WYK_DEBUG / WYK_LOG_FILE,
// nothing is enabled and no cleanup hook is installed (zero overhead).
func TestSetupDebugLogging_OffByDefault(t *testing.T) {
	t.Setenv("WYK_LOG_FILE", "")
	t.Setenv("WYK_DEBUG", "")
	origDebug, origCleanup := beads.Debug, debugLogCleanup
	beads.Debug, debugLogCleanup = false, nil
	t.Cleanup(func() { resetDebugLogging(t, origDebug, origCleanup) })

	setupDebugLogging()
	if beads.Debug {
		t.Fatal("beads.Debug should stay false when debug logging is off")
	}
	if debugLogCleanup != nil {
		t.Fatal("no cleanup hook should be installed when debug logging is off")
	}
}

// TestCaptureCrash_WritesRecordAndExitsNonZero exercises the deferred
// recover end-to-end via the standard os.Exit subprocess pattern: the
// child installs captureCrash, panics, and must (a) exit non-zero and
// (b) append a panic record naming the panic value + a stack to the
// crash log under XDG_STATE_HOME. (would-you-kindly-w5bf.2)
func TestCaptureCrash_WritesRecordAndExitsNonZero(t *testing.T) {
	if os.Getenv("WYK_TEST_CRASH") == "1" {
		defer captureCrash()
		panic("boom-from-test")
	}
	state := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCaptureCrash_WritesRecordAndExitsNonZero")
	cmd.Env = append(os.Environ(), "WYK_TEST_CRASH=1", "XDG_STATE_HOME="+state)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit from the panicking child; out=%s", out)
	}
	b, readErr := os.ReadFile(filepath.Join(state, "wyk", "crash.log"))
	if readErr != nil {
		t.Fatalf("crash log not written: %v (child output: %s)", readErr, out)
	}
	got := string(b)
	for _, want := range []string{"boom-from-test", "panic:", "version:", "captureCrash"} {
		if !strings.Contains(got, want) {
			t.Errorf("crash record missing %q; got:\n%s", want, got)
		}
	}
}

// TestWriteCrashRecord_TUINoStack pins the TUI branch: when no stack is
// passed (Bubble Tea owns it), the record still lands with version/args
// and an explicit note that the stack is on the terminal.
func TestWriteCrashRecord_TUINoStack(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := writeCrashRecord("tui", errors.New("kaboom"), nil)
	if path == "" {
		t.Fatal("writeCrashRecord returned empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read crash log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "kaboom") || !strings.Contains(got, "printed to the terminal") {
		t.Fatalf("unexpected TUI crash record:\n%s", got)
	}
}

// TestCrashLogPath_HonorsXDGState pins the path resolution.
func TestCrashLogPath_HonorsXDGState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state-test")
	got := crashLogPath()
	want := filepath.Join("/tmp/xdg-state-test", "wyk", "crash.log")
	if got != want {
		t.Fatalf("crashLogPath() = %q, want %q", got, want)
	}
}
