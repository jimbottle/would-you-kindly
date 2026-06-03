---
name: wyk
description: Use at the start of a work session, when the user asks "what should I work on / what's next", or when picking up tracked work in a bd (beads) project that uses wyk. Surfaces the agent inbox + ready queue, claims work, and acts on human-bounced items. Do NOT use for handing a task back to a human (use wyk-handoff) or auditing the whole backlog for accuracy (use wyk-project-review).
---

# wyk — pick up and track work

This project tracks tasks with **bd (beads)** and the **wyk** handoff
convention. Treat the CLI as the source of truth — run `wyk conventions`
for the authoritative label/inbox contract rather than guessing.

## "What should I work on?" / session start

1. Check the agent inbox FIRST — work a human bounced back to you:

   ```bash
   wyk inbox -json -compact -slim
   ```

   These are issues where the human removed the `human` label to say
   "back to you". The default move is to **work them now**, not defer —
   that round-trip is the whole point. Exception: if the expected
   unblocker is still missing, re-flag it with a note (see wyk-handoff)
   rather than sitting silently.

2. Find unblocked work:

   ```bash
   bd ready                 # issues with no open blockers
   bd show <id>             # details, dependencies, acceptance
   ```

3. Claim before starting. bd writes do NOT persist without the
   auto-commit flag:

   ```bash
   bd update <id> --claim --dolt-auto-commit=on
   ```

## File new work

The TUI's owner column is driven by **labels** (not bd's `owner`/`assignee`
fields, which this project ignores; `-a`/`--claim` don't set the badge). A
task with no owner label **defaults to AGENT** — the column is never blank.

So the one thing that matters: **if a task needs a human, hand it off** —
otherwise it silently defaults to AGENT and the human never sees it.

The badge has four states: **HUMAN** (a human must act), **AGENT** (yours),
**HUMAN-BLOCK** (yours but blocked by a human-flagged dep), and
**AGENT-HANDOFF** (`agent-handoff` label — *another* agent is working it).
If you see **AGENT-HANDOFF**, do NOT touch that task: a human orchestrates
the cross-agent coordination, and it's excluded from your inbox for exactly
this reason. Flag a task that way with `bd label add <id> agent-handoff
--dolt-auto-commit=on` when you need to fence off work another agent owns.

- **A task that needs a human → use the wyk-handoff skill**, never
  hand-rolled labels: `wyk handoff -create "…"` sets `human` (HUMAN
  badge) with a runbook.

- **Agent-filed work** → file it with `wyk create` (a thin `bd create`
  wrapper that forwards every flag AND stamps the Claude session, so the
  TUI's Session column shows which conversation filed it):

  ```bash
  wyk create "…" --description "why + what" --type task --labels src:agent
  ```
  (`--dolt-auto-commit=on` is added for you. A bare `bd create` still
  works and badges AGENT, but won't record the session.) Starting it
  right now? Also mark it in progress:
  `bd update <new-id> --claim --dolt-auto-commit=on`.

## Finish a task

```bash
bd close <id> --dolt-auto-commit=on
```

Then run the project's quality gates and follow its commit conventions
(see CLAUDE.md).

## The convention, in one line

Run `wyk conventions` for the full text. In short: agent-filed issues
carry `src:agent`; an issue for a human also carries `human`; the
agent inbox is `src:agent AND NOT human AND status != closed`.
