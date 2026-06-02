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

## File new work — ⛔ EVERY bead MUST have an owner

**NEVER run a bare `bd create`.** Every issue you file MUST carry an
owner label, or it renders with a **blank owner badge** in the TUI — a
defect this project does not tolerate. There is no such thing as an
ownerless task.

"Owner" here is the **label** that drives the badge — `src:agent` (AGENT)
or `human` (HUMAN) — **not** bd's `owner`/`assignee` fields, which this
project ignores. `-a`/`--claim` do NOT give a task an owner badge.

- **Agent-filed work — the default — ALWAYS pass `--labels src:agent`:**

  ```bash
  bd create "…" --description "why + what" --type task --labels src:agent --dolt-auto-commit=on
  ```
  Starting it right now? Also mark it in progress:
  `bd update <new-id> --claim --dolt-auto-commit=on` (this is about
  status, not ownership — the `src:agent` label above is what owns it).

- **A task that needs a human → use the wyk-handoff skill**, never
  hand-rolled labels: `wyk handoff -create "…"` sets `human` (HUMAN
  badge) with a runbook.

If you ever slip and create without an owner label, the task is a defect
— fix it the instant you notice:
`bd label add <id> src:agent --dolt-auto-commit=on`. Before ending a
session, confirm **zero** blank badges:

```bash
bd list --all --json | jq '[.[]|select((.labels//[])|any(.=="human" or .=="src:agent" or .=="src:human")|not)|.id]'
```

That array MUST be empty.

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
