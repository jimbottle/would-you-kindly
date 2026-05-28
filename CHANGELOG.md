# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`internal/registry`** — JSON-backed registry of bd workspaces at
  `~/.config/wyk/repos.json` (XDG-aware). Load, Save, Add, Remove,
  Has. Idempotent add; symlink-resolving path normalisation so the
  same repo via two paths counts as one entry.
- **`wyk init` now bootstraps the whole layer**: runs `bd init` if
  `.beads` is missing, installs the post-commit auto-close hook
  (existing behavior), and registers the repo in repos.json. Each
  step is independently idempotent. New flags `-skip-bd-init` and
  `-skip-register` let you opt out of either.

### Added (continued)

- **Multi-repo TUI**: when the registry has 2+ entries, `wyk` (no
  args) builds a `MultiBDSource` that queries every registered
  workspace and unions the results. The TUI renders extra `Repo`
  and `Branch` columns at the front of the table (hidden in
  single-repo mode so `wyk -C <dir>` stays compact). Writes route
  by ID → repo lookup, populated on every fetch; per-repo errors
  are tolerated as long as some repo returned data.
- **`MultiBDSource` partial-failure tolerance**: one bad workspace
  (e.g. moved/deleted on disk) does not poison the whole view; only
  if every sub fails does the user see an error.

### Fixed (Phase 4.B review pass — round 2)

- **`gitBranch` context actually plumbed**: `branchFn` is now
  `func(context.Context) string` rather than a closure that
  hard-coded `context.Background()`. A canceled `Fetch` (TUI quit
  or refresh-during-refresh) now unblocks any in-flight
  `git rev-parse` instead of waiting for it to return.
- **Dropped the legacy bare-ID fallback in `MultiBDSource.repoForIssue`**:
  every in-tree caller obtains the Issue from a `Source.Fetch`
  which populates Repo, so a missing Repo on a multi-repo write is
  a programmer error — surface it loudly rather than silently
  mis-routing via a stale id→repo map. `MultiBDSource` is smaller
  too (no `idToRepo` map, no `sync.RWMutex`). Locked in by
  `TestMultiBDSource_WriteWithEmptyRepoErrors`.
- **`wyk init -dry-run` register preview matches real run**: when
  the repo is already registered, the dry-run now prints
  `already registered in <path>` instead of the misleading
  `would register …`.

### Fixed (Phase 4.B review pass)

- **`wyk init` idempotency**: the "hook already installed" branch
  used to `return 0` before running the registry step. A previous
  init that failed to register (transient `~/.config` permission
  error) would never get a chance to retry. The control flow now
  flows through to the registry step on every run.
- **`registry.Save` atomicity**: writes go to a temp file in the
  same directory and `os.Rename` into place, so a crash, signal,
  or a concurrent second `wyk init` leaves either the old file or
  a complete new one — never a truncated half-write.
- **Multi-repo write routing**: the `Mutator` interface now takes
  the full `beads.Issue` (with `Repo` populated) instead of a bare
  ID. Two workspaces that happen to use the same ID can no longer
  cross-route writes. Locked in by
  `TestMultiBDSource_WriteRoutesByRepoNotID`.
- **Single-registered-repo case**: `wyk` (no args) with one entry
  in the registry now uses that entry rather than falling back to
  cwd. Previously, running `wyk` from outside a registered repo's
  directory would surface an opaque "no workspace" error instead
  of just opening the one repo the user had registered.
- **Parallel multi-repo fetches**: sub fetches now run concurrently
  (via `sync.WaitGroup`), turning N × bd-startup latency into
  roughly one bd-startup latency on every refresh.
- **`gitBranch` honors context**: uses `exec.CommandContext`, so a
  canceled fetch no longer leaves a stranded git subprocess.
- **`NewMultiBDSource` length check**: returns an error rather than
  panicking when clients and names slices disagree.
- **`findGitDir` + `findRepoRoot` merged**: one `git rev-parse
  --git-dir --show-toplevel` call instead of two forks per init.
- **Documented registry version-less load**: tests now pin the
  behavior — a JSON object without a `version` field is treated as
  v1 (older formats are tolerated, not silently corrupted).

### Changed

- **TUI list now renders as a table** with a column header row and
  fixed widths: `ID  T  Status  P  Updated  Title  [HUMAN]`. Type
  and updated-relative-date (e.g. `5m ago`, `3d ago`, `Jan 2`) are
  new columns; status is a colored text label instead of a one-char
  icon. Matches the information density of the roborev TUI.
- **`all` preset narrows to non-closed** — opening wyk used to show
  the full repo history (including closed issues) because the
  underlying call was `bd list --all`. The preset now maps to
  `bd list` (no `--all`), so the first paint is "actionable work",
  not "everything ever filed". `internal/beads.Client` keeps a
  separate `ListAll` method for a future archive view.

## [0.1.0] — 2026-05-28

First tagged release. Builds out the read path (Phase 1 TUI), the
write path (Phase 2 client + TUI + handoff CLI + post-commit hook),
and the agent-facing skill (Phase 3.A) on top of a single small
human-in-the-loop convention pinned in `docs/CONTRACT.md`. Polished
into a usable terminal product (Phase 3.B). Reviewed and iterated
through multiple roborev rounds.

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

### Phase 2.C — agent-side handoff

- New `pkg/handoff` package with `BounceToHuman(ctx, mutator, id,
  runbook)` — the single call an agent makes to hand a beads issue
  back to a human. Tags the issue with `human`, then overwrites its
  description with the runbook. Partial-failure contract is explicit:
  if the description write fails after the label landed, the issue
  stays flagged so a retry preserves the handoff signal.
