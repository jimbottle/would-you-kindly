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

DEMO_DIR=/tmp/wyk-demo
DEMO_CFG=/tmp/wyk-demo-cfg

command -v wyk >/dev/null || { echo "render-demo: 'wyk' not on PATH (go install ./cmd/wyk)"; exit 1; }
command -v vhs >/dev/null || { echo "render-demo: 'vhs' not on PATH (brew install vhs)"; exit 1; }

rm -rf "$DEMO_CFG"
XDG_CONFIG_HOME="$DEMO_CFG" bash docs/screenshots/wyk-demo-seed.sh "$DEMO_DIR"

vhs docs/screenshots/wyk-demo.tape
echo "rendered docs/screenshots/wyk-demo.gif"
