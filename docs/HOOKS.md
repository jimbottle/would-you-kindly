# The handoff hook

wyk can run a command of yours every time an issue changes hands
between an agent and a human. That is the whole feature: wyk pipes one
JSON document to the command's stdin, reads one optional JSON line back,
and records what came back on the bd issue. The command can do anything
with it — create a task in your to-do app, post to a channel, put a
block on your calendar. wyk knows nothing about the other side.

The hook fires **only for the human-labelled side of the contract**
([`CONTRACT.md`](CONTRACT.md)): when `human` is added, when it is
removed, and when a human-labelled issue is closed. Agent-owned issues
never reach it. That is deliberate: the hook exists to mirror *the
human's* to-do list somewhere the human already looks.

The first adapter is [BasicDo](https://github.com/jimbottle/basicdo)'s
`scripts/wyk-handoff-hook.mjs`; its `docs/WYK.md` is the worked example
of everything below.

## Turning it on

```bash
wyk config set hooks.handoff.command 'node ~/Projects/basicdo/scripts/wyk-handoff-hook.mjs'
wyk config set hooks.handoff.timeout_seconds 30        # optional; default 15
wyk config set hooks.handoff.events handoff,close      # optional; default: all
wyk doctor                                             # proves the command runs
```

That writes a `hooks.handoff` block to `~/.config/wyk/config.json`
(XDG-aware). The file is plain JSON and hand-editable:

```json
{
  "version": 1,
  "hooks": {
    "handoff": {
      "command": "node ~/Projects/basicdo/scripts/wyk-handoff-hook.mjs",
      "timeout_seconds": 30,
      "events": ["handoff", "close"]
    }
  }
}
```

`$WYK_HANDOFF_HOOK` overrides the command for one run (and enables the
hook with every event if nothing is configured) — handy for trying a
script before committing to it. Clearing the command disables the hook:
`wyk config set hooks.handoff.command ''`.

The command runs through `sh -c`, so it may carry arguments and shell
expansions. Nothing from the issue is ever interpolated into the
command line — the issue travels on stdin and in `WYK_*` variables —
so a hostile issue title cannot become a shell command.

## When it fires

| Event     | Fires after…                                                                  | Where                                                 |
| --------- | ----------------------------------------------------------------------------- | ----------------------------------------------------- |
| `handoff` | the `human` label is added and the runbook written                            | `wyk handoff`, `wyk handoff -create`, TUI `H` (soon)  |
| `bounce`  | the `human` label is removed — the human sent it back to the agent            | TUI `H` (soon), `wyk hook dispatch bounce <id>`       |
| `close`   | a human-labelled issue is closed                                              | TUI close, post-commit auto-close (soon), `dispatch`  |
| `ping`    | nothing — a probe with no issue, so a script can prove it starts and connects | `wyk doctor`, `wyk hook dispatch ping`                |

Transitions made with the raw `bd` CLI (`bd label add <id> human`) are
invisible to wyk and do not fire the hook. Use `wyk handoff`, or replay
afterwards with `wyk hook dispatch handoff <id>`.

`ping` is never filtered out by `events`, so `wyk doctor` can always
probe a hook that is narrowed to, say, `handoff` only.

## What the hook receives

One JSON document on stdin (`schema_version` 1), followed by a newline.
Every field wyk knows about the transition is there; take what you need
and ignore the rest. Additive fields keep the schema version; a renamed
or removed field bumps it — check it before trusting the shape.

```json
{
  "schema_version": 1,
  "event": "handoff",
  "actor": "cli",
  "timestamp": "2026-10-08T21:14:02Z",
  "wyk_version": "wyk v0.9.0 (abc1234)",
  "issue": {
    "id": "wyk-42",
    "title": "Rotate the staging DB password",
    "status": "open",
    "priority": 1,
    "issue_type": "task",
    "created_at": "2026-10-08T21:13:58Z",
    "updated_at": "2026-10-08T21:14:01Z",
    "description": "## Why this needs you …\n\n## Steps\n1. …",
    "owner": "jimbottle@users.noreply.github.com",
    "assignee": "jimbottle",
    "created_by": "jimbottle",
    "labels": ["human", "session:97d98ec7-…", "src:agent"],
    "due_at": "2026-10-10T17:00:00Z",
    "external_ref": "",
    "dependencies": [],
    "comment_count": 0
  },
  "runbook": "## Why this needs you …\n\n## Steps\n1. …",
  "runbook_kind": "task",
  "repo": { "name": "would-you-kindly", "path": "/Users/me/Projects/would-you-kindly", "prefix": "wyk" },
  "identity": "alice",
  "session": "97d98ec7-3163-41a6-82f7-6bfde3966ed7",
  "note": "see PR 7",
  "external": { "ref": "basicdo:task:abc123" }
}
```

Field by field:

- **`event`**, **`actor`** — the transition (table above) and the wyk
  surface that made it: `cli` (`wyk handoff`, `wyk hook dispatch`),
  `tui`, or `hook` (the post-commit auto-close).
- **`timestamp`**, **`wyk_version`** — when wyk fired, and from which
  build, so a script can refuse a payload from a version it has not
  been tested with.
- **`issue`** — the bd issue exactly as `bd show --json` returned it at
  that moment: labels, notes, `due_at` (from `wyk handoff -create -due`
  or `bd update --due`), `external_ref`, dependencies, timestamps.
  Absent for `ping`. Empty optional fields are omitted.
- **`runbook`**, **`runbook_kind`** — the issue description at the
  moment of the event; for a handoff that is the runbook the human will
  follow. `runbook_kind` is `task` (it has a `## Steps` heading),
  `question` (`## Question`), or empty.
- **`repo`** — the workspace: the registry's short name (what the TUI's
  Repo column shows; the directory basename when unregistered), its
  absolute path, and bd's `issue_prefix`.
