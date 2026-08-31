# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Split layout: the list and the selected issue's runbook side by
  side** (would-you-kindly-g1ud). On a terminal of at least 140×36 the
  list takes a left pane and the cursor row's detail — owner badge,
  title, meta, labels, description, notes, dependency links — renders
  in a right pane that follows the cursor. The slim row paints
  immediately; the `bd show` enrichment and dep lookups are debounced
  (75ms) so holding `j` doesn't fan out a shell-out per row. `⏎`
  focuses the pane (every detail key works there), `esc` returns to
  the list, `p` hides/shows the pane (forcing it on down to 100×20;
  the choice persists in `state.json`), and while the list has focus
  the wheel scrolls whichever pane the pointer is over (focusing the
  pane releases mouse capture for text selection, as the full-screen
  detail always has). Below the breakpoint the stacked
  layout is unchanged. Modelled on roborev's queue + review split —
  the human's job in wyk is reading runbooks, and this removes the
  `⏎`/`esc` round-trip per row.

### Fixed

- **Bulk close is one bd call per workspace instead of one per row**
  (would-you-kindly-cexj). Marking N rows and pressing `a`, `y` used
  to shell `bd close` N times sequentially, each paying Dolt's per-call
  latency — the user-reported "closing out multiple tasks at once is
  very slow". `bd close` accepts a list, so the Mutator grew an
  optional `BulkCloser` (`CloseMany`) that `BDSource` satisfies with a
  single invocation and `MultiBDSource` satisfies by grouping the
  selection per repo. Per-row error attribution and the mark-restore
  on failure are unchanged.

- **A hand-edited or symlinked `repos.json` entry no longer gets
  auto-registered a second time** (would-you-kindly-9vlt). `Has`/`Add`/
  `Remove` normalised the incoming path but compared it to the stored
  string verbatim, so an entry written as `/tmp/x` (→ `/private/tmp/x`
  on macOS) was invisible to the auto-register in `wyk handoff` /
  `wyk create`, which then added the same workspace under its folder
  name — and every row rendered twice. Stored paths are now normalised
  on the comparison side too.

- **The warm-start cache is scoped to the workspaces wyk was launched
  against** (would-you-kindly-mup1). The last-fetch snapshot was keyed
  to nothing, so `wyk -C <other-repo>`, a registry edit, or a run under
  a throwaway `XDG_CONFIG_HOME` painted the *previous* source's rows
  until the live fetch replaced them — the README screenshot regen
  captured a private client backlog exactly that way. Snapshots now
  carry a fingerprint of the repo paths and are ignored on mismatch;
  the demo renderer isolates `XDG_CACHE_HOME`/`XDG_STATE_HOME` too.

- **A workspace whose bd issue prefix differs from its directory name
  no longer loses every row and reports as a failed repo**
  (would-you-kindly-qp14). The cross-workspace leak guard required each
  issue ID to start with the *registry* name — which comes from the
  folder — but bd picks its own prefix at `bd init`. A repo registered
  as `louisville-open-data-expenditure-bot` whose issues are all
  `louisville-open-data-*` therefore had all of them dropped and
  surfaced "bd may be serving the wrong workspace", while `wyk doctor`
  reported it perfectly healthy. The guard now drops only rows that a
  *different registered workspace* claims — the actual contamination
  signal — so an unclaimed prefix is understood as the workspace's own.

- **The HUMAN-BLOCK dep lookups are batched into at most two bd calls
  per workspace** (would-you-kindly-3frr), instead of one `bd dep list`
  per candidate row. The fan-out competed with the per-repo list
  fetches for the same bd/Dolt capacity, and the resulting `bd dep
  list … timed out after 10s` entries fill `bd-errors.log`. bd 1.0.4
  tags each batch `dep list` record with its `issue_id`, so the
  attribution that once forced per-issue calls is now available in one
  — the client comment saying otherwise was stale. Measured across a
  24-workspace registry: dep-related subprocesses 42 → 21, total 66 →
  45, with the badge count unchanged.

### Changed

- **The ID column shows the FULL bd issue ID** (would-you-kindly-rvv9).
  It used to strip the repeated workspace prefix (`2oa` for
  `would-you-kindly-2oa`) and cap the column at 16 cells, which
  optimized the wrong thing: the column's job is matching a row against
  an ID someone quoted, and an agent says `would-you-kindly-l51f`,
  never `l51f` — while a longer ID like `workspace-custom-palette-3f2`
  came out as `workspace-cust…`, forcing you to open rows one by one to
  find the issue the agent meant. The column now sizes itself to the
  widest ID in view, capped at a third of the terminal width (never
  below the old 16, never above 40). A `Repo` column that merely
  repeats the ID's own prefix is now the first column dropped when the
  terminal is too narrow, so the wider IDs don't come out of the title.

### Fixed

- **Closing a task now gives immediate, row-specific feedback and
  blocks a second close until it lands** (would-you-kindly-khtw). A bd
  close takes seconds on a cold multi-repo workspace, and nothing on
  screen changed during that window — the row sat there unmarked, so a
  user who assumed the keypress hadn't registered pressed again, by
  which point the cursor had moved, and closed the wrong task. The
  target row now reads `closing` in the Status column the instant the
  write is dispatched and the banner names it; any further close (the
  `a` prompt, a bulk close, or `.` repeat) is refused with "still
  closing `<id>` — wait for it to clear" until the write lands. The
  block lifts on both outcomes: success drops the row from the list,
  and a failure leaves it retryable with its error banner.

