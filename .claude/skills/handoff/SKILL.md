---
name: handoff
description: Hand a task back to a human via the wyk handoff CLI. Use when you have completed everything you can do on a task but a remaining step requires human authority (auth/secrets, irreversible-or-political-or-legal decisions, physical access, third-party UI clicks) — NOT for tasks you could complete yourself but find tedious, NOT for ambiguity that asking a clarifying question would resolve, and NOT as a substitute for using AskUserQuestion to clarify intent.
---

# handoff

The single call to make when you have done what you can on a beads
issue and the remaining work genuinely requires a human.

This skill tags a bd issue with the `human` label and replaces its
description with the runbook the human will follow. The full
convention lives in `docs/CONTRACT.md`; this skill is the operational
shortcut.

## When this skill is the right move

Use it when **all** of these are true:

1. The task is tracked as a bd issue (or you are about to create one).
2. You have completed every step you reasonably can.
3. The remaining step requires a capability you cannot provide:
   - Authentication that lives in someone's 1Password / hardware key
   - A decision a human is accountable for (policy, legal, comms, money)
   - Physical or out-of-band action (cable-pull, badge swipe, paper signature)
   - A click in a third-party UI that doesn't expose an API
   - Review-and-publish on something with reputational weight (a release,
     a public statement, a customer-facing email)
4. The next agent that picks up this issue would benefit from a
   written runbook, not a back-and-forth.

## When NOT to use it

- **Clarifying questions.** If the blocker is "what should I do?",
  use `AskUserQuestion` instead. Handoff is for "I know what to do
  but cannot do it"; AskUserQuestion is for "I do not know what to
  do."
- **Tedious-but-doable tasks.** If you could complete the task with
  more time/attention/tool calls and the user reasonably expects
  you to, don't bounce it back. Handoff is not a way to opt out.
- **Quick, reversible operations the user could later undo.** A
  one-line config edit, a comment, a `bd close` — just do it.
- **Tasks not yet in bd.** First file the issue, then handoff. The
  human discovers handoffs via `bd query "label=human AND
  status!=closed"` — there must be an issue to find.
- **Internal investigation / reasoning steps.** Handoff is the end
  of your part of the work; not a place to dump partial thinking.

## How to use it

```bash
cat <<'EOF' | wyk handoff <issue-id>
1. Open <where>.
2. <action>.
3. <action>.
4. <verification step>.
5. Close this issue when complete.
EOF
```

`wyk handoff` reads the runbook from stdin (or `--file <path>`). It
exits 0 on success, 2 if bd is missing or there's no workspace, 1 on
other errors, and 64 on usage problems (empty runbook without
`-allow-empty`, TTY stdin, missing issue ID).

### Writing a good runbook

The runbook IS the handoff. A short fuzzy description gets ignored;
a runbook the human can follow without further context gets done.
Aim for:

- **Numbered steps**, in the order the human will perform them.
- **Concrete locations** (URLs, paths, vault entries, command lines).
- **A verification step** so the human knows when they're done.
- **A close instruction** at the bottom (`5. Close this issue when
  complete.`) — without it, the issue lingers as "ostensibly for a
  human" even after the work lands.
- **No agent jargon.** The human is the reader; they may not know
  what you tried or what got you stuck.

If you have context the runbook can't carry (transcripts, partial
work products, error messages), `bd note <id> "<text>"` is the
right place — append context separate from the canonical runbook
instructions.

## Heuristics: is this really for a human?

Before invoking, sanity-check yourself:

- "Could I do this if I tried again?" → If yes, try again.
- "Would a clarifying question unblock me?" → If yes, ask.
- "Is the human action <30 seconds?" → If yes, the runbook is the
  whole work; make sure it's tight.
- "Is the issue properly in bd?" → If no, file it first.

If you've answered no, no, no, no — proceed to handoff.

## Examples

### Good handoff: secret rotation requires 1Password access

```bash
# Issue wyk-staging-rotate already exists.
cat <<'EOF' | wyk handoff wyk-staging-rotate
1. Open 1Password vault 'Engineering / Staging' and locate
   'staging-postgres'.
2. Generate a new password (40 chars, alphanumeric + symbols).
3. In Heroku: heroku config:set DATABASE_PASSWORD=<new> -a wyk-staging
4. Update the 1Password entry with the new value.
5. Verify staging boots: curl -sS https://wyk-staging.example.com/healthz
6. Close this issue when complete.
EOF
```

### Good handoff: release sign-off

```bash
cat <<'EOF' | wyk handoff release-v0-3-0
1. Open https://github.com/example/wyk/releases/tag/v0.3.0-rc1
2. Review the changelog entries; confirm no breaking changes are
   marked as patches.
3. Click 'Publish release' to promote the RC to GA.
4. Close this issue once the release page is live.
EOF
```

### Bad handoff (don't do this): clarification

```bash
# WRONG — this is a clarifying question, not a handoff.
cat <<'EOF' | wyk handoff some-issue
What database column should I use for the user's locale?
EOF
```

**Fix:** Use `AskUserQuestion` to ask the question. Only handoff once
you know the answer and the remaining work needs human authority.

### Bad handoff (don't do this): "I gave up"

```bash
# WRONG — this is tedious-but-doable, not human-only.
cat <<'EOF' | wyk handoff long-refactor
The 47 callsites of the old API need to be updated.
EOF
```

**Fix:** Update the callsites yourself. Handoff is not a way to opt
out of work.

## Discovering existing handoffs

To see what's currently flagged for a human:

```bash
bd query "label=human AND status!=closed" --json
```

To see specifically what agents have handed back (vs. what humans
filed themselves):

```bash
bd query "label=human AND label=src:agent AND status!=closed" --json
```

The `wyk` TUI's `h` keystroke jumps straight to this view.

## Closing the loop

When the human finishes the work, they close the issue (`bd close
<id>` or `c` in the TUI). If they can't complete it and want to
bounce it back to the agent, they remove the `human` label (`bd
label remove <id> human` or `H` in the TUI). The agent then
discovers the un-labeled issue and resumes.

## See also

- `docs/CONTRACT.md` — the full convention this skill implements.
- `pkg/handoff.BounceToHuman` — Go entry point for in-process callers.
- `wyk` (the TUI) — the human-facing reader of these handoffs.
