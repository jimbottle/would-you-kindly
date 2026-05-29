# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

(nothing yet)

## [0.3.3] — 2026-05-29

Patch — fixes the `wyk update` cache trap surfaced testing v0.3.2's
release flow. Recommended for everyone on v0.3.0/v0.3.1/v0.3.2.

### Fixed

- **`wyk update` now bypasses the 24h cache** and live-fetches the
  release page every invocation. Previously, if the cache was last
  refreshed mid-cycle (e.g. when only `v0.3.0-alpha` was tagged),
  `wyk update` returned the stale answer (`already on v0.3.0
  (latest is v0.3.0-alpha)`) until the TTL expired — so the user
  couldn't actually update for ~24h after each release. The TUI
  nudge and `wyk doctor` keep using the cache (advisory, latency-
  sensitive). `wyk update` now persists the fresh result back to
  the cache so the next nudge reflects the same view the user just
  saw.
- New `updater.PersistLatest(rels)` helper centralises the
  cache-write side effect; reused by `LatestCached` and by the
  new `wyk update` post-fetch write.

### Internals

- `liveFetcher` package var seam in `cmd/wyk/update.go` lets the
  channel-dispatch tests stub the live fetch without network I/O.
  Tests no longer depend on the cache file as the source of
  truth.

## [0.3.2] — 2026-05-29

Research-driven TUI polish round. 12 commits since v0.3.1 — five new
features from the punch list, the HUMAN-BLOCK dep-scan global cap,
the updater channel-stable bug, and a 24× speedup on the tui test
suite.

### Added

- **Scrollable detail view** via `bubbles/viewport.Model`. Long
  runbooks now scroll with `j/k`, PgUp/PgDn, and mouse wheel; the
  badge, title, and meta lines stay fixed above so the row's
  identity never scrolls off. Footer shows scroll percent when
  content exceeds the viewport.
- **`bubbles/spinner`** animates the first-paint loading state
  instead of a static `loading…`.
- **Flash auto-clear** for write-success banners (`closed wyk-42`)
  after 4s. Gen-tagged so a stale clear can't wipe a newer status.
- **Preset-aware empty-state copy**. The `human` preset
  celebrates `✓ no human-flagged issues — nothing waiting on you
  right now`; ready / mine / blocked describe their absence
  factually; the default preset on a fresh repo points at
  `wyk handoff -create`.
- **Responsibility badge promoted** in the detail view — for a
  `← HUMAN` row the badge is the headline, on its own line above
  the title.
- **`bubbles/help` drives the status-bar footer**, sourced from the
  keymap so the bindings can't drift from the actual handlers.
  Read-only mode swaps in a write-free subset.
- **Priority quick-cap** keys: `1` → P0 only, `2` → ≤P1, `3` → ≤P2,
  `4` → ≤P3, `0` clears the cap. Applied before fuzzy match so
  query + cap compose correctly.
- **Filter chip strip** above the table — amber pills showing the
  non-default preset, the priority cap, and the active sort axis.
  Empty when nothing's filtered so a fresh view stays chrome-free.
- **`s` cycles sort key**: none → priority (asc) → updated (desc)
  → repo (asc) → id (asc) → none. Sorts run on a clone so `m.all`
  stays in bd's native order.
- **Column-header sort arrow** — `P↑`, `Updated↓`, `Repo↑`, `ID↑`
  decorate the active column so the user knows which axis is
  sorted without reading the chip.

### Changed

- **Error banners are sticky** — failed writes (`close wyk-42
  failed: bd: issue is pinned`) stay until the next keystroke so
  a user who glances away doesn't lose the failure text. Success
  banners still auto-clear at 4s.
- **HUMAN-BLOCK dep-scan semaphore hoisted** from each `BDSource`
  into `MultiBDSource` — the cap is now truly global
  (`markBlockedByHumanConcurrency = 8` total) rather than
  `M × 8` per refresh.

### Fixed

- **`wyk update -channel stable` resolves correctly** through
  prereleases. `LatestLive` returns the full `[]Release` page;
  new `PickStable` walks for the newest non-prerelease;
  `runUpdate` dispatches by channel. Cache schema keeps the
  legacy `latest` field for v0.3.0/v0.3.1 back-compat. Channel
  validation rejects typos like `-channel stabel` with exit 64.
