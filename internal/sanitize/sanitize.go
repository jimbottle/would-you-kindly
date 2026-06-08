// Package sanitize strips terminal control sequences from untrusted bd
// content before it reaches a terminal — issue titles, descriptions,
// notes, labels, and raw bd output. In a shared or multi-repo workspace a
// teammate (or a malicious registered repo) can author these, and bd does
// not constrain their charset. Rendered raw, an embedded escape — OSC 52
// (clipboard write), cursor/title manipulation, a CSI color run — would be
// INTERPRETED by the terminal rather than shown.
//
// Stripping the control BYTES is enough to defang a sequence: a CSI like
// "\x1b[31m" without its leading ESC is just the harmless literal "[31m";
// OSC 52 without its ESC/BEL framing is inert. The control runes are
// dropped (not escaped) so the visible remnant stays compact.
//
// This is the single home for the logic; the TUI render paths and the CLI
// text printers (probe/inbox/activity/depgraph) both call it.
package sanitize

import "strings"

// isUnsafeControl reports whether r is a control character to strip: C0
// (0x00–0x1F, including ESC), DEL (0x7F), and C1 (0x80–0x9F). When
// allowWhitespace is true, the layout-safe controls newline and tab are
// kept (for multi-line bodies).
func isUnsafeControl(r rune, allowWhitespace bool) bool {
	if allowWhitespace && (r == '\n' || r == '\t') {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// Inline strips ALL control characters — including newline and tab — for
// single-line fields (titles, dependency-row titles, labels, repo/branch
// cells, the one-line CLI list rows), where any control byte is either an
// injection vector or would break the line layout.
func Inline(s string) string {
	return strings.Map(func(r rune) rune {
		if isUnsafeControl(r, false) {
			return -1
		}
		return r
	}, s)
}

// Block strips control characters EXCEPT newline and tab, for multi-line
// bodies (descriptions, notes, the raw `:bd` output overlay) where those
// two are legitimate formatting.
func Block(s string) string {
	return strings.Map(func(r rune) rune {
		if isUnsafeControl(r, true) {
			return -1
		}
		return r
	}, s)
}
