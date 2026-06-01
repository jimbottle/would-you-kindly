---
name: wyk-project-review
description: Use when the user asks to review/audit/sanity-check the bd (beads) backlog or plan for accuracy or staleness, runs /wyk-project-review, or at a session checkpoint after substantial work — to verify the tracked issues still match reality based on what you actually did and learned this session. Do NOT use for picking the next task (use wyk) or filing/handing off a single issue (use wyk-handoff).
---

# wyk-project-review — does the tracker match reality?

Cross-check the open issues and the plan against your **current
context** — the code you read, the commits you made, what's actually
done — and propose corrections. The goal is an accurate tracker, not
new feature work.

## 1. Pull the current state

```bash
wyk inbox -json -compact -slim     # items a human bounced back
bd ready                           # unblocked work
bd list --status=open --json       # the full open backlog
wyk depgraph                       # dependency structure (text tree)
bd doctor --check=conventions      # lint / stale / orphans
```

## 2. Audit each open issue against what you know

For every open issue, assign a verdict and the bd action it implies:

- **accurate** — still correct and needed → leave it.
- **done-but-open** — the work already landed this session → close it
  (`bd close <id> --dolt-auto-commit=on`).
- **stale** — title / description / acceptance no longer match the
  code → update (`bd update <id> --title/--description/--notes …
  --dolt-auto-commit=on`).
- **mis-scoped / too big** — split into focused issues.
- **duplicate** — `bd supersede <dup> --with=<keep> --dolt-auto-commit=on`.
- **wrong status** — open vs blocked vs deferred. Add the real blocker
  (`bd dep add <id> <blocker>`) or `bd defer <id> --until "…"`.
- **missing** — work you know is needed but isn't filed → `bd create`.

## 3. Check the plan's coherence

Using `wyk depgraph`, `bd blocked`, and `bd orphans`: dependencies
reflect reality, no cycles / orphans / duplicates, and any epics still
decompose into the right children.

## 4. Report first, then apply on confirmation

Produce a concise per-issue report: `id → verdict → proposed action`.
Apply the changes (closes / updates / deps / defers / new issues) once
the user confirms, respecting `--dolt-auto-commit=on` and the status
lifecycle in `docs/CONTRACT.md`.
