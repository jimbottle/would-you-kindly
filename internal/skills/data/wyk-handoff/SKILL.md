---
name: wyk-handoff
description: Use when you have done everything you can on a task but a remaining step genuinely requires human authority — auth/secrets, an irreversible or legal/political/financial decision, physical access, or clicking through a third-party UI. Files and hands the task back to a human via the wyk CLI with a runbook. Do NOT use for work you could do yourself but find tedious, and do NOT use for ambiguity a clarifying question would resolve (ask the question instead).
---

# wyk-handoff — hand a task back to a human

When the next step is genuinely a human's to take, hand it off with a
runbook they can follow. The issue's **description IS that runbook**.

## First: is this a task or a question?

Decide before you write a word. The runbook's middle section declares
which, and `wyk handoff` **refuses** a runbook that has neither heading.

- **Task** — you need the human to **do** something (click through a
  UI, rotate a secret, approve a PR). The middle section is
  `## Steps`: numbered directions they can follow **without
  re-deriving your context** — every command, URL, file path and
  account, in order, then how to verify, then "Close this issue when
  complete." A task with no directions is a wish, not a task.
- **Question** — you need the human to **answer or decide**
  something. The middle section is `## Question`: the exact question
  as one sentence ending in `?`, the options you weighed with the
  consequence of each, your recommendation, and where to record the
  answer (a `bd note` on the issue, then bounce back with `H`). Don't
  dress a decision up as steps ("1. Decide X"). And if a reply in the
  current conversation would answer it, ask it there — don't hand off.

Get the skeleton with `wyk handoff -template` (task) or
`wyk handoff -template -question` (question), fill it in, and pass it
as the runbook.

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

Three sections, in this order:

1. `## Why this needs you (please confirm this is accurate)` — what
   you tried (three concrete attempts), the wall you hit, why no
   workaround exists. Phrased as a claim the human can push back on.
2. `## Steps` (task) **or** `## Question` (question) — see above.
3. `## What unblocks me when this returns` — the concrete artifact
   you expect back (credential at a known path, URL in a constant, a
   decision in a note), and for a question what you'll do with each
   answer, so the next agent can resume without re-asking.

The human should be able to act without re-deriving context. Keep it
tight — no filler, no restating things they already know.
`wyk handoff -dry-run` runs the same shape check as the real write, so
use it to confirm the runbook will be accepted.

## Do NOT hand off

- Something **you** can do (auth you already hold, a refactor, a test,
  a doc). Do it.
- Ambiguity about intent — ask a clarifying question instead.
- A blocker that's another tracked issue — record the dependency
  (`bd dep add <id> <blocker> --dolt-auto-commit=on`) and work
  elsewhere; don't hand a human a task they can't action yet.
