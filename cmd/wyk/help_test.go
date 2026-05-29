package main

import (
	"io"
	"os"
	"testing"
)

func TestRunHelp_MarkdownEmitsReference(t *testing.T) {
	// Redirect stdout so we capture the markdown body and stderr
	// so flag-parsing output stays out of the test runner log.
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, _ := os.Pipe()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = w
	os.Stderr = devnull
	defer func() {
		os.Stdout = oldOut
		os.Stderr = oldErr
		_ = devnull.Close()
	}()

	// io.ReadAll drains the pipe regardless of size or write
	// boundaries — single-Read with a 64KB buffer would
	// intermittently truncate as the markdown reference grows.
	doneCh := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		doneCh <- string(b)
	}()

	if code := runHelp([]string{"--markdown"}); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	_ = w.Close()
	out := <-doneCh

	for _, want := range []string{
		"# wyk keymap",
		"## Navigation",
		"## Writes",
		"| Key | Action |",
	} {
		if !contains(out, want) {
			t.Errorf("markdown output should contain %q; got:\n%s", want, out)
		}
	}
}

func TestRunHelp_NoFlagPointsAtOverlay(t *testing.T) {
	oldOut := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = oldOut }()

	doneCh := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		doneCh <- string(b)
	}()

	if code := runHelp(nil); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	_ = w.Close()
	out := <-doneCh
	if !contains(out, "?") {
		t.Errorf("default output should mention the `?` overlay; got %q", out)
	}
}

func TestRunHelp_CLIMarkdownEmitsEverySubcommand(t *testing.T) {
	// Capture stdout the same way TestRunHelp_MarkdownEmitsReference
	// does (drained-in-goroutine to avoid pipe-buffer truncation).
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, _ := os.Pipe()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = w
	os.Stderr = devnull
	defer func() {
		os.Stdout = oldOut
		os.Stderr = oldErr
		_ = devnull.Close()
	}()
	doneCh := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		doneCh <- string(b)
	}()

	if code := runHelp([]string{"--cli-markdown"}); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	_ = w.Close()
	out := <-doneCh

	if !contains(out, "# wyk CLI reference") {
		t.Errorf("missing top-level heading; got:\n%s", out)
	}
	// Every entry in the canonical table should appear as an H2.
	// Note: this only catches "table entry missing from output" —
	// the inverse (dispatch entry missing from the table) is
	// covered by TestCLISubcommandDocs_CoversEveryDispatchedSubcommand
	// below, and flag-level drift is caught by the CI docs-check.
	for _, d := range cliSubcommandDocs {
		want := "## `wyk " + d.Name + "`"
		if !contains(out, want) {
			t.Errorf("output missing section %q", want)
		}
	}
}

// TestCLISubcommandDocs_CoversEveryDispatchedSubcommand guards
// the inverse direction: a subcommand added to the dispatch /
// completion list must have a matching cliSubcommandDocs entry.
// Anchored on wykSubcommands (completion.go) because that's the
// existing canonical "user-facing subcommands" list — drift
// between the two is exactly what this test catches.
func TestCLISubcommandDocs_CoversEveryDispatchedSubcommand(t *testing.T) {
	have := make(map[string]bool, len(cliSubcommandDocs))
	for _, d := range cliSubcommandDocs {
		have[d.Name] = true
	}
	for _, name := range wykSubcommands {
		if !have[name] {
			t.Errorf("wykSubcommands has %q but cliSubcommandDocs is missing an entry — add one to cmd/wyk/clidocs.go", name)
		}
	}
	// Inverse: every doc entry should also appear in
	// wykSubcommands (so a typo in the doc table can't go
	// unnoticed). "hook" is intentionally absent from
	// wykSubcommands and should also be absent here — we don't
	// document the internal hook subcommand.
	want := make(map[string]bool, len(wykSubcommands))
	for _, n := range wykSubcommands {
		want[n] = true
	}
	for _, d := range cliSubcommandDocs {
		if !want[d.Name] {
			t.Errorf("cliSubcommandDocs has %q but wykSubcommands does not — typo, or a subcommand that should not be user-facing?", d.Name)
		}
	}
}

func TestRunHelp_MarkdownAndCLIMarkdownMutuallyExclusive(t *testing.T) {
	oldErr := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = oldErr
		_ = devnull.Close()
	}()
	if code := runHelp([]string{"--markdown", "--cli-markdown"}); code != 64 {
		t.Errorf("combined flags exit = %d, want 64", code)
	}
}

func TestRunHelp_RejectsPositionalArg(t *testing.T) {
	oldErr := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = oldErr
		_ = devnull.Close()
	}()
	if code := runHelp([]string{"unexpected"}); code != 64 {
		t.Errorf("exit = %d, want 64", code)
	}
}

// contains is a tiny strings.Contains alias so the helper-heavy
// tests above stay readable without an extra strings import.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