- **Atomic cache writes** — `internal/updater.writeCache` uses
  temp file + `Rename` (mirrors `registry.Save`) so a concurrent
  reader can't observe a half-written `update.json`.
- **`setPriorityCap`/`setSortKey` side effects tested** — cursor
  reset + scroll re-clamp are now pinned by regression tests.
- **tui test suite speedup 12s → 0.5s** — `flashClearDelay` is
  now a `var` so flash tests can shorten it via the
  `withFlashClearDelay(t, time.Millisecond)` helper.

## [0.3.1] — 2026-05-28

Owner column expansion: the per-row badge now identifies AGENT
and HUMAN-BLOCK alongside the existing HUMAN variants, so the
column tells the full "whose move is it" story rather than
flagging only human-flagged rows.

### Added

- **AGENT badge** (green) — surfaces on rows that match the agent
  inbox query (`src:agent` AND NOT `human`). Identifies tasks the
  agent owns and can act on now.
- **HUMAN-BLOCK badge** (amber) — surfaces on agent-owned rows
  whose dependency set includes a human-labeled task. Detection
  shells out to `bd dep list <id>` per candidate (parallel
  goroutines, best-effort, same-workspace only). The agent
  literally can't move these forward until the blocker closes;
  the inbox imperative excludes them.

### Changed

- Responsibility column header renamed `human` → `owner` to
  reflect the broader scope (was: only HUMAN; now: HUMAN /
  AGENT / HUMAN-BLOCK / blank).
- Column width widened 9 → 13 to fit `HUMAN-BLOCK`. Shorter
  badges get trailing whitespace; alignment of subsequent
  columns shifts right by 4.
- Convention surfaces (`wyk conventions`, SKILL.md,
  `docs/CONTRACT.md`, `bd remember` memory) call out
  HUMAN-BLOCK as an explicit exception to "work inbox items now"
  — agent can't unblock them, skip to the next.

### Internals

- New `beads.Issue.BlockedByHuman` field (`json:"-"`).
- New `beads.Client.ListDeps(ctx, id)` wrapping
  `bd dep list <id> --json`.
- New `markBlockedByHuman` in `internal/tui/source.go` runs the
  dep check in parallel inside `BDSource.Fetch` for every
  agent-inbox candidate.

## [0.3.0] — 2026-05-28

Supersedes the v0.3.0-alpha prerelease. Cuts as a non-prerelease
so `go install ...@latest` finally resolves to a current binary —
the v0.3.0-alpha tag had pulled users back to v0.2.3 via Go's
"@latest skips prereleases" rule, which was the exact trap this
release exists to close.

### Added (since v0.3.0-alpha)

- **`wyk update`** subcommand. Reads the cached release snapshot,
  compares against the running binary's build info, and runs the
  right `go install ...@<tag>` with a `[y/N]` confirm. Flags:
  `-y` skip prompt, `-dry-run` print without exec, `-channel
  stable` skip prereleases (default: include them).
- **Update-available nudge** in the TUI and `wyk doctor`. A
  one-line "↑ wyk vX.Y.Z available — run `wyk update`" appears
  above the TUI status bar; doctor adds a WARN row with the same
  info. Both read from a 24h-TTL cache at `$XDG_CACHE_HOME/wyk/
  update.json`; refresh happens out of band in a background
  goroutine launched at TUI startup so the hot path stays free of
  network I/O.
- **`internal/updater` package** — `LatestLive` (hits the GitHub
  releases API including prereleases), `LatestCached` (TTL +
  stale-fallback on network error), `IsNewer` (semver ordering),
  `InstallCommand`. Reusable from any caller; unit-tested
  end-to-end with stub clients.
- **Status lifecycle** is documented across the convention
  surfaces (skill, conventions, contract, bd-remember). The new
  rule: reach for `deferred` instead of holding-open when the
  blocker is "the rest of the project hasn't caught up yet"; reach
  for `blocked` (with `--add-dependency`) when the blocker is
  another tracked issue.
