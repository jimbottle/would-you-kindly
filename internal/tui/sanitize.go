package tui

import "github.com/jimbottle/would-you-kindly/internal/sanitize"

// The sanitizers live in internal/sanitize so the CLI text printers
// (probe/inbox/activity/depgraph) share the exact same logic as the TUI
// render paths (would-you-kindly-waub, -5zlr). These thin aliases keep
// the existing internal/tui call sites unchanged.

// sanitizeInline strips all control characters (titles, dep-row titles,
// labels, repo/branch cells). See sanitize.Inline.
func sanitizeInline(s string) string { return sanitize.Inline(s) }

// sanitizeBlock strips control characters except newline/tab (descriptions,
// notes, raw bd output). See sanitize.Block.
func sanitizeBlock(s string) string { return sanitize.Block(s) }
