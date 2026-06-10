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

func TestRunCompletion_HelpExitsZero(t *testing.T) {
	// -h / --help / help are deliberate requests, not usage errors:
	// they print the synopsis to stdout and exit 0 (rather than the
	// old "unknown shell" exit 64).
	for _, arg := range []string{"-h", "--help", "help"} {
		t.Run(arg, func(t *testing.T) {
			var code int
			out := captureRunStdout(t, func() int { code = runCompletion([]string{arg}); return code })
			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if !strings.Contains(out, "usage: wyk completion") {
				t.Errorf("expected usage on stdout; got %q", out)
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

func TestStrayArgMsg_UnknownSubcommandSuggests(t *testing.T) {
	// The typo case (would-you-kindly-tu9t): `wyk inbx` used to silently
	// launch the TUI. The message must name the bad word and, when a
	// subcommand is within edit distance 2, suggest it.
	cases := []struct {
		arg     string
		want    string // substring that must appear
		suggest string // expected did-you-mean target; "" = no suggestion
	}{
		{"inbx", `unknown subcommand "inbx"`, "inbox"},
		{"handof", `unknown subcommand "handof"`, "handoff"},
		{"stat", `unknown subcommand "stat"`, "stats"},
		{"frobnicate", `unknown subcommand "frobnicate"`, ""},
		{"INBOX", `unknown subcommand "INBOX"`, "inbox"}, // case-insensitive match
	}
	for _, c := range cases {
		got := strayArgMsg(c.arg)
		if !strings.Contains(got, c.want) {
			t.Errorf("strayArgMsg(%q) = %q, want it to contain %q", c.arg, got, c.want)
		}
		if c.suggest != "" && !strings.Contains(got, `did you mean "`+c.suggest+`"`) {
			t.Errorf("strayArgMsg(%q) = %q, want a did-you-mean for %q", c.arg, got, c.suggest)
		}
		if c.suggest == "" && strings.Contains(got, "did you mean") {
			t.Errorf("strayArgMsg(%q) = %q, want NO suggestion for a distant word", c.arg, got)
		}
	}
}

func TestStrayArgMsg_FlagsBeforeSubcommand(t *testing.T) {
	// The footgun case: `wyk -C dir handoff` parses "handoff" as a
	// positional because dispatch only reads os.Args[1]. The message
	// must say the subcommand goes first, not call it unknown — and
	// `hook` (excluded from completion on purpose) still counts as known.
	for _, sub := range []string{"handoff", "inbox", "hook"} {
		got := strayArgMsg(sub)
		if !strings.Contains(got, "must be the first argument") {
			t.Errorf("strayArgMsg(%q) = %q, want the flags-before-subcommand message", sub, got)
		}
		if strings.Contains(got, "unknown subcommand") {
			t.Errorf("strayArgMsg(%q) = %q, must not call a known subcommand unknown", sub, got)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"inbx", "inbox", 1},
		{"stat", "stats", 1},
		{"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestWykSubcommandsMatchDispatch(t *testing.T) {
	// wykSubcommands (completion + did-you-mean) must be exactly the
	// dispatch map minus `hook` (internal, hook-invoked only). Drift
	// used to cost only shell-completion coverage; since strayArgMsg,
	// it would also misclassify a known subcommand as unknown in the
	// flags-before-subcommand error (roborev #2029).
	listed := make(map[string]bool, len(wykSubcommands))
	for _, s := range wykSubcommands {
		if _, ok := subcommandHandlers[s]; !ok {
			t.Errorf("wykSubcommands lists %q but main dispatches no such subcommand", s)
		}
		if listed[s] {
			t.Errorf("wykSubcommands lists %q twice", s)
		}
		listed[s] = true
	}
	for name := range subcommandHandlers {
		if name == "hook" {
			continue // internal: deliberately absent from completion
		}
		if !listed[name] {
			t.Errorf("subcommand %q is dispatched but missing from wykSubcommands (completion + did-you-mean)", name)
		}
	}
}

func TestStrayArgGuard(t *testing.T) {
	// The post-flag.Parse contract main relies on: zero positionals is
	// the only good state; anything else must produce a message so main
	// exits 64 before the TUI/probe path runs.
	if msg, bad := strayArgGuard(nil); bad || msg != "" {
		t.Errorf("strayArgGuard(nil) = (%q, %v), want clean pass", msg, bad)
	}
	if msg, bad := strayArgGuard([]string{"inbx", "extra"}); !bad || msg == "" {
		t.Errorf("strayArgGuard with positionals = (%q, %v), want a message and bad=true", msg, bad)
	}
}
