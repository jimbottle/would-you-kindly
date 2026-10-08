---
name: wyk
description: Use at the start of a work session, when the user asks "what should I work on / what's next", or when picking up tracked work in a bd (beads) project that uses wyk. Surfaces the agent inbox + ready queue, claims work, and acts on human-bounced items. Do NOT use for handing a task back to a human (use wyk-handoff) or auditing the whole backlog for accuracy (use wyk-project-review).
---

# wyk — pick up and track work

This project tracks tasks with **bd (beads)** and the **wyk** handoff
convention. Treat the CLI as the source of truth — run `wyk conventions`
for the authoritative label/inbox contract rather than guessing.

## "What should I work on?" / session start

Other agents may be working this repo at the same time, each on its own
git branch. Work on yours, and **check work out before starting it** —
a claim is a lease that tells every other agent "mine, hands off".

1. Ask wyk, and claim the top pick in one step:

   ```bash
   wyk next -claim -json -compact -slim
   ```

   `wyk next` ranks, in order: issues you already hold (resume them),
   your inbox (work a human bounced back — the default move is to
   **work it now**), then ready work and claims other agents abandoned.
   It never offers an issue another identity holds a live lease on.
   Exit 4 means nothing to claim. Use `wyk next -json` without `-claim`
   to look first, then `wyk claim <id>` for the one you pick.

   An inbox item whose expected unblocker is still missing: re-flag it
   with a note (see wyk-handoff) rather than sitting silently.

2. Read it: `bd show <id>` (details, dependencies, acceptance).

3. Keep the lease alive while you work. It expires after the TTL
   (default 2h) unless renewed. If the agent-nudge Stop hook is
   installed it renews on every turn; otherwise run:

   ```bash
   wyk claim -renew          # renews every lease you hold
   ```

   Stopping without finishing? `wyk claim -release <id>` gives it back.

**Leases, in short.** `wyk claim <id>` exits **3** and names the holder
when another agent holds a live lease: pick something else. A row badged
`@<owner>` in the TUI is checked out by that agent: don't touch it.
`EXPIRED` means the holder stopped renewing; claiming it takes it over
and leaves a note. Use `-force` only when you know the holder is gone.
Your identity defaults to `claude-<session id>`, unique per session, so
concurrent agents never collide. Set `WYK_AGENT_IDENTITY` only if you
have a defined role (`reviewer`, `release-bot`) and want a stable name.
A new session (including after `/clear`) is a new identity, so claims
you made earlier show as another agent's until they lapse; resume one
with `wyk claim -force <id>`.

## File new work

The TUI's owner column is driven by **labels** (not bd's `owner`/`assignee`
fields; `-a`/`--claim` don't set the HUMAN/AGENT badge, though a live
`wyk claim` lease shows as `@<owner>` in its place). A
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
  right now? Claim it: `wyk claim <new-id>`.

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