- **`wyk handoff` no longer hangs forever under agent/CI harnesses**
  (would-you-kindly-l51f, from an external bug report). The stdin
  guard tested `os.ModeCharDevice` — which is not a terminal test: it
  is true for `/dev/null` (safe: immediate EOF) and false for pipes
  and sockets (which block). So `</dev/null` was falsely rejected as
  "stdin is a TTY" while an inherited harness socket sailed into an
  unbounded `io.ReadAll` — an unkillable (SIGALRM-immune) silent hang
  that also meant the `human` label was never applied. The guard now
  uses a real terminal check (`golang.org/x/term`), and every
  non-terminal stdin read is deadline-bounded (30s default,
  `WYK_STDIN_TIMEOUT` overrides — a Go duration or bare seconds) with
  a clear timeout error instead of a wedged process. `</dev/null` now
  reads as an empty runbook and gets the accurate "empty runbook…
  pass -allow-empty" diagnostic.

- **Bulk reads no longer silently truncate at bd's default result
  limit** (would-you-kindly-ec7z). bd 1.0.4 caps `query`/`list` at 50
  rows and `ready` at 100 by default, and `--json` honors the cap —
  so on any workspace past the cap the TUI, `wyk inbox`, `wyk stats`,
  and friends quietly saw a subset. Every bulk read now passes
  `--limit=0`.

### Changed

- **The agent inbox additionally excludes `status=deferred` and
  `status=hooked`** (would-you-kindly-hxd8; wyk-contract/v3 amended
  2026-08-19). bd 1.0.4's `deferred` (on ice until a date/subsystem)
  and `hooked` (attached to another agent's hook) carry the same
  "the blocker is tracked elsewhere" character as the existing
  `blocked` exclusion — presenting them as work-now was wrong, and
  for `hooked` risked two agents colliding on one issue. Items
  reappear the moment their status returns to open. `wyk stats`'
  inbox count mirrors the new query.

- **Status vocabulary aligned with bd 1.0.4's seven built-in
  statuses** (would-you-kindly-sbn9). wyk knew five; `pinned`
  (persistent, never a queue entry) and `hooked` now appear in the
  TUI's status legend (`hooked` shares the in-progress emphasis;
  frozen statuses stay dim), in `wyk stats`' by-status breakdown,
  and in `wyk conventions` / docs/CONTRACT.md's lifecycle table.
  CONTRACT.md also documents interop with bd's native `bd human`
  command family (`bd human respond` closes — it is NOT the
  bounce-back gesture), and the `--add-label` warning is now
  correctly scoped to `bd create` (bd 1.0.4's `bd update` does
  accept `--add-label`/`--remove-label`).

### Added

- **`wyk registry add [path]`** — register a bd workspace from the
  command line. Registration previously happened only as a side effect
  of `wyk init`, so hand-editing `repos.json` was the only headless
  option. The path may be the workspace root or any directory inside
  it; the nearest ancestor holding `.beads/` is what gets registered.

- **`wyk init -skip-hook`** — run the bootstrap without touching git
  hooks at all. Previously the only paths that reached registration
  from a repo with a foreign hook were `-chain` and `-force`, both of
  which mutate hooks; there was no way to register a repo while leaving
  its hooks alone. Mutually exclusive with `-chain` / `-force`.

### Changed

- **`wyk init` registers the repo BEFORE installing the post-commit
  hook**, and a hook it declines to write is now a warning rather than
  an abort (would-you-kindly-7kly). Declining a foreign hook exits 0
  with a `WARNING:` block naming what didn't happen; so does an
  out-of-repo `core.hooksPath`, which used to exit 64. Only an explicit
  `-chain` that cannot proceed (the `.pre-wyk` slot is occupied) still
  exits 64 — and by then the repo is already registered.

- **The `O` prompt is now labelled "assignee", not "owner"** — it writes
  bd's `assignee` field, while "owner" in the TUI means the Owner
  column's HUMAN / AGENT badge (a different thing entirely, driven by
  labels). The prompt, its bulk form, the cancel banner, and the keymap
  help all say assignee now.

### Fixed

- **`wyk init` aborted before registering when a foreign post-commit
  hook existed** (would-you-kindly-7kly), so the repo stayed invisible
  to `wyk inbox`, `wyk dashboard`, `wyk stats`, `wyk activity`,
  `wyk export`, `wyk depgraph` and the TUI. Because the *cwd-scoped*
  commands (`wyk handoff`, `wyk create`, `bd`) work fine in an
  unregistered repo, the failure was silent and asymmetric: an agent
  filed handoffs, saw `handed <id> to human`, and the human structurally
  could not receive them — one reporter lost a P1 outage this way for
  several days. It hits beads repos specifically, since `bd init` points
  `core.hooksPath` at `.beads/hooks`, making any other commit-hook
  tool's hook there "foreign" to wyk. `wyk init -dry-run` compounded it
  by promising registration the real run never reached.

- **`wyk doctor`'s `core.hooksPath redirect` warning suggested a command
  that could not work** (would-you-kindly-ghng). It recommended a bare
  `(cd … && wyk init)`, which is precisely what declines when the active
  hooks dir already holds another tool's hook; the advice now names
  `-chain` / `-force` in that case. Its `unset core.hooksPath`
  alternative now carries the caveat that clearing it disables every
  hook in that dir — including bd's own.

- **Low-severity drift batch from the public-release review**
  (would-you-kindly-6gjb). User-visible pieces: `wyk init -uninstall
  -skills` (and the `-scan` / `-fix-foreign-hooks` modes) no longer
  silently ignore `-skills`; `-h` exits 0 rather than 64 on every
  subcommand, matching the documented convention; the bulk type-change
  banner reads "retyped N rows" instead of "type N rows"; the text
  cursor blinks in every prompt, not just the filter one; `:filter list`
  and `:filter remove` are advertised in the palette placeholder and the
  unknown-command hint; every subcommand rejects stray positional
  arguments; and `wyk init -scan` reports the paths it couldn't read
  instead of returning "0 repos found" as if the tree were clean.
  Internally: the per-repo init flag list, the CLI usage strings, and the
  set of textinput-driven TUI modes each collapsed from several
  hand-copied copies to one source, and three new guards pin them —
  cliSubcommandDocs against the real FlagSets, `bulkVerbs` against the
  real bulk actions, and status writes through `setStatus` so a stale
  flash-clear can't wipe a fresh banner.

