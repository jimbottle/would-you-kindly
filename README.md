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
# Latest tagged release:
go install github.com/jimbottle/would-you-kindly/cmd/wyk@v0.1.0

# Or tip of main:
go install github.com/jimbottle/would-you-kindly/cmd/wyk@latest
```

Or from a checkout:

```bash
go build -o ./bin/wyk ./cmd/wyk
```

## Run

After running `wyk init` in each repo you want to track, just:

```bash
wyk
```

— and the TUI shows issues from every registered repo, with a
`Repo` and `Branch` column on the left of the table. Writes (`c`,
`H`, `n`) route to the correct repo automatically. The registry
lives at `~/.config/wyk/repos.json` (or `$XDG_CONFIG_HOME/wyk/...`)
and is plain editable JSON.

For a single repo (without going through the registry):

```bash
wyk -C /path/to/repo
```

If the registry is empty or has only one entry, `wyk` (no args)
falls back to running against the current directory — the v0.1.0
single-repo behaviour.

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

### Handing a task to a human (agent side)

For an agent that's just decided "this needs a human" (no bd issue
yet), file and hand off in one shot:

```bash
cat runbook.md | wyk handoff -create "Rotate staging DB password" -priority 1
# created would-you-kindly-63o — "Rotate staging DB password"
# handed would-you-kindly-63o to human (327-byte runbook)
```

For an existing issue:

```bash
cat runbook.md | wyk handoff wyk-42
# handed wyk-42 to human (327-byte runbook)
```

`wyk handoff` tags the issue with the `human` label and replaces its
description with the runbook from stdin (or `--file <path>`). With
`-create`, it runs `bd create` first (with `src:agent`) and uses the
new ID for the handoff. Same
contract as the TUI's `H` key — see
[`docs/CONTRACT.md`](docs/CONTRACT.md). Go programs can call
[`pkg/handoff.BounceToHuman`](pkg/handoff/handoff.go) directly.

### Picking up bounced-back work (agent inbox)

The other direction of the handoff loop: when a human presses `H` to
remove the `human` label, the issue lands in the agent's inbox.

```bash
wyk inbox          # human-readable list across every registered repo
wyk inbox -json    # structured output for an LLM to ingest
# > 1 issue(s) in inbox:
# >   would-you-kindly-037   P4  Configure production OAuth client
```

The canonical query is `label=src:agent AND NOT label=human AND
status!=closed` — things you (the agent) filed that a human has
touched but left open. Use this at the start of a session to find
what you need to act on next.

#### Claude Code skill

A project-local Claude Code skill at
[`.claude/skills/handoff/SKILL.md`](.claude/skills/handoff/SKILL.md)
tells any Claude session that opens this repo *when* `handoff` is the
right call and *how* to write a runbook the human can act on. The
skill is explicit about what handoff is NOT (clarifying questions,
tedious-but-doable work, quick reversible edits) — handoff is for
"I know what to do but genuinely cannot do it."

### Auto-closing issues on commit (`wyk init`)

```bash
wyk init
# wyk init: installed post-commit hook at .git/hooks/post-commit
```

After `wyk init`, every commit whose message contains a
`Closes:`, `Fixes:`, or `Resolves:` trailer (case-insensitive) auto-
closes the referenced bd issue. Hierarchical IDs work too:

```
Closes: would-you-kindly-ma5.4
Fixes #bd-42
Resolves: my-project-abc
```

**One ID per line.** A trailer listing multiple IDs (`Closes: bd-1,
bd-2`) is rejected wholesale — use two separate `Closes:` lines.
This is deliberate; it avoids closing extras from prose like
`Closes: bd-1 (we'll handle bd-2 next week)`.

If `.git/hooks/post-commit` already exists from another tool
(e.g. `roborev`, `husky`, `pre-commit`), you have three options:

- `wyk init -chain` (recommended) — preserves the existing hook
  at `post-commit.pre-wyk` and writes a wrapper that runs both:
  the original first, then wyk's auto-close. Non-destructive.
- `wyk init -force` — overwrites the existing hook entirely.
  Destructive — only use if you don't need the other tool's hook.
- `wyk init -dry-run` — preview what either path would do without
  writing anything.

To uninstall a chained install: `rm .git/hooks/post-commit` and
(optionally) `mv .git/hooks/post-commit.pre-wyk .git/hooks/post-commit`
to restore the original.

## Keys

### Reading

| Key       | Action                                            |
| --------- | ------------------------------------------------- |
| `j` / `k` | Move down / up                                    |
| `g` / `G` | Top / bottom of the list                          |
| `]` / `[` | Next / previous human-flagged issue (wraps)       |
| `enter`   | Open the selected issue (read its instructions)   |
| `esc`     | Back to the list                                  |
| `/`       | Open the fuzzy filter input (matches title + body)|
| `h`       | Jump to the human-flagged view                    |
| `tab`     | Cycle preset filters (all → ready → human → mine → blocked) |
| `r`       | Refresh from bd now                               |
| `?`       | Open the help overlay                             |
| `q`       | Quit                                              |

### Writing

| Key | Action                                                      |
| --- | ----------------------------------------------------------- |
| `c` | Close the cursor issue (asks `[y/N]` to confirm)            |
| `H` | Toggle the `human` label on the cursor issue                |
| `n` | Append a note to the cursor issue (opens a text prompt)     |

After any write, the list refetches and a status banner appears
above the help bar (e.g. `closed wyk-42`, or `close wyk-42 failed: …`).

The list also refreshes itself every 10 seconds.

## Status

**Phase 1 — read-only MVP.** wyk can list, filter, and read bd issues.
It cannot yet create, update, or close them; those are the next phase.
See `bd ready` in this repo for the open work.

## License

MIT. © Raylitics LLC.
