# Human-in-the-loop contract

`would-you-kindly` (binary `wyk`) surfaces beads issues that an agent has handed
back to a human. The convention below uses only features bd already supports —
no schema changes, no parallel storage — so any bd CLI or compatible tool can
read and write it.

## The convention

A task is "for a human" when it carries the **`human`** label. That single
label is the only signal `wyk` requires.

Two supporting conventions complete the contract:

| Concern               | Encoding                                                                                                                                                |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Needs human action**| Label `human`. Set by an agent (or a person) the moment a task requires hands-on work that an agent cannot or should not perform.                       |
| **Instructions**      | The issue **description** holds the specific, ordered steps the human must perform. Treat it as a runbook the human can follow without further context.|
| **Who filed it**      | Label `src:agent` if an agent created the issue, `src:human` if a person did. New issues created through `wyk` or the agent skill always set one; pre-existing issues with no `src:` label are treated as unknown source.|

### Why these and not others

- **`human` label, not status.** bd's built-in statuses (`open`, `in_progress`,
  `blocked`, …) describe workflow state, not audience. A task can be `open` and
  for a human, or `open` and for an agent. Conflating audience with status
  would lose information.
- **Description, not notes.** `bd show --json` and `bd list --json` return
  `description` in their default payload; `notes` is a separate field intended
  for ongoing context. The description is the right place for the *single
  authoritative set of instructions*; notes accrete around it.
- **`src:` prefix on the source label.** Namespaced label prefixes are the
  ecosystem-standard way to encode small, controlled vocabularies in bd
  without inventing a custom field.

### Closing the loop

When the human finishes the task, they close the issue (`bd close <id>`,
or `c` in the TUI). If they cannot complete it and want to bounce it
back to the agent, they remove the `human` label (`bd label remove
<id> human`, or `H` in the TUI).

The agent discovers bounced-back work via **`wyk inbox`**:

```bash
wyk inbox          # human-readable list across every registered repo
wyk inbox -json    # structured output for LLM ingestion
```

`wyk inbox` runs the canonical query

```
label=src:agent AND NOT label=human AND status!=closed
```

across every registered workspace. The intent: an issue an agent
filed (`src:agent`) that no longer carries `human` and isn't closed
is sitting in the agent's lap — the human acted on it but left
follow-up work. The agent picks it up, either closes it (work is
done) or re-applies `human` after another step (back to the human
for another round). The label flips trace the conversation.

**Assumption: one agent per workspace.** The `src:agent` label is
collective, not per-identity. If two agents share a workspace
(e.g. Claude and another assistant running concurrently, or two
sessions of the same agent), they will both see — and may both act
on — the same inbox items. This contract version (`wyk-contract/v1`)
does not address that race. A future revision could introduce a
`src:agent:<name>` convention; until then, scope multi-agent
collaboration to separate workspaces (one bd workspace per agent
identity).

**Partial-failure visibility for `wyk inbox -json`.** When one
registered repo's bd is broken (moved, deleted, daemon unreachable),
`wyk inbox` silently omits its contribution and returns the union
from the working repos. The exit code is non-zero only if *every*
repo fails. An LLM consuming the JSON should treat an unexpectedly
empty inbox as a possible-failure signal rather than ground truth —
the silent-partial-failure policy matches the multi-repo TUI's
behaviour but is worth knowing.

### The agent's side: `pkg/handoff` and `wyk handoff`

When an agent decides to hand a task back, the canonical call is
[`pkg/handoff.BounceToHuman`](../pkg/handoff/handoff.go):

```go
import "github.com/jimbottle/would-you-kindly/pkg/handoff"

// c is any handoff.Mutator — beads.Client satisfies it directly.
err := handoff.BounceToHuman(ctx, c, "wyk-42", runbook)
```

For agents that aren't Go programs, `wyk handoff` exposes the same
operation at the CLI:

```bash
cat <<EOF | wyk handoff wyk-42
1. Open 1Password vault 'Engineering / Staging'.
2. Rotate the entry 'staging-postgres'.
3. Update Heroku config: heroku config:set …
EOF
```

Both routes tag the issue with `human` first, then overwrite its
description with the runbook. If the description write fails after
the label landed, the issue is left flagged with the previous
description — a retry preserves the flag, so the human can still
discover the handoff while the agent figures out the recovery.

**`-create` mode and the orphan policy.** `wyk handoff -create
"<title>"` runs `bd create` first, then `BounceToHuman` against the
new ID. The two steps are NOT transactional: if `bd create` succeeds
but `BounceToHuman` fails afterwards, the issue exists with the
`src:agent` label and the bd-default description, but without the
`human` label or the runbook. We deliberately do NOT auto-delete the
orphan — losing data on a transient bd hiccup is worse than the
orphan, and a recoverable orphan can be retried. The CLI prints an
explicit WARNING with the orphan ID and cleanup commands; agents
consuming the CLI's exit codes should check stderr too.

## Exact bd commands

### File a human-flagged task (agent)

```bash
bd create "Configure production OAuth client" \
  --description="$(cat <<'EOF'
1. Sign in to console.cloud.google.com as the prod service account.
2. Create an OAuth 2.0 client of type "Web application".
3. Add https://app.example.com/auth/callback to authorized redirect URIs.
4. Copy the client ID and secret into 1Password at "Prod / OAuth / Google".
5. Paste the client ID (only) into this issue's notes via `bd note`.
EOF
)" \
  --labels=human,src:agent \
  --priority=1
```

### File a human-flagged task (person, at the CLI)

```bash
bd create "Review the Q3 access audit before Friday" \
  --description="Open the audit at https://example.atlassian.net/wiki/Q3-access and confirm or revoke each row by Friday EOD." \
  --labels=human,src:human \
  --priority=1
```

### Filter to exactly the human-flagged work

The canonical query — used by `wyk`'s dedicated human-view keystroke — is:

```bash
bd query "label=human AND status!=closed" --json
```

Near-equivalent flag-based form (handy in shell pipelines):

```bash
bd list --label=human --status=open,in_progress,blocked --json
```

This omits `deferred` (and any future non-closed statuses bd may add),
so it can return a strict subset of the canonical query. `wyk` uses the
`bd query` form because it composes cleanly with future predicates
(e.g. `AND priority<=1`) and won't drift if bd adds new statuses.

### Discover handoffs from an agent specifically

```bash
bd query "label=human AND label=src:agent AND status!=closed" --json
```

### See what humans have filed for themselves

```bash
bd query "label=human AND label=src:human AND status!=closed" --json
```

## Versioning the contract

This document is the contract. If the labels or the field-mapping change,
bump the **Schema** line below and update `wyk`'s preset query strings in the
same commit.

**Schema:** `wyk-contract/v1`
