package wyklog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"  Error ", slog.LevelError},
		{"", slog.LevelInfo},      // empty → default
		{"bogus", slog.LevelInfo}, // unknown → default
	}
	for _, tc := range cases {
		if got := ParseLevel(tc.in, slog.LevelInfo); got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSetupActivatesAndFiltersByLevel(t *testing.T) {
	t.Cleanup(Reset)
	var buf bytes.Buffer
	Setup(&buf, slog.LevelWarn)
	if !Active() {
		t.Fatal("Active() should be true after Setup")
	}
	ctx := context.Background()
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		t.Error("Debug should be filtered out at level Warn")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		t.Error("Error should pass at level Warn")
	}
	slog.Debug("hidden")
	slog.Error("shown", "k", "v")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("debug line should have been filtered; got: %s", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("error line missing; got: %s", out)
	}
}

func TestReset(t *testing.T) {
	var buf bytes.Buffer
	Setup(&buf, slog.LevelDebug)
	Reset()
	if Active() {
		t.Fatal("Active() should be false after Reset")
	}
	slog.Error("after-reset")
	if buf.Len() != 0 {
		t.Errorf("Reset should detach the buffer; got: %s", buf.String())
	}
}
