package sanitize

import (
	"strings"
	"testing"
)

func TestInline_StripsAllControls(t *testing.T) {
	in := "pwn\x1b]52;c;ZWdpdA==\x07\x1b[31mred\x1b[0m\ttab\nnl"
	got := Inline(in)
	for _, r := range []rune{0x1b, '\n', '\t', 0x07} {
		if strings.ContainsRune(got, r) {
			t.Errorf("Inline must strip %#x; got %q", r, got)
		}
	}
	if !strings.Contains(got, "red") || !strings.Contains(got, "[31m") {
		t.Errorf("printable remnant should survive (defanged); got %q", got)
	}
}

func TestBlock_KeepsNewlineTabOnly(t *testing.T) {
	got := Block("a\x1bb\nc\td\x07e")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Errorf("Block must strip ESC/BEL; got %q", got)
	}
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Errorf("Block must keep newline/tab; got %q", got)
	}
}
