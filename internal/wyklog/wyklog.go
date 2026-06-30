// Package wyklog centralizes wyk's structured, leveled logging on top of
// log/slog, replacing the old single Debug boolean over the stdlib logger
// (would-you-kindly-w5bf.4). One slog.Logger — installed as slog.Default
// — is the sink for the verbose bd-call trace plus the bd-failure and
// crash records, each emitted at a level (Debug / Warn / Error) so
// WYK_LOG_LEVEL can dial verbosity.
//
// Off by default: until Setup runs, Active() is false. Callers that ALSO
// keep an always-on file record (the crash log, the bd-error log) consult
// Active() before emitting to the slog stream, so a failure isn't echoed
// to stderr (slog's zero-value default) when logging hasn't been turned
// on — wyk stays silent unless WYK_DEBUG / WYK_LOG_FILE is set.
package wyklog

import (
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
)

var active atomic.Bool

// Setup installs a slog.TextHandler writing to w at lvl as the process
// default logger and marks logging active. Call once at startup, when a
// log destination has been resolved.
func Setup(w io.Writer, lvl slog.Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})))
	active.Store(true)
}

// Active reports whether Setup has installed a real sink. The always-on
// file records (crash.log, bd-errors.log) check this before ALSO writing
// to the slog stream, so they don't print to stderr when logging is off.
func Active() bool { return active.Load() }

// Reset restores the inactive, silent state (Active()==false and a
// discarding default logger). Intended for tests that need isolation
// between logging setups; production installs a real sink via Setup.
func Reset() {
	active.Store(false)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
}

// ParseLevel maps a WYK_LOG_LEVEL string (case-insensitive) to a
// slog.Level, returning def for empty or unrecognized input.
func ParseLevel(s string, def slog.Level) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return def
	}
}
