// Package clipboard sends text to the system clipboard via OSC 52
// escape sequences. Chosen over the OS-specific helper-binary route
// (pbcopy/xclip/wl-copy) because OSC 52 works through SSH and inside
// tmux/screen — a wyk user on a remote dev box would otherwise see
// the copy succeed locally on the SSH host but never reach their
// laptop's clipboard. Every modern terminal (iTerm2, Terminal.app,
// kitty, alacritty, wezterm, gnome-terminal, recent xterm) honors
// OSC 52 with permissive defaults; tmux honors it when
// `set -g allow-passthrough on` is set.
//
// The package writes directly to /dev/tty so bubbletea's
// alt-screen capture of stdout doesn't intercept the escape
// sequence — the terminal still sees and interprets it but no
// glyph is rendered on screen.
package clipboard

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

// Copy sends text to the terminal's clipboard via OSC 52. Returns
// an error if /dev/tty isn't openable (Windows, headless CI) or
// the write fails partway. Callers should surface the error to the
// user as a status banner — silent failure is worse than "couldn't
// copy" because the user would think it worked and paste stale
// content from earlier.
func Copy(text string) error {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /dev/tty: %w", err)
	}
	defer f.Close()
	return writeOSC52(f, text)
}

// writeOSC52 emits the OSC 52 escape sequence carrying text's
// base64 payload to w. Extracted so the wire format can be pinned
// in tests against a bytes.Buffer — Copy itself is just the tty
// open + this write.
//
// OSC 52 ; c (clipboard target) ; <base64 payload> BEL.
// Some terminals accept ST (\x1b\\) instead of BEL; BEL is the
// more compatible choice and what xterm originally documented.
func writeOSC52(w io.Writer, text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if _, err := fmt.Fprintf(w, "\x1b]52;c;%s\x07", encoded); err != nil {
		return fmt.Errorf("write OSC 52: %w", err)
	}
	return nil
}
