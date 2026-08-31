# wyk-demo.tape — the agent→human handoff round-trip, for the README.
#
# Regenerate the GIF after a UI change with:
#   bash docs/screenshots/render-demo.sh
#
# That wrapper seeds a disposable bd workspace OUTSIDE the recorded
# terminal (embedded Dolt's `bd init` corrupts the ttyd pty VHS records
# into) and then runs `vhs` on this tape. The tape itself only scopes the
# shell to that pre-seeded workspace via XDG_CONFIG_HOME (a one-entry
# registry) so every `wyk` command targets it alone and the user's real
# registered repos stay invisible.

Output docs/screenshots/wyk-demo.gif

Require wyk
Require bd

Set Shell "bash"
Set FontSize 16
Set Width 1640
Set Height 860
Set Padding 24
Set Framerate 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms

# --- Hidden setup: scope the shell to the pre-seeded demo workspace ---
# These literal paths must match DEMO_DIR / DEMO_CFG in render-demo.sh and the
# seed script's default — VHS tapes can't read shell variables, so keep them in
# sync if you ever change the demo location.
Hide
Type "export XDG_CONFIG_HOME=/tmp/wyk-demo-cfg XDG_CACHE_HOME=/tmp/wyk-demo-cache XDG_STATE_HOME=/tmp/wyk-demo-state"
Enter
Sleep 400ms
Type "cd /tmp/wyk-demo"
Enter
Sleep 400ms
Type "clear"
Enter
Sleep 800ms
Show

# --- 1) Agent side: file a runbook and hand it to a human ---
Type "echo 'Reset the vault password; roll the staging k8s secret.' | wyk handoff -create 'Rotate the staging DB password' -priority 1"
Sleep 800ms
Enter
# bd's embedded Dolt cold-starts on the first write; wait it out so the
# next keystrokes don't land in the shell before the prompt is back.
# (Render with nothing else loading the machine — a concurrent build or
# test run stretches every bd call past these waits.)
Sleep 10s

# --- 2) Human side: open the TUI ---
# The terminal is sized past the split breakpoint (140x36) so the demo
# shows the list + detail pane layout; the PNG below is README's still.
Type "wyk"
Sleep 600ms
Enter
# Embedded Dolt cold-starts on the first bd call; give it time so the
# still doesn't capture the loading spinner.
Sleep 9s
Screenshot docs/screenshots/wyk-tui.png

# h → the human view: "whose move is it?"
Type "h"
Sleep 3s

# enter → read the runbook the agent left
Enter
Sleep 4s

# a → close it (resolved), y to confirm
Type "a"
Sleep 1.5s
Type "y"
Sleep 2.5s

# esc back to the list (the human queue is clear), q to quit
Escape
Sleep 2s
Type "q"
Sleep 1.5s
