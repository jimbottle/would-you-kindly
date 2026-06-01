---
name: wyk-handoff
description: Use when you have done everything you can on a task but a remaining step genuinely requires human authority — auth/secrets, an irreversible or legal/political/financial decision, physical access, or clicking through a third-party UI. Files and hands the task back to a human via the wyk CLI with a runbook. Do NOT use for work you could do yourself but find tedious, and do NOT use for ambiguity a clarifying question would resolve (ask the question instead).
---

# wyk-handoff — hand a task back to a human

When the next step is genuinely a human's to take, hand it off with a
runbook they can follow. The issue's **description IS that runbook**.

## Hand off

Flags go before the issue id (Go flag parsing stops at the first
positional arg):

- Existing issue, runbook from a file or stdin:

  ```bash
  wyk handoff -file runbook.md <id>
  cat runbook.md | wyk handoff <id>
  ```

- File a NEW human task and hand it off in one step (the recommended
  path for new human work):

  ```bash
  wyk handoff -create "<imperative title>" -file runbook.md
  ```

  Add `-dry-run` to print the labels + runbook that would be written
  without touching bd — use it to sanity-check the runbook first.

`wyk handoff` applies the right labels (`human` + `src:agent`) and sets
the issue description to your runbook. Prefer it over hand-rolling
labels via `bd create`.

## Write a complete runbook

The human should be able to act without re-deriving context. State:
the exact step(s) only they can do, the commands / URLs / files
involved, what "done" looks like, and what to hand back. Keep it tight
— no filler, no restating things they already know.

## Do NOT hand off

- Something **you** can do (auth you already hold, a refactor, a test,
  a doc). Do it.
- Ambiguity about intent — ask a clarifying question instead.
- A blocker that's another tracked issue — record the dependency
  (`bd dep add <id> <blocker> --dolt-auto-commit=on`) and work
  elsewhere; don't hand a human a task they can't action yet.
