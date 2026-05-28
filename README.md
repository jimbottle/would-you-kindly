# would-you-kindly

A terminal UI over the [beads](https://github.com/gastownhall/beads) issue
tracker, built for one specific moment: when an agent finishes the
mechanical part of a task and needs a human to do the part it cannot.

`wyk` (the binary) lists your bd issues, navigates them with vim keys, and
lets you press one key — `h` — to filter down to exactly the issues an
agent has flagged for your attention.

## Why

Most issue trackers assume tasks are either assigned to a person or
not. When an agent is doing most of the work, a third state matters:
"the agent has done what it can and is handing this back to a human."
`wyk` keys off a small, explicit convention (see
[`docs/CONTRACT.md`](docs/CONTRACT.md)) and gives that state its own
keystroke.

## The contract in one sentence

A task is for a human when it carries the `human` label; its
description is the runbook the human follows; `src:agent` / `src:human`
records who filed it. The full contract lives in
[`docs/CONTRACT.md`](docs/CONTRACT.md).

## Install

You'll need Go 1.26+ (matching `go.mod`) and
[bd](https://github.com/gastownhall/beads) on your `PATH`.

```bash
go install github.com/jimbottle/would-you-kindly/cmd/wyk@latest
```

Or from a checkout:

```bash
go build -o ./bin/wyk ./cmd/wyk
```

## Run

From any directory that contains a `.beads/` workspace:

```bash
wyk
```

Or against a specific repo:

```bash
wyk -C /path/to/repo
```

### Non-TTY one-shot

For scripts and CI:

```bash
wyk --probe
# 3 issue(s) flagged for human:
#   would-you-kindly-2oa      P1  Rotate the staging database password
#   would-you-kindly-1ej      P2  Approve the v0.3.0 release on GitHub
#   would-you-kindly-117      P3  Decide retention policy for ephemeral wisp beads
```

Exits 0 on success, 2 if bd is missing or there's no workspace, 1 on
other errors.

## Keys

| Key       | Action                                            |
| --------- | ------------------------------------------------- |
| `j` / `k` | Move down / up                                    |
| `g` / `G` | Top / bottom of the list                          |
| `enter`   | Open the selected issue (read its instructions)   |
| `esc`     | Back to the list                                  |
| `/`       | Open the fuzzy filter input (matches title + body)|
| `h`       | Jump to the human-flagged view                    |
| `tab`     | Cycle preset filters (all → ready → human → mine → blocked) |
| `r`       | Refresh from bd now                               |
| `q`       | Quit                                              |

The list also refreshes itself every 10 seconds.

## Status

**Phase 1 — read-only MVP.** wyk can list, filter, and read bd issues.
It cannot yet create, update, or close them; those are the next phase.
See `bd ready` in this repo for the open work.

## License

MIT. © Raylitics LLC.
