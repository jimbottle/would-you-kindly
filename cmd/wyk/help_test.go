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
