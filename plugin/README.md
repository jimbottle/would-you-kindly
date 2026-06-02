# wyk — Claude Code plugin

This is the [Claude Code plugin](https://code.claude.com/docs/en/plugins)
form of the [`wyk`](https://github.com/jimbottle/would-you-kindly) agent
integration. It bundles the same agent skills that `wyk skills install`
ships, so a single plugin install wires them into every Claude session —
no per-machine `wyk skills install` step.

## What it bundles

- **Skills** (`skills/`) — `wyk`, `wyk-handoff`, `wyk-project-review`,
  byte-identical to the binary's embedded copies (kept in sync from
  `internal/skills/data` via `make plugin-skills`; a test fails if they
  drift). They auto-load when the plugin is installed.
- **A SessionStart hook** (`hooks/`) — best-effort: runs `wyk inbox` at
  session start to surface issues a human has bounced back. Silent if
  `wyk` isn't installed or there's nothing to show, so it never disrupts
  a non-wyk session.

## Install

```
/plugin marketplace add jimbottle/would-you-kindly
/plugin install wyk@wyk
```

(The marketplace lives at the repo root's `.claude-plugin/marketplace.json`;
the first `wyk` is the plugin, the second is the marketplace name.)

## What it does NOT bundle, and why

- **The git post-commit auto-close hook.** That's a *git* hook (it reads
  `Closes: <id>` commit trailers), not a Claude Code hook, and it's
  installed per-repo by `wyk init`. A plugin can't install git hooks into
  your repositories, so this stays with the CLI. Run `wyk init` in a repo
  to wire it.
- **An MCP server.** Considered and deliberately skipped: the `wyk` CLI is
  lean and agents already call it via Bash, so a tool-surface MCP server
  would add a maintenance burden for little gain. Worth revisiting only if
  a host genuinely prefers tools over a CLI.

## Requirements

The skills and the session hook drive the `wyk` (and `bd`) CLIs — install
`wyk` separately (`go install github.com/jimbottle/would-you-kindly/cmd/wyk@latest`
or `wyk update`). The plugin carries the *instructions*; the binary does
the work.