- **Inbox imperative**: the agent's default move on a non-empty
  `wyk inbox` is to WORK the highest-priority item, not to
  acknowledge and move on. Surfaced via skill, conventions,
  contract, `bd remember`, and CLAUDE.md.
- **Meta-rule in CLAUDE.md**: feedback on the wyk experience IS
  wyk product feedback — file a bd issue, then handle the symptom.

### Notes

Everything from [0.3.0-alpha] below is also in this release —
that section captures the bulk of the work (agent discoverability,
TUI layout polish, scroll/refresh robustness, registry CLI,
handoff convention tightening). v0.3.0 adds the update mechanism,
the lifecycle/inbox/meta-rule guidance, and the retroactive bd
paper trail for three earlier UI changes that landed without
tracking.

The deferred TUI screenshot work (`would-you-kindly-md3`) ships
later when the TUI layout stabilises further.

## [0.3.0-alpha] — 2026-05-28

The alpha-quality version. Everything in v0.2.3 plus a wide round of
agent-discoverability work, TUI layout polish, scroll/refresh
robustness, registry CLI, version subcommand, scan validation, CI,
and a tightened handoff convention. The remaining v0.3.0 work
(README screenshots) ships in a follow-up; this tag is cut so
`go install ...@latest` resolves to something current.

### Added

- **`wyk --version` / `-v` / `version`** subcommand. Reads
  `runtime/debug.ReadBuildInfo`, so `go install ...@v0.3.0-alpha`
  prints `wyk v0.3.0-alpha` and source-tree builds print
  `wyk (devel) (commit <sha>)`. No hand-maintained const to drift.
- **`wyk registry list / remove / prune`** for managing
  `~/.config/wyk/repos.json` without hand-editing. `prune` confirms
  with `[y/N]` (skip via `-y`) and removes entries whose path or
  `.git` is gone — surfaced as a gap during the v0.2.3 audit.
- **`wyk conventions`** subcommand prints the agent-ready label
  contract; `-json` emits a stable schema (labels, queries,
  preferred_command, bd_create_example, runbook_sections,
  contract_url). Agents can dump this at session start to learn
  the contract without reading source.
- **`wyk doctor` Conventions stanza** — always-PASS line documenting
  the `human` / `src:agent` labels and the agent inbox query, with
  a pointer at `wyk conventions` for the full text.
