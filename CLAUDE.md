# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:7510c1e2 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->


## Build & Test

```bash
go build ./...                # all packages compile
go build -o ./bin/wyk ./cmd/wyk   # produce the CLI binary
go test ./...                 # full suite
go test -race ./...           # race detector (load-bearing for MultiBDSource)
go vet ./...
```

CI runs vet + build + `test -race` on every PR; keep them green locally
before pushing.

Most bd-shelling code uses a swappable runner field so tests can
inject a stub command without needing a real `bd` binary on PATH —
mimic that pattern (e.g. `beads.Client`'s `runner`, `runScanAndRegister`'s
`probeBDFunc`) when adding new bd-dispatching code.

## Architecture Overview

- **`cmd/wyk`** — the CLI binary and subcommand dispatchers. Each
  subcommand (`init`, `handoff`, `hook`, `inbox`, `stats`, `doctor`,
  `registry`, `version`) owns its own `flag.FlagSet`; `main.go` switches
  on `os.Args[1]` before parsing top-level flags. The TUI starts when no
  subcommand matches.
- **`internal/beads`** — typed wrapper over the `bd` CLI. `Client` shells
  out via a swappable runner; methods (`Query`, `List`, `Ready`, `Show`,
  `Create`, `Close`, `AddLabel`, `RemoveLabel`, `Note`, `UpdateDescription`)
  parse bd's JSON. Never reads bd's storage directly.
- **`internal/tui`** — the Bubble Tea Model + the `Source` / `Mutator` /
  `Detailer` / `MultiSource` interfaces it consumes. `BDSource` wraps one
  workspace; `MultiBDSource` parallel-fetches across many and unions the
  result. Per-sub errors travel atomically on `fetchedMsg`.
- **`internal/registry`** — JSON-backed registry of bd workspaces at
  `~/.config/wyk/repos.json` (XDG-aware). `Load` / `Save` / `Add` /
  `Remove` / `RemoveByName` / `Has`. Save writes atomically via
  temp-file + rename.
- **`internal/filter`** — preset → bd-query mapping (`all`, `ready`,
  `human`, `mine`, `blocked`) plus the keymap's preset-cycle order.
- **`pkg/handoff`** — the `BounceToHuman` helper that filed bd issues use
  to transition agent → human (adds `human` label, replaces description
  with the runbook). External programs can call this directly.

## Conventions & Patterns

**bd writes must pass `--dolt-auto-commit=on`.** bd defaults to leaving
writes in Dolt's working set; without the flag, `bd dolt push` later
can't see them. Every `Client` write method already does this — when
shelling out from a script/test, do the same.

**The handoff contract.** A task is for a human when it carries the
`human` label; its description IS the runbook the human follows;
`src:agent` / `src:human` labels record who filed it. See
`docs/CONTRACT.md` for the full text. New code that changes who's
holding the work should obey this contract — adding/removing `human`,
preserving the source label.

**Source / Mutator / Detailer.** Three small interfaces in
`internal/tui` segment the TUI's read, write, and lazy-detail needs.
Implementations can opt into any subset; the model runtime
type-asserts. `MultiSource` is a fourth (optional) interface that lets
the model render per-sub fetch failures.

**Commit conventions.** Conventional-Commits prefixes (`feat`, `fix`,
`docs`, `ci`, `chore`) with a scope (`feat(tui):`, `fix(doctor):`).
Issues are closed via `Closes: <id>` trailers (one ID per line — the
auto-close hook deliberately rejects multi-ID lines, see README).

**Trunc is rune-aware** (`internal/tui.trunc`) — width semantics
throughout the TUI are visual, not byte. Don't reach for byte-level
slicing for column widths.

**Registry path normalisation.** `internal/registry.normalizePath`
resolves symlinks before dedup so the same workspace can't register
twice via macOS's `/var` → `/private/var` shortcut or any user
shortcut symlink.

**Feedback on wyk's experience IS wyk product feedback.** When the
user comments on the TUI, the CLI, the handoff flow, or any wyk
surface ("the headers don't show", "the W column is unclear", "this
should be deferred not held open"), the right first move is to file
a bd issue capturing the improvement, then handle the immediate
symptom. Don't stop at patching the one-off case — every piece of
friction the user reports has either revealed a real product gap or
a missing piece of convention documentation, and both deserve
durable fixes. The implicit rule the user follows: the agent's
behaviour against wyk is data about wyk itself.

**Status lifecycle.** When filing or updating bd issues from this
project, follow the lifecycle documented in `docs/CONTRACT.md` —
default to `open`, reach for `deferred` when the blocker is "the
rest of the project hasn't caught up yet", `blocked` (with
`--add-dependency`) when the blocker is another tracked issue, and
the `human` label + handoff when a human is genuinely required.
Holding-open a task that depends on an unstable subsystem clutters
the ready view and creates false signals about what someone could
start now.

**Act on the agent inbox.** If `wyk inbox` returns items at any
point in a session, the default move is to WORK them — they're
tasks the human has bounced back, meaning whatever unblocker the
agent specified has arrived. Treating inbox items as "things to
handle later" defeats the round-trip. Exception: the user is
mid-conversation about something explicitly urgent, or the
expected unblocker artifact is actually missing (re-flag `human`
with a note explaining what's still needed; don't sit silently).