- **An unregistered repo no longer silently swallows handoffs.** Every
  multi-repo view (`wyk inbox`, `wyk dashboard`, `wyk stats`, the TUI)
  reads `~/.config/wyk/repos.json`, so a bd workspace missing from it
  was omitted from all of them with no warning — an agent could follow
  the handoff convention exactly, with correct labels, and the work
  still reached nobody. This lost a P0 in the field. Four changes close
  it: `wyk create` and `wyk handoff` now register the workspace they
  write to (printing a one-line notice; `-dry-run` still writes
  nothing); `-C` registers the workspace it resolves, since it has just
  proved the repo is readable; `wyk doctor` FAILs when the working
  directory is an unregistered bd workspace, and `wyk doctor -fix`
  registers it — including when the registry is otherwise empty, the
  state a headless agent-driven workspace is most likely to be in; and
  `wyk registry add` provides the explicit form. Registration is
  best-effort and never changes a command's exit code: the issue being
  filed matters more than the bookkeeping around it.

- **`wyk create` and bare-id `wyk handoff` now stamp `src:` provenance
  labels** — the two paths previously set no `src:` label (only
  `session:<id>` / only `human`), violating the contract that every
  wyk-filed issue carries provenance. The consequence was silent: an
  agent-filed task, once a human bounced it back, could never match the
  `wyk inbox` query (`label=src:agent AND NOT label=human …`), so returned
  work was invisible. Now `wyk create` stamps `src:agent` in a Claude
  session (else `src:human`), and `handoff.BounceToHuman` adds the
  collective `src:agent` alongside `human`. Two guards keep the class of
  bug from recurring: a writer-vs-reader contract test (every filing
  path's labels must satisfy the inbox query after a simulated
  bounce-back) and a `wyk doctor` check that flags any wyk-filed
  (`session:`-labeled) or human-flagged issue missing a `src:` label, with
  the backfill command.

- **Click-drag text selection works in the detail view again** — the
  mouse-navigation feature captured the mouse globally, so selecting
  text in an issue's instructions (the reading view's primary mouse
  use) silently stopped working. Capture now follows the view: the
  list and `:bd` output stay captured for wheel/click navigation; the
  detail view — including prompts overlaid on it — releases the mouse
  automatically and re-captures on the way out. The `m` master toggle
  still works everywhere. Mouse-mode switching now goes through
  direct program calls instead of tea commands, whose batched
  delivery was observed to drop the terminal write.


## [0.7.0] — 2026-06-10

### Added

- **Preset switching is instant** — pressing `h` / `tab` (or any preset
  change) used to fire a fresh multi-repo `bd` query and leave the
  previous preset's rows on screen for the 2-3s a cold round-trip
  takes. Each preset's last result is now cached and painted
  immediately on switch, with the authoritative fetch reconciling in
  the background (cache cleared when `C` toggles the closed-scope).

- **`h` toggles the human view** — pressing `h` while already in the
  human view returns to the preset you came from (default `all`)
  instead of being a one-way trip.

- **Every subcommand's `-h` now leads with a synopsis and common-case
  examples** — the `init -h` treatment, everywhere: `wyk <name> — 
  <summary>`, the canonical usage, then 1-3 copy-pasteable examples
  before the flag table, instead of Go's bare flag dump. Content comes
  from the same source as the generated CLI reference (which now also
  shows the examples), so `-h` and the published docs can't drift on
  the synopsis. `wyk handoff -h` finally advertises `-template`. The
  README's doctor section is retitled "Troubleshooting" so the heading
  matches what a stuck user searches for.

- **Mouse navigation, with an `m` toggle** — the wheel moves the list
  cursor (and scrolls the detail/output views) and a left-click lands
  the cursor on the clicked row. Mouse capture had been dropped
  entirely so click-drag text selection would work; the toggle now
  serves both: press `m` to release the mouse for bare click-drag
  selection (most terminals also pass shift/option-drag through while
  captured), press it again to re-capture. The preference persists in
  `state.json`; existing files default to captured.

- **`wyk doctor` bd version-compatibility check** — a new check
  alongside "bd binary on PATH" that parses `bd --version` and verifies
  it's within the range wyk is known to work against. FAILs only on a bd
  older than the supported minimum (genuinely incompatible), WARNs on an
  unparseable version or a newer-than-tested major, and PASSes in range.
  Since wyk shells out to bd for every read and write, this turns a
  silent version skew into an explicit, actionable signal.
- **`--no-color` flag** — a top-level flag that disables colored output,
  equivalent to the existing `NO_COLOR` / `WYK_NO_COLOR` environment
  variables. Closes the conventional-CLI gap of honoring the env var but
  not the flag.

- **README `Platforms` + `What wyk is not` sections** — explicit
  tested-platform statement (macOS + Linux; Windows unsupported/untested)
  and a short non-goals list, so adopters know the scope and support
  surface up front.
- **Prebuilt binaries + Homebrew tap (goreleaser)** — tagged releases now
  attach prebuilt `linux`/`darwin` (`amd64`/`arm64`) archives + checksums
  and update the Homebrew tap, so `brew install jimbottle/wyk/wyk` and a
  direct download from the Releases page join `go install` as supported
  install paths. `wyk --version` carries the real tag on these builds via
  an `-ldflags -X` stamp (goreleaser's clean `go build` would otherwise
  report `(devel)`). go-install remains the documented default — this is
  purely additive.

