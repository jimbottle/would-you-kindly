# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Human-in-the-loop contract** (`docs/CONTRACT.md`): tasks carry the
  `human` label, the description holds the runbook, and a
  `src:agent` / `src:human` label distinguishes who filed the work.
- **Bubble Tea TUI** (`cmd/wyk`) over bd, with vim-style navigation
  (`j`/`k`/`g`/`G`), `enter` to expand an issue and read its
  instructions, `esc` to return, `/` for a fuzzy title+body filter,
  `h` to jump directly to the human-flagged view, `tab` to cycle
  through preset filters (`all`/`ready`/`human`/`mine`/`blocked`),
  `r` to refresh, and a status bar showing the active preset, counts,
  and last-sync time.
- **bd CLI client** (`internal/beads`) that shells out to `bd` and
  parses its JSON, tolerating unknown fields for forward compatibility,
  with typed errors for the two common failure modes (bd not installed,
  no `.beads` workspace).
- **Periodic refresh** every 10 seconds — no filesystem-watcher
  dependency.
- **Non-TTY probe mode** (`wyk --probe`) for scripts and CI: prints
  human-flagged issues and exits with a meaningful status code.

### Fixed

- **TUI fetch race**: `fetchedMsg` now carries the originating preset
  and stale results are dropped so a tick can't clobber a user-initiated
  preset switch.
- **TUI auto-refresh**: the 10s tick suspends when bd is missing or the
  workspace is absent (terminal errors), and resumes after a successful
  manual refresh (`r`).
- **TUI filter prompt**: `ctrl+c` now quits from the `/` prompt; cursor
  blink ticks are forwarded to the textinput so the prompt's cursor
  animates as expected.
- **TUI preset switch**: the visible list is cleared on preset change
  so the old preset's rows don't render under the new header during
  the fetch in-flight.
- **filter.Query**: `PresetReady` and `PresetAll` return an empty
  string (sentinel) rather than a plausible-but-wrong query, so a
  Source that forgets to special-case them fails loudly.
- **`--me` resolution**: shells out to `git config user.email` only
  when the flag is empty, removing a startup hitch and an implicit
  dependency on git being on `PATH`.
- **README**: lists Go 1.26+ to match `go.mod`.
- **CI**: pinned to Go `1.26.x`, scoped `pull_request` to `main`,
  added `permissions: contents: read` and a 10-minute job timeout.
- **CHANGELOG**: `[Unreleased]` link points at `/commits/main` (a
  resolvable URL) until there's a tag to compare against.
- **docs/CONTRACT.md**: dropped the false "return the same set"
  equivalence claim between `bd query` and the `bd list --status=…`
  enumeration (the enumeration omits `deferred`); softened the
  `src:agent`/`src:human` invariant to acknowledge legacy issues with
  no source label.
- **TUI duplicate tick chains**: a generation counter on `tickMsg`
  retires stale ticks, so rapid recovery from a terminal-error state
  (refresh within 10s) no longer spawns concurrent tick chains.
- **TUI loading state**: `switchPreset` and manual refresh now set a
  `loading` flag the view renders as `loading…`, distinguishing an
  in-flight fetch from a genuinely empty result.
- **TUI initial paint**: `New(...)` starts in the `loading` state, so
  the first paint before `Init`'s fetch returns no longer renders
  "no issues — bd returned an empty list" for a slow startup.
- **TUI tick liveness on recovery**: `fetchedMsg` re-arms the tick
  chain when it observes a terminal-error → success transition,
  closing a rare interleaving where a slow recovery could leave
  auto-refresh permanently dormant.

### Phase 2.A — bd Client write methods

- `internal/beads.Client` gains `Close`, `AddLabel`, `RemoveLabel`,
  `Note`, and `UpdateDescription`. Every write passes
  `--dolt-auto-commit=on` so it persists past the dolt working set.
- The inner exec call is now a swappable `runner` field so tests can
  verify argv (and stdin, for `UpdateDescription`) without spawning
  the real bd binary.

### Phase 2.B — TUI write actions

- New `Mutator` interface alongside the existing `Source`; the TUI
  checks at runtime whether its backend implements it, falling back
  to a read-only hint if not.
- New keystrokes: `c` (close, with `[y/N]` confirmation), `H` (toggle
  `human` label), `n` (append a note via a text prompt).
- Writes show a transient status banner above the help bar and
  trigger a refetch on success; failures surface the bd error message
  in the same banner without losing the current list.

### Out of scope (still deferred)

- The agent-side `pkg/handoff` skill (Phase 2.C — next).
- A `wyk init` command and post-commit hook (Phase 2.D — deferred).
- Any background daemon.

[Unreleased]: https://github.com/jimbottle/would-you-kindly/commits/main
