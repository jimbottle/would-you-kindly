#!/usr/bin/env bash
#
# wyk-demo-seed.sh — build a throwaway bd workspace for the README demo cast.
#
# Used by docs/screenshots/wyk-demo.tape (VHS) to seed a deterministic,
# disposable workspace so the recording never touches real registered repos.
# Re-runnable: it nukes and rebuilds the target dir each time.
#
#   bash docs/screenshots/wyk-demo-seed.sh [target-dir]   # default: /tmp/wyk-demo
#
set -euo pipefail

# Default to /tmp/wyk-demo — NOT $TMPDIR (which is /var/folders/... on macOS) —
# because wyk-demo.tape hardcodes /tmp/wyk-demo. A bare standalone run must seed
# the same dir the tape reads from. render-demo.sh passes this explicitly.
DEMO_DIR="${1:-/tmp/wyk-demo}"

# The bd-create guard (wyk hook) only blocks `wyk create`; we call bd
# directly here, but set this so the script is safe under any wrapper.
export WYK_ALLOW_BD_CREATE=1

rm -rf "$DEMO_DIR"
mkdir -p "$DEMO_DIR"
cd "$DEMO_DIR"

bd init --prefix demo >/dev/null 2>&1

# Three background issues so the multi-issue view looks alive but not
# noisy. All default to the AGENT owner badge (no human/src label). The
# live handoff in the tape adds the fourth, HUMAN-badged issue.
bd create --title "Add Prometheus /metrics endpoint" \
  --type feature --priority 2 -a demo --dolt-auto-commit=on >/dev/null
bd create --title "Flaky TestMultiBDSource on macOS CI" \
  --type bug --priority 2 -a demo --dolt-auto-commit=on >/dev/null
bd create --title "Bump Go toolchain to 1.26" \
  --type chore --priority 3 -a demo --dolt-auto-commit=on >/dev/null

# When XDG_CONFIG_HOME points at a throwaway dir (the tape sets this),
# write a one-entry registry so a plain `wyk` targets ONLY this demo
# workspace — clean commands, no "no repos registered" banner, and the
# user's real registry stays invisible.
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
  mkdir -p "$XDG_CONFIG_HOME/wyk"
  cat > "$XDG_CONFIG_HOME/wyk/repos.json" <<JSON
{
  "version": 1,
  "repos": [
    { "name": "staging-platform", "path": "$DEMO_DIR" }
  ]
}
JSON
fi

echo "seeded $DEMO_DIR"