- **`bd-create-guard` PreToolUse hook** — `wyk init` now registers a
  Claude Code hook in `.claude/settings.json` that blocks an agent's raw
  `bd create` and tells it to use `wyk create` instead (same flags, plus
  the session stamp). This enforces the "use wyk create" convention at
  the harness level so the TUI's Session column actually fills, rather
  than relying on an agent following the docs. Bypass a one-off with
  `WYK_ALLOW_BD_CREATE=1`; opt out at init with `-skip-claude-md`.

- **`AGENT-HANDOFF` owner state** — a fourth owner-column badge, driven
  by the `agent-handoff` label, for when one agent must not interfere
  with a task another agent is working. A human is expected to
  orchestrate the coordination; the badge just calls the situation out
  distinctly. These rows are excluded from the agent inbox query so the
  "work it now" imperative never fires on a task that isn't the current
  agent's to touch. Themeable via `agent_handoff_bg` / `agent_handoff_fg`.
  Bumps the handoff contract to `wyk-contract/v2`.

- **`wyk create`** — a thin wrapper over `bd create` (every flag
  forwarded verbatim) that stamps each new issue with a `session:<id>`
  label recording the Claude session that filed it (from
  `$CLAUDE_CODE_SESSION_ID`). Use it instead of `bd create` so work can
  be traced back to a conversation; plain `bd create` still works but
  won't record the session.

- **TUI Session column** — shows the short Claude session ID that
  created each issue (populated by `wyk create`). Toggleable via the `o`
  overlay and auto-hidden first on narrow terminals; blank for issues
  filed without `wyk create`.

- **`wyk handoff -create` also stamps the Claude session** — so
  handoff-filed issues populate the TUI's Session column too, not only
  issues filed with `wyk create`. (Raw `bd create` still records nothing —
  wyk can't intercept it.)

- **`wyk init` now seeds a wyk conventions block into `CLAUDE.md`** — so
  a freshly-init'd repo is wyk-aware, not just bd-aware. Without it, an
  agent told to "build the plan in wyk" had no local definition mapping
  that to filing issues, and silently fell back to markdown/TodoWrite.
  The block covers planning-as-bd-issues (via `wyk create`), the owner
  column, handoff, and an explicit "address problems, don't shrug them
  off" rule. Created if absent, refreshed in place if present
  (version-agnostic marker), opt out with `-skip-claude-md`.

### Changed

- **`go.mod` go directive relaxed from `go 1.26.3` to `go 1.26`** — the
  patch-level pin forced a toolchain download (or an outright build
  failure under `GOTOOLCHAIN=local`) for anyone on go 1.26.0–1.26.2,
  which is stricter than intended and contradicted the README/CONTRIBUTING
  "Go 1.26+". Nothing in the tree requires a 1.26.3-specific fix, so the
  directive now states the true minimum.

- **CI now also builds + race-tests on macOS** — the test workflow runs
  the build and `go test -race` on both `ubuntu-latest` and `macos-latest`
  (the TUI's instant-refresh path uses platform-specific `fsnotify`, and
  the tool is developed on macOS). The static gates (gofmt, docs-drift,
  golangci-lint, govulncheck) stay Linux-only to avoid redundant macOS
  minutes.

- **The status-bar footer wraps into a column-aligned key grid** — the
  status info sits on its own line and the key bindings flow into a
  roborev-style grid below it (the most columns that fit, padded so the
  bar separators line up between rows). Every binding stays visible and
  no line exceeds the terminal width — including on terminals that render
  ambiguous-width glyphs as two cells, which is what made the old
  one-line footer run off the right edge.

- **Leaner always-loaded agent context** — the `bd remember` convention
  memory (surfaced by `bd prime` every session) and the seeded CLAUDE.md
  wyk block are each ~halved in size, keeping every load-bearing rule
  (owner states, inbox = work-them, the HUMAN-BLOCK/AGENT-HANDOFF skips,
  handoff via `wyk handoff`, status lifecycle) and deferring the full
  runbook format to `wyk conventions`. Re-run `wyk init` to refresh a
  repo's copy.

- **Content-aware column widths + dimmed headers** — table columns now
  size to the wider of their header and the widest value on screen
  (clamped to sane maxima), so e.g. the Owner column shrinks to fit when
  no long badge is present instead of always reserving room for
  `AGENT-HANDOFF`, and a `main`-only Branch column collapses to its
  header. The priority value is padded to its column so it lines up under
  the `Priority` header, and column headers render in a dim grey so the
  row content reads as primary.

- **Detail-view "copy instructions" + bar-delimited footers** — the
  detail view's body-copy key is now `c` ("copy instructions"; `y`
  stays a silent alias), and the list help bar and detail footers use a
  vertical-bar (`▕`) separator between options instead of bullets/spaces.

- **TUI column headers are full, capitalized field names** — the owner
  column header is `Owner` (was lowercase `owner`), the type column is
  `Type` (was `T`), and the priority column is `Priority` (was `P`). The
  README's "A day in the life" section now defines every column.

- **The TUI title column is capped at 50 visual columns** — long titles
  no longer drag across the full width of a wide terminal into an
  unreadable wall of text. The column still shrinks below the cap to fit
  a narrow pane; the detail view (enter) shows the full title.

- **`wyk doctor` CLAUDE.md wyk-awareness check** — flags any registered
  repo whose `CLAUDE.md` lacks the wyk conventions block (WARN), with a
  re-run-init nudge. Surfaces repos init'd before the seed existed.

### Fixed

- **The agent inbox no longer surfaces blocked tasks** — `wyk inbox`'s
  query excluded closed but not `status=blocked`, so a task whose
  tracked blocker was still outstanding presented as "work this now"
  and sent the agent chasing an unblocker that hadn't arrived (found
  live: a blocked task sat in the inbox for a full session). The
  query — and `wyk stats`' inbox count, the conventions output, the
  seeded agent memory, and the contract doc, which had also drifted —
  now exclude `blocked`, mirroring how `bd ready` treats it.