- **`wyk init` writes the convention into `bd remember`** with key
  `wyk-handoff-convention`, so `bd prime` surfaces it on every
  agent session start in repos wyk init has touched. Idempotent;
  best-effort (failure WARNs but doesn't gate the hook install).
- **`wyk init -scan` probes bd** (`bd query status!=closed` under a
  2s timeout) before registering each candidate; jsonl-only
  exports, abandoned shells, and other duds are skipped with a
  stderr line. Bails with exit 1 once if `bd` is missing from PATH
  (so a fresh machine without bd doesn't silently no-op).
- **Top-level `wyk --help`** lists every subcommand. Pre-fix only
  the bare top-level flags (-C, -me, -probe) were visible; the
  recommended path for filing a human task (`wyk handoff`) was
  invisible.
- **TUI `wyk` column** (header was "W" briefly) — green `✓` for
  repos with wyk's post-commit hook installed, blank for bd-only.
  Detection routes through `git rev-parse --git-path` so
  worktrees, submodules, and gitlink subdirs resolve correctly.
- **TUI HUMAN column** — the `← HUMAN` / `· HUMAN` badge now rides
  in its own column second-from-left (between cursor and `wyk`),
  not appended to the title. The most important "needs your
  attention" signal is now where the eye lands first instead of
  clipped at the rightmost edge.
- **TUI per-sub Fetch error banner** — `MultiBDSource` was
  swallowing per-sub errors as long as one repo returned data,
  hiding broken workspaces. A new `MultiSource.FetchWithSubErrors`
  returns issues + per-sub errors atomically; an amber banner
  surfaces failures (e.g. `1 repo failed to load: domo-mcp (press
  r to retry; wyk doctor for details)`).
- **TUI sticky-header viewport** — rows are now windowed around
  the cursor instead of dumped as a flat list. The column header
  stays visible regardless of how cramped the terminal gets, with
  `↑ N more above` / `↓ N more below` hints when content is clipped.
- **Handoff runbook structure (REQUIRED)** — every handoff
  description carries three sections:
  - `## Why this needs you (please confirm this is accurate)` —
    agent's self-verification with three concrete attempts,
    boundary hit, why no workaround.
  - `## Steps` — numbered with verification + close.
  - `## What unblocks me when this returns` — the concrete
    artifact the agent expects on bounce-back.
  Surfaced through the skill file, `wyk conventions`, the bd
  remember memory, and `docs/CONTRACT.md`.
- **CI** via `.github/workflows/test.yml` — `go vet`, `go build`,
  `go test -race` on PR and main; README badge.

### Changed

- **TUI no-blank-on-refresh** — transient fetch errors and
  in-flight refreshes no longer take over the canvas. A flaky bd
  query during an auto-refresh tick surfaces as a small banner
  above the status bar; the rows stay put. Terminal errors
  (`bd not installed`) append `— press r to retry` to the banner
  since auto-refresh suspends in that state.
- **TUI title truncation** — long titles get `…` and the full
  text lives behind `enter`. Pre-fix every issue title spilled
  past the right edge.
- **TUI `trunc` is rune-aware** — multi-byte content (issue
  titles, repo names with diacritics) can no longer be split
  mid-codepoint.
- **`wyk doctor` resolves the hook via `git rev-parse`** instead
  of a literal `<r.Path>/.git/hooks/post-commit` read. Gitlink
  subdirs (worktrees, submodules) no longer false-FAIL.
- **Cross-workspace leak guard** — multi-repo Fetch now validates
  every issue ID against the registered sub name using longest-
  prefix-match. bd-daemon-state leaks (broken workspace returning
  another workspace's data) get dropped + surfaced as a fetch
  error instead of silently misrendering.
- **CLAUDE.md actually populated** — Build & Test, Architecture,
  and Conventions sections now carry real content.

### Fixed

- Multi-byte names couldn't be split mid-codepoint in `trunc`
  (was byte-cap, now rune-aware).
- Status banner appearance shrank `bodyHeight()` without re-
  clamping `scroll`, briefly hiding the cursor row.
- Modal-entry handlers (modeFilter, modeNote, modeQuickAdd,
  modeConfirmClose) didn't re-clamp scroll either.
- Window resize didn't re-clamp scroll.
- `wyk init -scan` registered any `.beads/` subdir without
  validating bd could read it.
- `wyk init` didn't announce `bd remember` step under `-dry-run`.
- Conventions prose vs structured form could silently drift on
  the inbox query — extracted to a shared const.
- `human_tasks` query in the structured form lacked `status!=closed`
  (now matches `agent_inbox`'s scoping).
- Registry `prune` removed by Name (non-unique) — could drop the
  alive entry when two repos shared a basename. Now removes by
  Path (unique).
- `findDeadEntries` treated non-IsNotExist Stat errors as alive
  and crashed the `.git` check. Now any Stat error → dead.

### Migration

The handoff runbook structure is new but not enforced
mechanically — existing handoffs continue to work. New handoffs
written via the skill follow the structure; agents reading
`wyk conventions` see it; `bd prime` carries it into new sessions
via the `bd remember` memory.

## [0.2.3] — 2026-05-28

Phase 10 (TUI completeness, round 2). Two visibility bugs surfaced
during a registry audit: a wyk-installed indicator that read silent
on gitlink subdirs, and per-sub Fetch failures that disappeared into
the void. Plus a sibling fix in `wyk doctor` for the same gitlink
case.

### Added

- **W column** in the multi-repo TUI table. A green `✓` next to repos
  with wyk's post-commit hook installed (plain or chained); blank for
  repos that are bd-only. Tells you at a glance which rows will
  auto-close from `Closes:` trailers and which won't. Detection
  routes through `git rev-parse --git-path hooks/post-commit` so
  worktrees, submodules, and any layout where `.git` is a file
  resolve to the same hook the installer wrote.
- **Per-sub Fetch error banner** above the help bar in multi-repo
  mode: "1 repo failed to load: domo-mcp (press r to retry; wyk
  doctor for details)". `MultiBDSource` was previously dropping
  errored subs on the floor as long as one repo returned data —
  invisible failures. A new `MultiSource` interface exposes the
  per-sub error snapshot to the model.

### Fixed

- **`wyk doctor` falsely FAILed on gitlink registrations**. Reading
  `<r.Path>/.git/hooks/post-commit` directly errored with "not a
  directory" when `.git` was a *file* pointing into a parent repo's
  git dir — the layout `git worktree add` and submodules produce.
  Doctor now resolves via `git rev-parse --git-path`, same as the
  installer.
  Closes: would-you-kindly-2m9.
- **TUI per-sub failures are no longer silent**. The bug that hid
  `domo-mcp`'s broken `.beads/` state from the user — now reads as
  an amber banner pointing at `wyk doctor` for the details.
  Closes: would-you-kindly-m99.

## [0.2.2] — 2026-05-28

Phase 9 (TUI completeness) plus a visual polish round. Closes the
most common reasons a user would drop out of the TUI to a shell.

### Added

- **`N` quick-add**: file a new issue from the TUI without dropping
  to `bd create`. Capital `N` opens a title-only prompt; on enter
  it runs in the cursor's current repo (so a multi-repo view files
  in the right workspace) and labels the new issue `src:human`.
  The list refetches so the row appears immediately.
- **Notes in the detail view**: bd accumulates ad-hoc context via
  `bd note` (or the TUI's `n`), but the previous detail view only
  showed the description. A new `Detailer` interface lets the model
  lazy-fetch the full record (via `bd show`) on enter; notes appear
  a beat after the rest of the detail view loads. `beads.Issue`
  gains a `Notes` field; `beads.Client.Show` returns a fully-
  populated `Issue` (description AND notes — list/query drop one
  or the other for efficiency).

### Changed

- **Table cell colors flattened to the terminal default**: Repo,
  Branch, ID, T, P, Updated, and the header row now use the
  terminal's default foreground (typically white on dark themes)
  instead of the previous dim grey. Reads at the same brightness
  as the Title — matches the roborev reference and feels less
  hierarchical, more like a structured report. Status colors and
  HUMAN badges are unchanged.

## [0.2.1] — 2026-05-28

Onboarding fixes after the first v0.2.0 user hit the registry-empty
cliff. All three changes target "I installed wyk, why does it only
show this one project?".

### Added

- **`wyk init -scan <root>`** — bulk-discover bd workspaces under a
  directory tree and register every one found. Skips hidden dirs,
  `node_modules`, `vendor`. Idempotent (already-registered paths
  are silently skipped). The fastest path from "fresh install" to
  "multi-repo view across every project I have."
- **Empty-registry banner in the TUI**: when no repos are
  registered, wyk surfaces a warm-orange italic hint above the
  table telling the user how to set up — `wyk init` here or
  `wyk init -scan ~/Projects` to bulk-register. No more silent
  cwd-only fallback.

### Changed

- **`BDSource` always decorates issues with `Repo`/`Branch`** when
  given a `Name`. `buildSource` populates Name on every path
  (-C / single registered / empty-registry cwd-fallback), so the
  TUI's Repo and Branch columns are visible even in single-repo
  mode. Matches the always-on density the v0.2.0 user asked for.

## [0.2.0] — 2026-05-28

Multi-repo support, the agent-side handoff loop, hook composability,
TUI polish, a diagnostic subcommand, and observability — all the
post-v0.1.0 work consolidated. Renamed the project (and the Go
module path, bd issue prefix, and GitHub repo) from `would-you-kindly`
to `would-you-kindly` mid-cycle.

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

[Unreleased]: https://github.com/jimbottle/would-you-kindly/compare/v0.3.3...HEAD
[0.3.3]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.3.3
[0.3.2]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.3.2
[0.3.1]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.3.1
[0.3.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.3.0
[0.3.0-alpha]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.3.0-alpha
[0.2.3]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.2.3
[0.2.2]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.2.2
[0.2.1]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.2.1
[0.2.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.2.0
[0.1.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.1.0