- **`identity`**, **`session`**, **`note`** — the `-identity` routing
  target, the Claude Code session id that filed it, and the `-note`
  text, when present.
- **`external`** — what an earlier run of the hook recorded for this
  issue (see below). Absent on the first handoff. **A script that
  receives it should update that task, not create another.**

A few basics are mirrored into the environment for one-line shell
scripts: `WYK_HOOK_EVENT`, `WYK_HOOK_SCHEMA_VERSION`, `WYK_ISSUE_ID`
(absent for `ping`), `WYK_REPO`, `WYK_REPO_PATH`. Everything else is
in the JSON.

## What the hook may answer

Nothing is required: exit 0 with an empty stdout means "done". To let
wyk remember what you created, print **one JSON object as the last
line of stdout**:

```json
{"ref": "basicdo:task:abc123", "url": "http://basicdo.local/tasks/abc123"}
```

- **`ref`** is an opaque string that identifies the external record.
  wyk stores it in bd's own `external_ref` field on the issue
  (`bd update <id> --external-ref`) and sends it back as `external.ref`
  on every later event for that issue. It is the idempotency key: a
  script that honours it never creates a duplicate, however many times
  the same handoff is replayed.
- **`url`** is a human-facing link. On a first handoff wyk adds a bd
  note, `Handed off to <url>`, so it shows in `bd show` and the TUI's
  detail pane, and prints it as the last line of `wyk handoff`.

Earlier stdout lines are ignored (so a script may log progress there),
but if the last non-empty line is not a JSON object the run counts as
failed — a silently lost `ref` is exactly what causes duplicates later.
Put diagnostics on stderr.

## When the hook fails

The hook runs **after** every bd write for the transition has landed,
and wyk never rolls those back. So a failing hook leaves the human with
the task in bd and only the external mirror missing. wyk reports it
loudly and distinctly:

- `wyk handoff` prints the script's stderr and exits **3** — not 1, so
  an agent can tell "the handoff failed, retry it" (1) from "the human
  has the task; the mirror is missing" (3). It names the replay command.
- A timeout (`timeout_seconds`, default 15) is reported as such with the
  config key to raise.
- `ref` came back but could not be written to bd: also a failure with
  the replay command, because losing the link is the duplicate risk the
  ref exists to prevent. Replaying sends the (absent) ref and lets the
  script dedupe by its own means.

Replay any event for any issue, at any time:

```bash
wyk hook dispatch handoff wyk-42          # re-send a handoff (also: backfill)
wyk hook dispatch close wyk-42            # the issue was closed outside wyk
wyk hook dispatch ping                    # what wyk doctor runs
wyk hook dispatch -C ~/other/repo bounce other-7
```

`dispatch` rebuilds the payload from `bd show`, so it reflects the
issue's current state, not the state at the original event.

## What an agent sees

An agent using wyk learns the hook is on from several places, so it
never discovers the external task after the fact:

- `wyk handoff -dry-run` prints `would run handoff hook: <command>`.
- `wyk handoff` prints `hook (handoff): <url>` as its last line on
  success, and the stderr block above on failure (exit 3).
- `wyk conventions` (text and `-json`) describes the hook when one is
  configured, and `wyk inbox -json` / `wyk export` carry each issue's
  `external_ref`. *(Landing with the rest of the epic.)*
- `wyk doctor` runs a `ping` and reports whether the command exists,
  is executable, and answers. *(Same.)*

## Writing an adapter

A minimal shell adapter that just logs every event:

```bash
#!/bin/sh
# ~/bin/wyk-hook-log.sh — append each event to a file; answer nothing.
cat >> "$HOME/wyk-handoffs.jsonl"
```

A real adapter, in whatever language, should:

1. Read all of stdin, parse the JSON, check `schema_version == 1`.
2. On `ping`: verify its own configuration (credentials, reachability)
   and exit 0 without side effects.
3. On `handoff`: if `external.ref` is present, update that record;
   otherwise look the issue up by a key it derives (`repo.name` +
   `issue.id` is stable), create the record if absent, and print
   `{"ref": …, "url": …}`.
4. On `bounce` and `close`: complete or archive the record named by
   `external.ref` (or found by the derived key). Print nothing, or the
   same `ref`.
5. Put every message for a human on stderr; exit non-zero on any
   failure. wyk quotes stderr in its own report.
6. Keep secrets in the environment (`BASICDO_API_KEY`, …), never in
   `config.json`, which is world-readable JSON.

The BasicDo adapter in `basicdo/scripts/wyk-handoff-hook.mjs` is the
reference for all six.

## Design notes

- **bd's `external_ref`, not a wyk label.** bd already has a field for
  "this issue is mirrored elsewhere"; a `ext:<ref>` label would have
  duplicated it and leaked into every label-driven view.
- **The runbook is the description.** The contract says the description
  *is* the runbook, so the payload sends it under both names: `runbook`
  for the common case, `issue.description` for scripts that want the
  raw record.
- **Stdin, not argv.** Runbooks are long and arbitrary; argv has length
  limits and quoting rules. JSON on stdin has neither.
- **One command, many events.** One script with a `switch` on `event`
  is easier to install and reason about than four hook slots.
- **Human-side only.** An agent's own issues are the agent's business.
  The point of wyk is the moment an agent needs a human; that is what
  gets mirrored.
