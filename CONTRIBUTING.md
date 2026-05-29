# Contributing to would-you-kindly

This file covers what you need to know to develop, test, and submit changes
to wyk. For the project's purpose and convention overview, read
[README.md](README.md) and [docs/CONTRACT.md](docs/CONTRACT.md) first.

## Dev setup

Required:

- Go 1.26+ (the version pinned in [`go.mod`](go.mod))
- [`bd`](https://github.com/gastownhall/beads) on your `PATH` — wyk shells
  out to it for every read and write, and the test suite needs it for the
  `wyk doctor` / multi-repo paths
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

Run the full suite before submitting:

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
- If the change closes a bd issue, end the body with `Closes bd <id>.`

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
gofmt -l .                         # must be empty
~/go/bin/golangci-lint run ./...   # must report 0 issues
go test -race -timeout 5m ./...    # must pass
```

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

The project uses [`roborev`](https://github.com/anthropic/roborev) for
multi-agent code reviews on every push. After your commit lands you'll see
review jobs appear; address findings via `/roborev-fix` (or the equivalent
manual flow) before the next push.

## Releasing

Maintainers only: see commit history around `v0.3.x` for the cadence. Each
release is a `git tag -a vX.Y.Z` + push; `wyk update` finds it via the
GitHub releases API.
