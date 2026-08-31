#!/usr/bin/env bash
#
# render-demo.sh — regenerate docs/screenshots/wyk-demo.gif.
#
# Seeds a throwaway bd workspace OUTSIDE the terminal VHS records (embedded
# Dolt's `bd init` corrupts that pty), then runs vhs on wyk-demo.tape. Run
# from anywhere; paths are resolved relative to this script's repo.
#
#   bash docs/screenshots/render-demo.sh
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# These two paths are HARDCODED in wyk-demo.tape's hidden setup (the tape's
# `export XDG_CONFIG_HOME=...` and `cd ...` lines) and are the seed script's
# defaults — VHS tapes can't read shell variables, so the literals must stay
# in sync across all three files. If you change them here, change them in the
# tape and wyk-demo-seed.sh too.
DEMO_DIR=/tmp/wyk-demo
DEMO_CFG=/tmp/wyk-demo-cfg
# The cache (XDG_CACHE_HOME) and state (XDG_STATE_HOME) dirs are isolated
# too: wyk warm-starts from ~/.cache/wyk/last-fetch.json, and a demo that
# only redirected the registry once painted the real, private backlog
# into the README screenshot (would-you-kindly-mup1).
DEMO_CACHE=/tmp/wyk-demo-cache
DEMO_STATE=/tmp/wyk-demo-state

command -v wyk >/dev/null || { echo "render-demo: 'wyk' not on PATH (go install ./cmd/wyk)"; exit 1; }
command -v vhs >/dev/null || { echo "render-demo: 'vhs' not on PATH (brew install vhs)"; exit 1; }

# Cleanup division of labour: we own the isolated config dir; the seed script
# owns its DEMO_DIR teardown (it `rm -rf`s the target before rebuilding).
rm -rf "$DEMO_CFG" "$DEMO_CACHE" "$DEMO_STATE"
XDG_CONFIG_HOME="$DEMO_CFG" bash docs/screenshots/wyk-demo-seed.sh "$DEMO_DIR"

vhs docs/screenshots/wyk-demo.tape
echo "rendered docs/screenshots/wyk-demo.gif"
