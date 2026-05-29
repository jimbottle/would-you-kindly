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

## Status lifecycle: open vs deferred vs closed

A surprising number of "should I handoff?" calls turn out to be
"should I defer?" instead. The lifecycle:

- **open** — actionable now. The default for newly-filed issues.
- **in_progress** — someone has claimed it (`bd update --claim`).
- **blocked** — waiting on another tracked bd issue. Pair with
  `--add-dependency <id>` so the blocker is explicit and the
  dependency-closes-this-unblocks chain works.
- **deferred** — waiting on a subsystem that hasn't stabilised
  yet. Screenshots of a WIP UI, automation for an API still being
  redesigned, polish that depends on an unfinished feature. The
  task is real but prematurely actionable; deferring hides it
  from `bd ready` and the TUI's `ready` preset so the queue
  reflects what someone could actually start now.
- **closed** — done. The post-commit hook auto-closes from
  `Closes:`/`Fixes:`/`Resolves:` trailers.

Default to OPEN. Reach for **DEFERRED** instead of holding-open
when the blocker is "the rest of the project hasn't caught up
yet" — holding-open implies someone should do this now and
clutters the ready view. Reach for **BLOCKED** when the blocker
IS another tracked issue. Reach for **HANDOFF + human label**
(this skill) only when a human is genuinely required to do the
remaining work.

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

## Before you write the runbook: self-verify and own the claim

A handoff is not just a list of steps — it is a **claim** by you,
the agent, that the human is genuinely required AND a specification
of what you need back from them. Both have to be in the runbook in
words the human can read and push back on. Skip these and you ship
either (a) a handoff for something you could have done yourself, or
(b) a handoff that comes back without enough new state for you to
make progress.

Before you call `wyk handoff`, answer these out loud (literally —
write the answers into the runbook text):

1. **What did you try?** Name the specific commands, tools, or
   approaches you ran. "I tried X, X returned Y." If you can't
   name three concrete attempts, you have not earned the handoff
   yet — try those first.
2. **Where exactly did you hit the wall?** Identify the boundary
   you cannot cross — auth lives in a 1Password vault you can't
   read, the decision is policy/legal/spend authority you don't
   have, the action is a physical or third-party-UI click, etc.
3. **Why can you not work around it?** Briefly: why is there no
   alternative path that keeps the work on your side?
4. **What concrete artifact unblocks you when this returns?** A
   credential dropped at a known path, a URL pasted into a
   constant, a decision recorded in the description, a config
   value committed. If the answer is "I'll figure it out from
   notes," you don't yet know what you're asking for — sharpen it.

The first three live in a "**Why this needs you (please confirm
this is accurate)**" section at the top of the runbook. The last
lives in a "**What unblocks me when this returns**" section right
after the numbered steps. Both are explicit invitations for the
human to push back: if the agent overclaimed in #1–3, the human
sends it back with `H` and the issue lands in `wyk inbox` for the
agent to try harder. If #4 is wrong, the human says so and the
agent revises.

## How to use it

Two modes, depending on whether the bd issue already exists:

**Common case — you just decided this needs a human, no issue yet.**
File and hand off in one shot. Every runbook follows the same
shape:

```bash
cat <<'EOF' | wyk handoff -create "Rotate the staging DB password" -priority 1
## Why this needs you (please confirm this is accurate)

I cannot do this myself because <specific capability the agent lacks>.
What I tried: <three concrete attempts and what each returned>.
If you think I could have done this without you, send it back with
H and tell me what to try; I'll resume from the inbox.

## Steps

1. Open <where>.
2. <action>.
3. <action>.
4. <verification step>.
5. Close this issue when complete.

## What unblocks me when this returns

When this comes back closed (or via `H` for partial progress) I
expect to find: <concrete artifact — e.g. "the new password in
1Password vault X under key Y", "the URL pasted into
UNINSTALL_FEEDBACK_URL in src/constants.ts", "a one-line decision
recorded in this issue's description">. If that artifact is
missing, the next agent that picks this up cannot resume.
EOF
```