- **Warm-start now seeds across presets** — the on-disk last-fetch
  snapshot was silently skipped whenever the saved preset differed
  from the restored one (quit on `all`, reopen on `human`), leaving a
  `loading…` spinner for the entire multi-repo cold start (~9s). An
  `all` snapshot now seeds the human/mine/blocked views through an
  in-memory filter (closed rows excluded — the snapshot may have been
  saved while `C` was toggled on); `ready` still cold-starts (its
  blocker semantics are bd's to compute).

- **An unknown subcommand no longer silently launches the TUI** — `wyk
  inbx` (a typo) or `wyk -C dir handoff` (top-level flags before the
  subcommand) used to fall through the dispatcher and open the
  full-screen TUI, hiding the mistake. Both now fail with exit 64: a
  typo gets `unknown subcommand "inbx" — did you mean "inbox"?`, and a
  known subcommand preceded by flags is told the subcommand must come
  first (each owns its own flags).
- **`wyk handoff -create` validates `-priority` / `-type` at parse
  time** — `-priority banana` used to sail through `-dry-run` ("would
  create: … priority=banana") and only fail when bd rejected the real
  write, defeating dry-run's purpose. Both flags are now checked
  against bd's accepted values (0-4 / P0-P4; the nine issue types)
  with a usage error naming the valid set.

- **`wyk import` rejects dumps it can't trust and always reports
  totals** — valid JSON without a `schema_version` (wrong file,
  hand-rolled blob) or with a newer schema than this wyk reads used to
  pass silently with exit 0; both now fail loudly. The plan summary
  always leads with "N issue(s) across M repo(s) in the dump", so an
  empty dump reads as the warning it is rather than silent success.

- **Go standard-library vulnerabilities `GO-2026-5039` (`net/textproto`)
  and `GO-2026-5037` (`crypto/x509`)** — CI's `govulncheck` step was
  failing on these (fixed upstream in go1.26.4). CI installs Go via
  `go-version-file: go.mod`, and with the `go 1.26` directive (see
  _Changed_) it pulls the latest patched 1.26.x, which carries both
  fixes — so the build is green again without pinning users to a
  specific patch.

- **`wyk --probe` surfaces partial multi-repo failures on stderr** — in
  multi-repo mode probe now uses `MultiSource.FetchWithSubErrors` (rather
  than the plain `Fetch`, which discarded per-sub errors), so a repo
  that's down is named on stderr instead of silently dropping out. A
  short or empty human-flagged list is no longer mistaken for "no work"
  when the real cause is "a repo failed." The issue list still goes to
  stdout unchanged; the warning is stderr-only, matching the partial-
  failure handling already in `wyk inbox` and `wyk stats`.
- **`wyk --help` no longer uses Unicode dashes in flag hints** — two
  subcommand hints rendered `(−json …)` / `(–json …)` with a minus sign /
  en dash instead of an ASCII `-`, so copy-pasting them produced `flag
  provided but not defined`. Guarded by a test.

- **A bad `-C <dir>` now gives a clean error** — `wyk -C /no/such/dir`
  reported bd's raw JSON error blob; it now validates the directory up
  front and prints `-C directory "…" does not exist` (or `… is not a
  directory`).

- **`wyk completion -h|--help` prints usage and exits 0** — previously it
  reported `unknown shell "-h"` and exited 64.

- **The `/` filter now matches repo / branch / ID, and no longer floods
  on common words** — it previously searched only the title (fuzzy) and
  description, so filtering by repo name (the most common use) didn't
  work, and the description's fuzzy-subsequence matched a scattered
  `a·n·d·r·o·i·d` in almost any long body. The filter now matches the
  **repo, branch, ID, and description as case-insensitive substrings**,
  keeping fuzzy subsequence on the (short) title so `rpw` → "rotate
  password" still works. Title matches rank above the rest. The match is also **highlighted** in the Repo, Branch, and ID columns, not just the Title.

- **`wyk init` installed the auto-close hook into a bypassed path on a
  fresh repo** — the hook path was resolved before `bd init` ran, so it
  predated bd pointing `core.hooksPath` at `.beads/hooks`. wyk then wrote
  to `.git/hooks/post-commit` while git read from `.beads/hooks`, and
  `Closes:`/`Fixes:` auto-close silently never fired. The hook path is now
  re-resolved after `bd init`, so it lands where git actually looks.

## [0.6.1] — 2026-06-02

### Changed

- **A null owner now badges AGENT** — an issue with no `src:`/`human`
  label renders **AGENT** instead of a blank owner column, so the column
  is never empty. The thing worth watching is now the inverse: a task
  that needs a human must be handed off (`wyk handoff`), or it silently
  defaults to AGENT. `wyk doctor`'s old "blank owner badge" check is
  retired accordingly.

## [0.6.0] — 2026-06-02

### Added

- **Graceful narrow-terminal degradation** — the issue list now
  auto-hides lower-value columns as the terminal narrows instead of
  wrapping or truncating mid-row, so wyk stays readable in a split pane.

- **Opt-in priority emphasis** — priorities can be visually emphasized
  in the list for faster triage.

- **`wyk doctor` owner-badge guard** — `doctor` now flags any non-closed
  issue that would render with a **blank owner badge** (no `human` and no
  `src:` label), the convention's "task has no owner" defect that bd
  can't enforce on its own.

### Changed

- **`src:human` issues badge as AGENT** — a human-filed task that isn't
  flagged `human` now renders an **AGENT** badge instead of a blank one:
  a human filing a task is agent-owned work unless explicitly handed off.
  Closes the last gap where an issue could show no owner badge.

- **All on-screen text is natively selectable** — wyk no longer captures
  the mouse, so you can select and copy any text in the TUI with your
  terminal's normal selection.

- **Background-adaptive palette** — colors adapt to light and dark
  terminals, and the over-used amber was de-overloaded for clearer
  status signaling.

### Fixed

- **`core.hooksPath`-aware hooks** — auto-close silently failed when
  git's `core.hooksPath` redirected hooks away from `.git/hooks` (e.g.
  `bd` points it at `.beads/hooks`), so the `Closes:` trailer ran a hook
  that never fired. `wyk init` now installs into the **active** hooks dir
  (following `core.hooksPath`, chaining any existing hook there) so the
  hook git actually runs is wyk's; it refuses with guidance when
  `core.hooksPath` points *outside* the repo (stale config). `wyk doctor`
  gained a `core.hooksPath redirect` check that explains the bypass and
  the right fix, and `wyk uninstall` removes from the active dir too.

### Security

- **Supply-chain hardening in CI** — all GitHub Actions are pinned to
  commit SHAs (on the Node 24 runtime), and CI runs `govulncheck` to
  catch known vulnerabilities in dependencies and the Go stdlib that the
  code actually calls. Releases are now cut automatically from a `vX.Y.Z`
  tag, with notes drawn from this changelog.

## [0.5.0] — 2026-06-01

### Added

- **Claude Code plugin** — the agent skills are now also packaged as a
  Claude Code plugin under [`plugin/`](plugin), installable in one step
  (`/plugin marketplace add jimbottle/would-you-kindly` then `/plugin
  install wyk@wyk`). It bundles the three skills (kept byte-identical to
  the embedded copies via `make plugin-skills` + a drift test) and a
  best-effort SessionStart hook that surfaces `wyk inbox`. The git
  auto-close hook (a git hook, not a Claude hook) stays with `wyk init`,
  and an MCP surface was considered and skipped — the CLI is lean enough
  that agents call it via Bash. See [`docs/SKILLS.md`](docs/SKILLS.md).

- **Skill drift detection (stale vs. modified)** — `wyk skills`
  records a `.wyk-managed` provenance hash next to each installed
  `SKILL.md`. On the next check it tells an *out-of-date* skill (an
  unedited older wyk version) from a *locally modified* one: `wyk
  skills install` now auto-refreshes out-of-date skills **without**
  `-force` (that flag still guards your edits), and `wyk doctor` /
  `wyk skills list` report the two states distinctly. This makes the
  post-`wyk update` "run `wyk skills install`" flow refresh cleanly
  while preserving any skills you've hand-edited.

- **Skills wired into setup/maintenance** — `wyk doctor` now reports
  whether the agent skills are installed and current (and `doctor -fix`
  installs any missing ones); `wyk init -skills` installs them during
  repo setup; and `wyk update` reminds you to `wyk skills install` with
  the new binary since it may ship updated skills.

- **`wyk skills`** — install the agent-facing Claude Code skills wyk
  ships (`wyk` triage/inbox, `wyk-handoff`, `wyk-project-review`) into
  `~/.claude/skills` (default) or `./.claude/skills` (`-project`) so a
  harness loads them on demand. The skills are embedded in the binary
  (so `wyk update` carries new versions) and their thin bodies call the
  wyk/bd CLI rather than restating conventions. `list` shows install
  state, `print <name>` emits one to stdout, `install`/`uninstall` are
  idempotent with `-dry-run`/`-y`; `install -force` overwrites a
  locally-modified copy (left untouched by default). See
  [`docs/SKILLS.md`](docs/SKILLS.md) for the full reference. A drift
  test keeps the embedded skills honest — every `wyk <cmd>` a skill
  names must be a real shipped subcommand, and each skill's frontmatter
  `name:` must match its directory.

- **`-compact` on every agent `-json` surface** — `wyk inbox`,
  `stats`, `dashboard`, `activity`, and `depgraph` join `export` in
  accepting `-compact`, which emits non-indented JSON (indentation is
  pure overhead for an LLM). One shared encoder (`emitJSON`) now backs
  every `-json` path. Default stays pretty; pass `-json -compact` (and
  `-slim`) for the leanest agent output.

- **`-slim` for `wyk export` and `wyk inbox`** — drops the heavy
  `description`/`notes` bodies (and timestamps/`created_by`) from each
  issue, keeping the lightweight metadata an LLM needs to scan a
  backlog (`id`/`title`/`status`/`priority`/`labels`). ~72% smaller on
  a body-heavy export. The full shape is unchanged without the flag.
  Part of the agent-token-efficiency epic.

- **Drill into dependencies from the detail view** — the
  `Dependencies`/`Dependents` rows are now selectable: `Tab`
  (`Shift-Tab`, or `n`/`p`) cycles a highlight through them, `Enter`
  opens the highlighted issue's detail (following cross-repo edges),
  and `Esc` pops back to the issue you came from. `j/k` still scroll
  the body. The footer advertises the link nav when an issue has any.

### Changed

- **`wyk export` defaults to open issues** — it now emits the
  actionable (open) set by default instead of open+closed; `-closed`
  restores the full history. An agent reading the export wants the
  live backlog, not closed archives. The dump gains a `schema_version`
  (now `2`) so consumers can detect the default; `wyk import` already
  skipped closed-in-dump entries, so round-trips are unaffected. Part
  of the agent-token-efficiency epic.

- **Leaner issue JSON** — the agent-facing `-json` outputs (`export`,
  `inbox`, `activity`) now elide empty/zero fields per issue:
  open issues no longer carry `closed_at:"0001-01-01T00:00:00Z"`, and
  zero `dependency_count`/`dependent_count`/`comment_count`, empty
  `notes`/`description`/`owner`/`created_by`, and empty `labels` are
  dropped. `id`/`title`/`status`/`priority` are always present
  (`priority:0` = P0 is never dropped). Marshaling-only — bd JSON still
  parses unchanged, and export→import round-trips. Part of the
  agent-token-efficiency epic.

- **Agent `-json` commands degrade gracefully on partial multi-repo
  failure** — `wyk inbox`, `stats`, and `activity` no longer drop a
  failed workspace silently (and `inbox`/`stats` no longer emit
  *nothing* when one repo errors). Each now surfaces an `errors:
  [{repo, error}]` array alongside its data so an agent reading stdout
  can tell a result is partial and which workspaces are missing.
  `inbox -json` changes shape from a bare issue array to a
  `{issues, errors}` envelope; `stats -json` gains an `errors` field;
  `activity -json` gains `errors`. Partial failures exit 0 (data was
  returned); total failure exits 1 but still emits a parseable
  payload. (`export` and `dashboard` already carried per-repo errors.)
  Part of the agent-token-efficiency epic.

### Added

- **Dependency context in the detail view** — opening an issue now
  renders collapsing `Dependencies` and `Dependents` sections at the
  bottom of the body (`ID — title (status)`, dimmed). Edges are
  fetched lazily on detail entry (no per-row fan-out) and cached for
  the session, so re-opening the same issue is instant. A failed
  lookup degrades to a single `(… unavailable)` line so the body
  still flows. Pairs with the topological deps-sort: sort by deps,
  drill in, and see why a row landed where it did.
- **Session restore across restarts** — the TUI now remembers the
  active filter preset, sort key, and selected issue between launches
  via `$XDG_CONFIG_HOME/wyk/state.json` (atomic write, mirrors the
  `theme`/`ui.json` pattern). On start it rehydrates that state; the
  cursor restore is best-effort (a vanished issue falls back to the
  top, never crashes). An explicit `-preset` flag still wins over the
  saved view. A missing file restores nothing; a corrupt one logs and
  starts fresh — never fatal.
- **`wyk depgraph`** — new subcommand that walks the registered
  workspaces and emits the dependency edges between bd issues as a
  text tree (roots first, dependents nested), Graphviz DOT (`-dot`,
  pipe into `dot -Tsvg`), or `{nodes, edges}` JSON (`-json`). Filters:
  `-repo name`, `-priority N` (cap), `-closed` (default omits closed).
  Cross-repo dependency targets are included as nodes; cycles are
  detected and marked rather than looping. Backs a future
  "Dependency graph" page on the docs site that updates from CI.

## [0.4.1] — 2026-05-31

Bug-fix and resilience round: visible-error cleanups, doctor /
refresh polish, and a TUI warm-start that eliminates the
"loading…" beat on every launch.

### Added

- **TUI warm-start cache** — every successful fetch persists to
  `$XDG_CACHE_HOME/wyk/last-fetch.json` (atomic write, 7-day TTL,
  5000-issue cap). On launch, the cached snapshot paints the first
  frame immediately; the live fetch swaps in fresh data when it
  returns. A subtle "cached `<age>`" indicator replaces the
  "synced HH:MM:SS" line until the first live fetch lands.
- **`wyk init -fix-foreign-hooks`** — bulk-chain wyk into every
  registered repo with a foreign post-commit hook. The inverse of
  `wyk doctor -fix` (which leaves foreign hooks alone).
- **Detail-view word wrap** — long descriptions and notes now wrap
  to the viewport width instead of overflowing horizontally.
- **`y` in detail mode** — yank the visible body to the system
  clipboard. Mouse capture is dropped on detail entry so native
  click-drag text selection also works.
- **`s` cycle: `deps`** — sort the visible list so issues with
  fewer dependencies come first (DependencyCount ASC; Priority +
  ID tiebreaks). Pragmatic approximation; true topological order
  is open as a follow-up.
- **`T`** in the TUI cycles the cursor issue's type
  (task → bug → feature → chore → epic → decision → spike → story
  → milestone → task).

### Fixed

- **`refresh failed: ... bd list --json: signal: killed`** — the
  context timeout in `beads.Client.run` is now classified as
  "timed out after `<elapsed>`" / "canceled" instead of leaking
  exec's SIGKILL flavor. The reported duration is the actual
  elapsed time so the message stays accurate when a shorter parent
  deadline fires before our own.
- **Multi-repo failure banner** — when every sub returned the same
  underlying error (typical case: every repo hit the 10s deadline
  because the machine is under load), the banner now coalesces to
  `N repos all failed: <shared-error>` instead of a noisy
  per-repo name list. Mixed errors still render names.
- **`wyk doctor` per-repo timeout** — bumped from 5s to 10s
  (matched to `beads.NewClient()`'s default `Timeout`) and
  extracted as a named constant. A repo that doctor passes but
  the TUI fails on would be a confusing false signal; aligning
  both means a doctor warning predicts a TUI refresh failure.
- **`.beads/.~issues.jsonl.*`** dolt scratch files are gitignored
  — they were showing up as untracked on every `git status`.
- **Cached rows + failed fetch** — the status bar now keeps
  showing "cached `<age>`" instead of misleading "synced
  HH:MM:SS" when the first live fetch after a warm-start fails.
- **SaveCache off the event loop** — the cache persist is
  dispatched as a `tea.Cmd` so the fsync no longer blocks the
  Bubble Tea input/render path.

### Internal

- `applyImportUpdate`, `filterByMaxPriority`, `filterRegistryByName`,
  `filterDumpByName`, `filterDumpSince`, `limitByPriority`,
  `limitActivityEvents` extracted as small shared helpers so tests
  exercise production code, not parallel inline copies.
- Hand-maintained `cliSubcommandDocs` table + committed
  `docs/generated/cli.md` snapshot drives the docs site; drift is
  caught by `make docs-check` in CI.

## [0.4.0] — 2026-05-29

Largest round since v0.1.0 — `wyk` graduates from "TUI over the inbox" to
a CLI-driven toolkit around the same contract. New top-level subcommands
(`activity`, `export`, `import`, `dashboard`, `doctor`, `help`,
`completion`, `update`, `version`) plus a much bigger TUI keymap.

### Added — CLI subcommands

- **`wyk activity`** — recently-touched issues across registered repos.
  Flags: `-since 24h`, `-json`, `-priority N`, `-repo <name>`,
  `-status open|closed|all`.
- **`wyk export`** — JSON dump of every registered repo's full issue
  list + ready IDs. Flags: `-since <dur>` (incremental), `-compact`
  (non-indented), `-repo <name>`.
- **`wyk import`** — restore from a `wyk export` dump. Reads stdin or
  `-file`. Per-repo reconcile: closed-in-dump skipped, open issues
  diff-applied if the ID matches locally, created fresh if not. Flags:
  `-file <path>`, `-dry-run`, `-repo <name>`.
- **`wyk dashboard`** — per-repo rollup of open/human/closed counts.
  Flags: `-json`, `-days N`, `-repo <name>`.
- **`wyk doctor`** — checks bd/wyk on PATH, `$EDITOR`, audit-trail
  actor, XDG paths, and per-repo `.git` / `.beads` / hook state.
  Flag: `-json` for CI-gatable structured output.
- **`wyk help [--markdown]`** — pointer to the in-TUI `?` overlay;
  `--markdown` emits the generated keymap reference for docs.
- **`wyk completion <bash|zsh|fish>`** — shell completion scripts.
- **`wyk version --check`** — polls the release feed; exit 0 current,
  1 newer available, 2 network failure. Honors the stable/any channel
  cached by `wyk update`.

### Added — CLI flags on existing subcommands

- **`wyk inbox`** gains `-priority N` (cap at priority N or higher) and
  `-repo <name>` (alternative to `-C <dir>`).
- **`wyk handoff`** gains `-note <text>` (post a `bd note` after the
  handoff lands).
- **`wyk -preset <name>`** — top-level flag launches the TUI into a
  specific preset (`all`/`ready`/`human`/`mine`/`blocked`).

### Added — TUI

- **Writes:** `u` undo last close, `e` edit description in `$EDITOR`,
  `L` toggle arbitrary label, `O` change owner, `d` defer, `v`
  multi-select with bulk close/flag/defer, `+`/`-` bump priority,
  `.` repeat last write action.
- **Clipboard:** `y` yank ID, `Y` yank "ID — title", `*` yank every
  visible ID, `M` yank cursor as markdown task line (`- [ ] ID —
  title`), `_` yank every visible row as markdown task list.
- **View:** `o` column-visibility overlay (persisted to
  `~/.config/wyk/ui.json`), `C` toggle show-closed across presets,
  `S` reverse sort, `s` cycle sort key, fuzzy-match highlight in the
  Title column, per-preset empty-state copy.
- **Prompts:** `n` multi-line note (textarea), `@name` expand a saved
  fuzzy filter, `:` command palette with `:assign` / `:priority` /
  `:label` / `:filter list` / `:filter remove` / `:bd <args>` (raw bd
  in a scrollable overlay).
- **Background:** fsnotify watch mode (instant refresh on bd writes),
  channel-aware update nudge, mouse-click row selection + wheel scroll,
  stats line in status bar (`· N human · M mine`), help overlay Status
  column legend.

### Added — TUI customization

- **`~/.config/wyk/theme.json`** — optional color overlay. Empty /
  missing keys fall through to the built-in lipgloss styles. Accepts
  ANSI 256 codes (`"212"`) or hex literals (`"#ff66cc"`).
- **`NO_COLOR` / `WYK_NO_COLOR`** — disable styling without rebuilding.

### Changed

- HUMAN badge is always rendered plain (no `← HUMAN` variant); QuickAdd
  now refuses to file an issue without an owner so the audit trail
  doesn't carry blanks.
- `wyk doctor` refactored to share a single `[]check` slice between the
  text and `-json` paths.

### Internal

- `internal/theme/`, `internal/filter/`, `internal/filters/`,
  `internal/uiconfig/`, `internal/watch/`, `internal/clipboard/`,
  `internal/registry/`, `pkg/handoff/` packages.
- Swappable test seams: `clipboardCopy`, `liveFetcher`,
  `currentTagForCheck`, `defaultExportClient`, `defaultImportClient`,
  `defaultActivityClient`.
- golangci-lint v2 with `std-error-handling` exclusions; gofmt drift
  check in CI.
- `CONTRIBUTING.md` covers dev setup, tests, conventions.

## [0.3.3] — 2026-05-29

Patch — fixes the `wyk update` cache trap surfaced testing v0.3.2's
release flow. Recommended for everyone on v0.3.0/v0.3.1/v0.3.2.

### Migration (if you're stuck on v0.3.0/v0.3.1/v0.3.2)

Your installed binary carries the bug, so `wyk update` itself
can't pull v0.3.3 — the stale cache locks the result for up to
24h. Two ways out, pick either:

```bash
# A: nuke the stale cache, then your existing wyk update goes live:
rm -f ~/.cache/wyk/update.json
wyk update

# B: install v0.3.3 directly, bypassing your buggy binary entirely:
go install github.com/jimbottle/would-you-kindly/cmd/wyk@v0.3.3
```

After either, `wyk update` works normally for every future
release (live-fetches every invocation).

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

[Unreleased]: https://github.com/jimbottle/would-you-kindly/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.7.0
[0.6.1]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.6.1
[0.6.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.6.0
[0.5.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.5.0
[0.4.1]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.4.1
[0.4.0]: https://github.com/jimbottle/would-you-kindly/releases/tag/v0.4.0
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
