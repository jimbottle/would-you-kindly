#!/usr/bin/env bash
# SessionStart hook for the wyk plugin: surface the agent inbox (issues
# a human has bounced back) so the session opens already knowing what to
# act on, per wyk's "act on the agent inbox" convention.
#
# Deliberately best-effort and quiet: if wyk isn't installed, or there
# are no registered repos / no inbox items, it prints nothing and exits
# 0 so it never disrupts a session that doesn't use wyk.
set -u

command -v wyk >/dev/null 2>&1 || exit 0

# `wyk inbox` already prints nothing meaningful when the inbox is empty;
# swallow any error (e.g. no registered repos) rather than surfacing it.
wyk inbox 2>/dev/null || true
exit 0