That single invocation runs `bd create` with `src:agent`, then
`pkg/handoff.BounceToHuman` to apply the `human` label and write the
runbook as the description. The output is the new issue ID.

**Alt — the issue already exists** (e.g. you've been working on it
and only now realised it needs a handoff):

```bash
cat runbook.md | wyk handoff <existing-issue-id>
```

`wyk handoff` reads the runbook from stdin (or `--file <path>`). It
exits 0 on success, 2 if bd is missing or there's no workspace, 1 on
other errors, and 64 on usage problems (empty runbook without
`-allow-empty`, TTY stdin, missing issue ID, both `-create` and a
positional ID).

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
## Why this needs you (please confirm this is accurate)

I cannot read or write the 1Password vault — credentials live in
your hardware-key-protected device. What I tried: `op vault list`
(no items returned because I'm not authenticated), `op item get
staging-postgres` (auth error), checking ~/.config/op for any
existing session token (none present). I have no path to the
secret without your authenticated session.

## Steps

1. Open 1Password vault 'Engineering / Staging' and locate
   'staging-postgres'.
2. Generate a new password (40 chars, alphanumeric + symbols).
3. In Heroku: heroku config:set DATABASE_PASSWORD=<new> -a wyk-staging
4. Update the 1Password entry with the new value.
5. Verify staging boots: curl -sS https://wyk-staging.example.com/healthz
6. Close this issue when complete.

## What unblocks me when this returns

I do not need the new password itself — the rotation is the work.
When this is closed I will treat the staging credential as rotated
and move on. If you'd like me to follow up (e.g. update a runbook
note or expire the OLD credential's audit log entry), drop a
`bd note <id> "<followup>"` and bounce it back with `H`.
EOF
```

### Good handoff: release sign-off

```bash
cat <<'EOF' | wyk handoff release-v0-3-0
## Why this needs you (please confirm this is accurate)

Publishing the release is a reputational/contractual decision I
don't have authority for — once the page goes live, it's visible
to customers and the version pin in our public release notes.
What I tried: confirming the changelog is accurate (it is — diff
against main matches), running the test suite green, and pushing
the tag. The 'Publish release' button click is the only step left.

## Steps

1. Open https://github.com/example/wyk/releases/tag/v0.3.0-rc1
2. Review the changelog entries; confirm no breaking changes are
   marked as patches.
3. Click 'Publish release' to promote the RC to GA.
4. Close this issue once the release page is live.

## What unblocks me when this returns

The release URL transitioning from /tag/v0.3.0-rc1 to /tag/v0.3.0
is the signal — when this issue closes I will update README's
install pin to v0.3.0 and announce.
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

## Picking up bounced-back work

At the start of a session, check whether any of your previous
handoffs were bounced back for follow-up:

```bash
wyk inbox -json
```

The output is a JSON array of issues you (an agent, per `src:agent`)
filed that the human has un-flagged with `H` — meaning they acted on
them and want you to do the next step. Re-flag with `human` if you
finish a chunk and need another round; close when fully done.

This closes the round-trip with `wyk handoff`: handoff sends work
to the human, inbox picks up what they sent back.

### Act on inbox items; do not just notice them

If `wyk inbox` returns any items, your default move is to **work
them now**, not to acknowledge them and continue with whatever
else is happening. The inbox by construction holds tasks the human
is no longer blocking on — the artifact you needed has arrived,
the decision is made, the credential is in place. Treating them as
"things to handle later" defeats the round-trip.

Exceptions:
- The user is mid-conversation about something explicitly urgent
  (a production fire, a release window closing).
- The inbox item's "What unblocks me when this returns" artifact
  is actually missing (the human bounced it back without
  finishing — re-flag `human` and explain in a note, don't just
  sit on it).
- The row renders as **HUMAN-BLOCK** in the TUI's `owner` column.
  HUMAN-BLOCK means the agent owns the task but one of its
  dependencies is a human-flagged task — the blocker isn't closed
  yet, so progress is literally impossible until the human acts
  on the dep. Skip to the next inbox item; revisit when the
  blocker closes.

If you're between explicit user requests and the inbox has items,
pick the highest-priority one and resume — that's the loop the
convention is designed to enable.

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
