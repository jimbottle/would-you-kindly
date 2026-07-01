package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
	"github.com/jimbottle/would-you-kindly/internal/tui"
	"github.com/jimbottle/would-you-kindly/internal/wyklog"
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

// resetDebugLogging restores the global logging state a test mutated: the
// cleanup hook, the stdlib log sink, and the slog default (via wyklog).
func resetDebugLogging(t *testing.T, origCleanup func()) {
	t.Helper()
	if debugLogCleanup != nil {
		debugLogCleanup()
	}
	debugLogCleanup = origCleanup
	log.SetOutput(os.Stderr)
	wyklog.Reset()
}

// TestSetupDebugLogging_EnablesGlobally pins would-you-kindly-w5bf.1 +
// w5bf.4: setupDebugLogging (now called before subcommand dispatch)
// activates the slog sink and writes the startup banner whenever a log
// path resolves, so EVERY command — not just the TUI — traces its bd
// calls through the structured logger.
func TestSetupDebugLogging_EnablesGlobally(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dbg.log")
	t.Setenv("WYK_LOG_FILE", logPath)
	t.Setenv("WYK_DEBUG", "")
	t.Setenv("WYK_LOG_LEVEL", "")
	origCleanup := debugLogCleanup
	debugLogCleanup = nil
	t.Cleanup(func() { resetDebugLogging(t, origCleanup) })

	setupDebugLogging()
	if !wyklog.Active() {
		t.Fatal("setupDebugLogging should activate the slog sink when a log path resolves")
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
	if !strings.Contains(string(b), "wyk starting") {
		t.Fatalf("startup banner missing from log: %q", b)
	}
}

// TestSetupDebugLogging_OffByDefault: with no WYK_DEBUG / WYK_LOG_FILE,
// no cleanup hook is installed (zero overhead, no file opened).
func TestSetupDebugLogging_OffByDefault(t *testing.T) {
	t.Setenv("WYK_LOG_FILE", "")
	t.Setenv("WYK_DEBUG", "")
	origCleanup := debugLogCleanup
	debugLogCleanup = nil
	wyklog.Reset() // start from a known-inactive state, not incidental prior cleanup
	t.Cleanup(func() { resetDebugLogging(t, origCleanup) })

	setupDebugLogging()
	if debugLogCleanup != nil {
		t.Fatal("no cleanup hook should be installed when debug logging is off")
	}
	if wyklog.Active() {
		t.Fatal("no slog sink should be installed when debug logging is off")
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

// TestRotateLogIfLarge pins would-you-kindly-w5bf.5: a log at/over the cap
// is rotated to ".1" (active path reopens empty); under the cap it's left
// alone. WYK_LOG_MAX_BYTES sets a tiny cap for the test.
func TestRotateLogIfLarge(t *testing.T) {
	t.Setenv("WYK_LOG_MAX_BYTES", "10")
	dir := t.TempDir()
	path := filepath.Join(dir, "wyk-debug.log")

	// Under cap: no rotation.
	if err := os.WriteFile(path, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateLogIfLarge(path)
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("under-cap file should not rotate; .1 exists: %v", err)
	}

	// Over cap: rotate to .1, active path gone (will reopen fresh).
	big := []byte("this is definitely more than ten bytes")
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	rotateLogIfLarge(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("over-cap active log should have been renamed away; still present: %v", err)
	}
	b, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("rotated backup .1 missing: %v", err)
	}
	if string(b) != string(big) {
		t.Fatalf(".1 should hold the rotated content; got %q", b)
	}
}

// TestLogMaxBytes_EnvOverride pins the WYK_LOG_MAX_BYTES knob and its
// fallback to the default on unset/invalid.
func TestLogMaxBytes_EnvOverride(t *testing.T) {
	t.Setenv("WYK_LOG_MAX_BYTES", "4096")
	if got := logMaxBytes(); got != 4096 {
		t.Fatalf("logMaxBytes() = %d, want 4096", got)
	}
	t.Setenv("WYK_LOG_MAX_BYTES", "garbage")
	if got := logMaxBytes(); got != defaultLogMaxBytes {
		t.Fatalf("invalid env should fall back to default %d, got %d", defaultLogMaxBytes, got)
	}
}

// TestRecordBDFailure_WritesSentinelRecord pins would-you-kindly-w5bf.6:
// recordBDFailure appends a one-line record with the argv, dir, and the
// classified sentinel to the always-on error log.
func TestRecordBDFailure_WritesSentinelRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	recordBDFailure([]string{"list", "--all"}, "/repo", fmt.Errorf("bd list: %w", beads.ErrTimedOut))
	b, err := os.ReadFile(errorLogPath())
	if err != nil {
		t.Fatalf("error log not written: %v", err)
	}
	got := string(b)
	for _, want := range []string{"bd list --all", `dir="/repo"`, "sentinel=timed-out"} {
		if !strings.Contains(got, want) {
			t.Errorf("error record missing %q; got: %s", want, got)
		}
	}
}

// TestBDSentinelName classifies each sentinel + the plain-error fallback.
func TestBDSentinelName(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", beads.ErrTimedOut), "timed-out"},
		{fmt.Errorf("x: %w", beads.ErrNoWorkspace), "no-workspace"},
		{fmt.Errorf("x: %w", beads.ErrBDNotFound), "bd-not-found"},
		{errors.New("plain"), "-"},
	}
	for _, tc := range cases {
		if got := bdSentinelName(tc.err); got != tc.want {
			t.Errorf("bdSentinelName(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestSetupDebugLogging_LevelKnob pins would-you-kindly-w5bf.4: WYK_LOG_LEVEL
// dials the slog level of the file log. At level=error the verbose bd trace
// (Debug) is filtered while failures (Error) still pass.
func TestSetupDebugLogging_LevelKnob(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dbg.log")
	t.Setenv("WYK_LOG_FILE", logPath)
	t.Setenv("WYK_DEBUG", "")
	t.Setenv("WYK_LOG_LEVEL", "error")
	origCleanup := debugLogCleanup
	debugLogCleanup = nil
	t.Cleanup(func() { resetDebugLogging(t, origCleanup) })

	setupDebugLogging()
	ctx := context.Background()
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		t.Error("WYK_LOG_LEVEL=error should filter the Debug-level bd trace")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		t.Error("WYK_LOG_LEVEL=error should still pass Error-level records")
	}
}

// TestRedactBDArgs pins that argv values are stripped for the always-on
// error log (roborev on w5bf.6): verb + flag names survive, values don't.
func TestRedactBDArgs(t *testing.T) {
	got := redactBDArgs([]string{"create", "--title=secret", "--notes", "sensitive", "-a", "alice", "positional"})
	if strings.Contains(got, "secret") || strings.Contains(got, "sensitive") ||
		strings.Contains(got, "alice") || strings.Contains(got, "positional") {
		t.Fatalf("redactBDArgs leaked a value: %q", got)
	}
	for _, want := range []string{"create", "--title=<redacted>", "--notes", "<redacted>", "-a"} {
		if !strings.Contains(got, want) {
			t.Errorf("redactBDArgs dropped %q; got %q", want, got)
		}
	}

	// A space-separated value that begins with '-' (a markdown bullet or
	// dash-led note) must NOT be mistaken for a flag and preserved — it
	// gets redacted (roborev on w5bf.6).
	got = redactBDArgs([]string{"create", "--description", "- fixes the crash on startup"})
	if strings.Contains(got, "fixes the crash") {
		t.Fatalf("redactBDArgs leaked a dash-prefixed value: %q", got)
	}
	if !strings.Contains(got, "--description <redacted>") {
		t.Fatalf("dash-prefixed value should be redacted; got %q", got)
	}
}

// TestLooksLikeFlag pins the flag-shape predicate used by redaction.
func TestLooksLikeFlag(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"-a", true}, {"--title", true}, {"--title=x", true},
		{"- fixes", false}, {"-", false}, {"--", false},
		{"-5", false}, {"---x", false}, {"plain", false}, {"", false},
	} {
		if got := looksLikeFlag(tc.in); got != tc.want {
			t.Errorf("looksLikeFlag(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
