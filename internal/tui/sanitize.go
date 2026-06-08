package tui

import "strings"

// Issue titles, descriptions, notes and raw `bd` output are
// attacker-influenceable: in a shared or multi-repo workspace a teammate
// (or a malicious repo you registered) can author them. Rendered raw,
// an embedded terminal escape — OSC 52 (clipboard write), cursor/title
// manipulation, a CSI color run — would be INTERPRETED by the terminal,
// not shown. lipgloss does not strip these. So every untrusted string is
// passed through one of the sanitizers below before it reaches the
// screen (would-you-kindly-waub).
//
// Stripping the control BYTES is enough to defang a sequence: a CSI like
// "\x1b[31m" without its leading ESC is just the harmless literal text
// "[31m"; OSC 52 without its ESC/BEL framing is inert. We drop the
// control runes rather than escape them so the visible remnant stays
// compact.

// isUnsafeControl reports whether r is a control character we must strip:
// C0 (0x00–0x1F, including ESC), DEL (0x7F), and C1 (0x80–0x9F). When
// allowWhitespace is true, the layout-safe whitespace controls newline
// and tab are kept (for multi-line bodies).
func isUnsafeControl(r rune, allowWhitespace bool) bool {
	if allowWhitespace && (r == '\n' || r == '\t') {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// sanitizeInline strips ALL control characters — including newline and
// tab — for single-line fields (issue titles, dependency-row titles,
// repo/branch labels), where any control byte is either an injection
// vector or would break the row layout.
func sanitizeInline(s string) string {
	return strings.Map(func(r rune) rune {
		if isUnsafeControl(r, false) {
			return -1
		}
		return r
	}, s)
}

// sanitizeBlock strips control characters EXCEPT newline and tab, for
// multi-line bodies (descriptions, notes, the raw `:bd` output overlay)
// where those two are legitimate formatting.
func sanitizeBlock(s string) string {
	return strings.Map(func(r rune) rune {
		if isUnsafeControl(r, true) {
			return -1
		}
		return r
	}, s)
}
