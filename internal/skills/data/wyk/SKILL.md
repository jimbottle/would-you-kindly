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

**Every task gets an owner.** bd auto-sets `owner` (who filed it) but
NOT `assignee` (who's responsible for doing it) — so always set the
assignee when you create work. Don't leave orphan tasks.

- A task you'll start now → claim it (sets assignee + in_progress):

  ```bash
  bd create --title "…" --description "why + what" --type task --dolt-auto-commit=on
  bd update <new-id> --claim --dolt-auto-commit=on
  ```

- A task for later → assign without starting it:

  ```bash
  bd create --title "…" --description "…" --type task -a <assignee> --dolt-auto-commit=on
  ```

- A task that needs a **human**: don't hand-roll labels — use the
  **wyk-handoff** skill (`wyk handoff -create "…"`), which owns the
  human handoff.

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