- New `wyk handoff <id>` CLI subcommand exposing the same operation
  for non-Go agents. Reads the runbook from stdin or `--file`.

### Fixed (Phase 2 review pass)

- **TUI confirm/note race**: `c` and `n` now capture the target issue
  ID on prompt entry; a refetch arriving before the user confirms
  can no longer close or note a different issue, and if the target
  vanishes from the list the prompt cancels with an explanatory
  banner instead of panicking on an out-of-range cursor.
- **`wyk handoff --help` exit code**: `flag.ErrHelp` is now a
  successful exit (0), no longer the "bd missing / no workspace"
  code (2). Genuine usage errors get a dedicated 64 instead of
  overloading 2.
- **`wyk handoff` empty / TTY-stdin guard**: refuses to run with an
  empty runbook (which would silently wipe the description) unless
  `-allow-empty` is passed, and detects TTY stdin to print a usage
  hint instead of blocking on `io.ReadAll`.
- **`BounceToHuman` retry contract**: documented the dependency on
  `bd label add` being idempotent (verified against bd 1.0.4) so the
  retry-after-partial-failure story holds.

### Phase 2.D — repo binding (`wyk init` + auto-close hook)

- New `wyk init` subcommand installs a `.git/hooks/post-commit` that
  scans the commit message for `Closes:`, `Fixes:`, or `Resolves:`
  trailers (case-insensitive, optional `#`/`:` separator, hierarchical
  IDs supported) and auto-closes each referenced bd issue. Refuses
  to overwrite a non-wyk hook without `-force`; idempotent when the
  hook is already a wyk one. `-dry-run` previews.
- New `wyk hook post-commit [<sha>]` subcommand invoked by the
  installed hook. The hook script defers to it so upgrading the wyk
  binary updates the parsing/close logic without needing to
  reinstall the hook. Per-issue close failures (already-closed,
  unknown ID) are logged but never fail the hook — git has already
  made the commit by the time post-commit runs.
- Pure parser (`parseCloseRefs`) is regex-driven and unit-tested
  across a broad range of trailer shapes.

### Fixed (Phase 2.D review pass)

- **`wyk init -dry-run` against a foreign hook** now exits 0 with a
  preview message ("would refuse to overwrite …") instead of the
  usage-error code 64. Scripts that gate on `wyk init -dry-run || …`
  no longer have to special-case the refusal.
- **`wyk hook post-commit -C <dir>`** now also passes `-C <dir>` to
  the `git show` invocation that reads the commit message — without
  it, the hook would have read messages from the process cwd while
  writing to a different repo's bd workspace.
- **One-ID-per-line parser behavior** is now both documented in the
  README and pinned by tests (`Closes: bd-1, bd-2` → no matches;
  `Closes: bd-1 (we'll handle bd-2 next week)` → no matches).
- **Handoff TTY guard fails closed**: when `os.Stdin.Stat()` itself
  errors, the empty-runbook safeguard now refuses rather than
  falling through to `io.ReadAll`.
- **TUI cancel-banner wording**: replaced "no longer in the list"
  with "removed from the workspace by a refresh" so the message
  matches what `issueExists` actually checks (`m.all`, not the
  filtered `m.visible`).
- **Comment cleanup**: tightened the status-banner comment in
  `Update` to match the prompt handlers' actual behavior — they
  only set or clear `m.status` on prompt resolution, not on every
  keystroke.

### Fixed (Phase 3.B review pass)

- **`go mod tidy`**: `sahilm/fuzzy` was added without running tidy,
  so it landed in the indirect block. Tidied — it now sits in the
  direct-require list alongside the Bubble Tea ecosystem.
- **Fuzzy cross-field bleed**: title and description are now scored
  independently and merged on the max score, so a query like `ad`
  can't subsequence-match by picking the `a` from a title and the
  `d` from a description. Locked in by
  `TestFuzzyFilterDoesNotBleedAcrossTitleDescBoundary`.

### Phase 3.B — TUI polish

- **True fuzzy matcher** (`github.com/sahilm/fuzzy` v0.1.2): `/`
  filter now scores subsequence matches and stable-sorts results
  best-first. Substring queries still work (subsumed by fuzzy);
  `rpw` against "rotate password" — which the substring matcher
  missed — now ranks first.
- **`]` / `[` keys**: jump to the next / previous human-flagged
  issue in the current view. Wraps. Status banner says
  "no human-flagged issues in this view" when none exist.
- **`?` help overlay**: modal listing every keybinding, grouped
  (navigation / filters / writes / meta). Source of truth is the
  keymap itself — no copy/paste of help strings. Opens from list
  or detail mode; esc / ? / q dismisses and restores the previous
  mode.

### Phase 3.A — Claude Code skill file

- New `.claude/skills/handoff/SKILL.md` makes the handoff convention
  discoverable to any Claude Code session that opens this repo. The
  skill documents *when* to call `wyk handoff` (steps requiring
  human authority, irreversible decisions, third-party UI clicks),
  *when NOT to* (clarifying questions belong in `AskUserQuestion`,
  tedious-but-doable tasks should just be done), how to write a
  runbook that a human can actually act on, and good vs. bad handoff
  examples. Closes the "last mile" the original brief implied:
  infrastructure → an actually-callable agent skill.

### Out of scope (deferred to a later phase)

- Composing with an existing post-commit hook from another tool
  (today's behavior is "refuse with -force escape hatch").
- A pre-commit hook that validates `Closes:` references resolve to
  real bd issues.
- Any background daemon.

[Unreleased]: https://github.com/jimbottle/would-you-kindly/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.1.0
