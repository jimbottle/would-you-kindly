# Contributing to would-you-kindly

This file covers what you need to know to develop, test, and submit changes
to wyk. For the project's purpose and convention overview, read
[README.md](README.md) and [docs/CONTRACT.md](docs/CONTRACT.md) first.

## Dev setup

Required:

- Go 1.26+ (the version pinned in [`go.mod`](go.mod))
- [`bd`](https://github.com/gastownhall/beads) on your `PATH` — wyk shells
  out to it for every read and write, and the test suite needs it for the
  `wyk doctor` / multi-repo paths. **`github.com/gastownhall/beads` is the
  canonical source** (the older `github.com/steveyegge/beads` path now
  redirects there); wyk targets **bd 1.0.4 or newer**, and a few behaviors
  are pinned to that floor (`wyk doctor` warns on older). Note that bd's own
  generated `.beads/README.md` may still print the legacy `steveyegge` URL —
  that file is bd's, not wyk's.
- A POSIX shell + git

Optional but recommended:

```bash
# golangci-lint v2 is what CI runs; tests pass without it but CI will catch
# any drift.
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
```

Clone, then:

```bash
go build -o ./bin/wyk ./cmd/wyk
./bin/wyk --version
```

## Tests

The one command that matters:

```bash
make check
```

It mirrors the `test` CI workflow exactly — gofmt, the generated-docs
drift check, `go vet`, golangci-lint at CI's pinned version, build, and
`go test -race` — plus the plugin-skills drift guard. A green
`make check` is a green push. The individual pieces, when you want one
à la carte:

```bash
go test -race -timeout 5m ./...
```

`-race` is what CI runs and matters here — `MultiBDSource`'s parallel-fetch
path has caught real concurrency bugs through this exact invocation.

Lint and format gates:

```bash
gofmt -l .                  # must produce no output
golangci-lint run ./...     # CI-blocking; matches .golangci.yml
```

Single test debugging:

```bash
go test ./internal/tui/... -run TestNameYouCareAbout -v
```

## Project layout

| Path | What lives here |
|------|----------------|
| `cmd/wyk/` | Subcommand entry points + main.go dispatcher |
| `internal/tui/` | The bubbletea TUI: model, keymap, sources, render |
| `internal/beads/` | Thin client over the `bd` CLI binary |
| `internal/filter/` | Preset → bd-query mapping (`PresetReady`, `PresetHuman`, etc.) |
| `internal/registry/` | The `~/.config/wyk/repos.json` schema and loader |
| `internal/uiconfig/`, `internal/filters/` | Other per-user config files |
| `internal/clipboard/`, `internal/updater/`, `internal/watch/` | Specialised concerns |
| `pkg/handoff/` | The handoff runbook contract (importable; everything else is `internal/`) |

The TUI's source-of-truth keymap lives in
[`internal/tui/keymap.go`](internal/tui/keymap.go). The in-TUI help overlay
and `wyk help --markdown` both render from the same `DocsKeymap` function,
so adding a binding lands in both with one edit.

## Commit conventions

Look at `git log --oneline` for the style. The short form:

- One-line subject in `area: imperative verb` form
  (`feat(tui): 'y' yanks the cursor issue ID`,
  `fix(updater): cache trap on stale 24h TTL`)
- A blank line, then a body explaining the **why** (the diff already shows
  the what). Wrap at ~72 characters.
- If the change closes a bd issue, end with a `Closes:` trailer — one bare
  issue ID per line, colon required. The post-commit hook scans for this
  exact form and auto-closes matching issues; the prose form `Closes bd
  <id>.` is **not** picked up. Example:

  ```
  Closes: would-you-kindly-ma5.4
  ```

  See [README's auto-close section](README.md#auto-closing-issues-on-commit-wyk-init)
  for the full rules (multiple IDs need separate trailer lines; explanatory
  text on the trailer line disqualifies it).

The `area` prefixes in current use: `feat`, `fix`, `ci`, `docs`, `chore`.

## Issue tracking

This project tracks its own work in bd, in the same repo. `wyk` or
`bd ready` will show you what's open.

When you find a bug or want to propose a feature, file a bd issue **before**
you start coding — the description is the runbook everyone (humans + agents)
follows during the work. Agent-filed candidates get the `src:agent` label;
human-filed ones don't need a source label.

## Before you submit a PR

```bash
make check                         # the whole CI gate, locally
```

If you'd rather see the pieces: `gofmt -l .` must be empty,
`golangci-lint run ./...` must report 0 issues, `go test -race -timeout
5m ./...` must pass, and `make docs-check` must find `docs/generated/`
current (run `make docs-snapshot` after touching a flag or keybinding).

If you added a TUI keybinding, also confirm:

- The binding is documented in [`internal/tui/keymap.go`](internal/tui/keymap.go).
- It lands in [`DocsKeymap`](internal/tui/keymap.go) so both the in-TUI
  overlay (`?`) and `wyk help --markdown` pick it up.
- The new mode (if any) is listed in `chromeExtra`'s switch (`internal/tui/model.go`)
  so the body-height budget stays accurate.

If you added a write action, also confirm:

- A `bulkVerbs` entry for the past-tense verb.
- An `issueExists` guard on the single-target dispatch path.

## Code review

Maintainers run an automated review pass over incoming changes in addition
to human review, so you don't need to run anything extra. Just make sure
`make check` is green and open your PR; expect review comments, and
addressing them is the normal back-and-forth.

## Releasing

Maintainers only. Each release is a `git tag -a vX.Y.Z` + push; the release
workflow cuts the GitHub release from the matching `CHANGELOG.md` section,
and `wyk update` finds it via the GitHub releases API.

### Versioning (pre-1.0)

While the project is on `0.y.z`, **default to a patch bump (`0.y.Z`).**
The large majority of releases — bug fixes, additive flags, internal
refactors, doc/CI changes, small UX tweaks — are patches. Cutting a fresh
minor for an ordinary batch of work churns the version for no reader
benefit; the minor digit should track **milestones, not activity**.

Bump the **minor (`0.Y.0`)** only when a release clears a real bar — at
least one of:

- a **new subcommand** or a substantial new user-facing capability;
- a **breaking change** to a flag, a command's output / JSON shape, or the
  handoff contract / label conventions;
- a change a user would have to **read the release notes to adopt** (not
  just "things got better").

When in doubt, it's a patch. A grab-bag of fixes and small additions is a
patch even if there are a lot of them.
