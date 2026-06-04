# Design note: per-identity agent inbox (`src:agent:<name>`)

Status: **agreed** — design-first; key forks signed off (see "Agreed decisions").
Tracks: `would-you-kindly-zy66`. Targets contract schema **`wyk-contract/v3`**.

## Agreed decisions

1. **Scope: both phases.** Ship phase-1 strict scoping, then phase-2 the
   unclaimed-sweep. bd 1.0.4 was probed and does **not** support wildcard /
   negated-prefix label matching (`label=src:agent:*` → `parsing query:
   unexpected character '*'`), so phase 2 is implemented **client-side**: fetch
   `label=src:agent`, then in Go keep rows that either carry `src:agent:<name>`
   or carry no `src:agent:` sublabel at all. `beads.Issue.Labels []string` is
   exposed, so the filter is straightforward.
2. **Env var: `WYK_AGENT_IDENTITY`** (matches the existing `WYK_*` prefix).
3. **Keep both mechanisms, orthogonal.** Identity routing = "this is A's";
   `agent-handoff` = "nobody auto-works this, a human coordinates." They layer.

## Problem

The handoff contract (`docs/CONTRACT.md`, schema v2) assumes **one agent per
workspace**. The `src:agent` label is *collective*: it means "an agent filed
this," not "*this* agent filed this." So when two agents share a workspace —
Claude plus another assistant, or two concurrent sessions of the same agent —
they both match the inbox query

```
label=src:agent AND NOT label=human AND NOT label=agent-handoff AND status!=closed
```

and both see, and may both act on, the same bounced-back items. The v2
`agent-handoff` label is only a *partial* fence: it excludes a task from
*everyone's* inbox so a human can orchestrate, but it does not **route** a task
to a specific agent. The contract's documented fallback today is "scope
multi-agent work to separate workspaces."

This note proposes an optional identity layer that routes inbox items per agent
while keeping every existing single-agent workflow and every existing query
byte-for-byte unchanged.

## Goals / non-goals

**Goals**
- Route an inbox item to a named agent identity.
- 100% backward compatible: existing `src:agent` tasks, existing queries, and
  the default (no-identity) `wyk inbox` behave exactly as in v2.
- No bd schema change — labels only, same as v1/v2.
- Expressible in bd's query grammar (no feature we don't already rely on).

**Non-goals**
- wyk arbitrating *which* agent does what. As with `agent-handoff`, a human
  (or an external orchestrator) assigns identities; wyk only reads them.
- A registry of known identities, presence/liveness, or locking. Identity is a
  free-form slug, validated for shape only.

## Core decision: layer, don't replace

An identity-routed task carries **both** labels:

```
src:agent            ← the collective umbrella (unchanged meaning)
src:agent:<name>     ← the identity routing tag (new)
```

`src:agent` is **never removed** when an identity tag is added. This is the
load-bearing choice and it buys all the back-compat:

- The collective inbox query (`label=src:agent …`) still matches identity-routed
  tasks — nothing regresses for an agent that ignores identities.
- Every place that keys off exactly `src:agent` — `cmd/wyk/stats.go`
  (`HasLabel("src:agent")`), the TUI owner-column logic in
  `internal/tui/source.go`, `beads.Issue.IsHuman`/`IsAgentFiled` — keeps working
  with no change, because the umbrella label is still present.

Rejected alternative: `src:agent:<name>` *replaces* `src:agent`. This breaks
the collective query and every consumer above, and would make "is this agent
work at all?" require a prefix match instead of an equality check. Not worth it.

### Identity slug grammar

`<name>` matches `[a-z0-9][a-z0-9-]*` — lowercase, digits, hyphens. No colon
(labels are colon-namespaced, so a colon in the identity is ambiguous), no
spaces, no uppercase (avoids case-folding surprises across tools). wyk
validates the slug wherever it accepts one and rejects bad shapes with a clear
error rather than silently filing an unfindable label.

## Identity resolution (reading the inbox)

`wyk inbox` gains one identity input, resolved in this precedence:

1. `--identity <name>` flag (explicit, highest).
2. `WYK_AGENT_IDENTITY` env var (ambient — set once per agent session; verified
   not to collide with existing `WYK_*` vars: only `WYK_ALLOW_BD_CREATE` and
   `WYK_NO_COLOR` exist today).
3. **Unset → today's collective behavior.** No identity means the exact v2
   query. This is what makes the change invisible to single-agent users.

## Inbox query semantics

The hard question: when identity `foo` reads its inbox, should it see only its
own routed tasks, or also *unrouted* collective `src:agent` tasks (filed before
identities existed, or filed without routing)?

Two phases:

**Phase 1 (recommended to ship first) — strict scoping.** With an identity, the
query is plain label equality:

```
label=src:agent:<name> AND NOT label=human AND NOT label=agent-handoff AND status!=closed
```

This is trivially expressible, needs no negated-prefix support, and gives each
agent a clean private lane. The tradeoff: an identity inbox does **not** show
un-routed collective work. We mitigate with documentation — "to sweep unrouted
work, run `wyk inbox` with no identity" — and optionally a `--include-unclaimed`
flag in phase 2.

**Phase 2 (agreed) — unclaimed sweep.** The named inbox also unions in
collective tasks that carry no identity sublabel, so un-routed agent work isn't
stranded. The natural query —

```
(label=src:agent:<name>) OR (label=src:agent AND NOT label=src:agent:*)
```

— is **not expressible in bd 1.0.4**: it rejects the `*` wildcard
(`parsing query: unexpected character '*'`). So phase 2 is implemented
**client-side**:

1. Fetch the collective set: `label=src:agent AND NOT label=human AND NOT
   label=agent-handoff AND status!=closed`.
2. In Go, keep a row when it has `src:agent:<name>` **or** has no `src:agent:`
   sublabel at all (i.e. drop only rows routed to a *different* identity).

`beads.Issue.Labels []string` is already exposed, so the filter is a simple
prefix scan. The cost is a slightly wider fetch (the whole collective set rather
than a label-equality slice), which is acceptable at inbox sizes. If a future
bd gains prefix/wildcard matching, this can collapse back to a single query.

## Filing / routing identity work

Routing is additive and goes through the existing surfaces:

- `wyk handoff <id> --identity <name>` and `wyk handoff -create "<title>"
  --identity <name>` add `src:agent:<name>` alongside the `src:agent` they
  already apply. (Handoff to a *human* is unchanged — that's the `human` label.)
- A bare re-route of an existing task: `bd label add <id> src:agent:<name>
  --dolt-auto-commit=on` (the collective `src:agent` stays).
- Un-route: remove the identity sublabel; the task falls back to collective.

The contract invariant to document: **adding an identity tag never removes
`src:agent`.**

## Interaction with `agent-handoff`

`agent-handoff` remains the explicit "no agent should auto-grab this; a human
coordinates" fence, and stays excluded from every inbox (collective and
identity-scoped). It is now *partly* redundant with identity routing — routing a
task to agent A already keeps it out of agent B's strict inbox — but it stays as
the orthogonal, stronger signal: identity routing says "this is A's," while
`agent-handoff` says "nobody auto-works this yet." Both are kept; the doc
explains when to reach for which.

## Surfaces to update (single change, kept in lockstep)

The v2 → v3 bump must touch all of these together (the project already treats
inbox-query drift across these files as a discoverability bug):

- `docs/CONTRACT.md` — new section, bump the `**Schema:**` line to
  `wyk-contract/v3`, add a v3 changelog entry.
- `cmd/wyk/inbox.go` — `inboxQuery` const + the `--identity` flag + resolution.
- `cmd/wyk/conventions.go` — `agentInboxQuery` + `conventionsBody` prose.
- `cmd/wyk/doctor.go` — the Conventions stanza strings.
- `cmd/wyk/init.go` — `rememberedConventionMemory` string.
- `cmd/wyk/stats.go` — optional per-identity inbox breakdown (additive).
- `cmd/wyk/clidocs.go` — `wyk inbox` flag docs (feeds `docs/generated/cli.md`;
  run the docs-check / regen so `make check`'s docs guard stays green).
- The plugin-skills drift guard (`skills_drift_test.go`) if the convention text
  it mirrors changes.

## Backward-compatibility summary

| Concern | v2 today | After v3 |
| --- | --- | --- |
| Existing `src:agent`-only tasks | in collective inbox | unchanged in collective inbox; in an identity inbox only under phase-2 unclaimed-sweep |
| `wyk inbox` with no identity | collective | **identical** (collective) |
| Tooling keyed on `HasLabel("src:agent")` | works | works (umbrella label preserved) |
| bd storage / schema | labels only | labels only (no change) |

## Acceptance (for `would-you-kindly-zy66`)

A design is agreed for per-identity inbox scoping that is backward-compatible
with collective `src:agent`. Implementation follows as two separate tracked
tasks: **phase 1** (strict scoping: the `--identity`/`WYK_AGENT_IDENTITY`
plumbing, slug validation, the equality query, routing via `wyk handoff
--identity`, and the v3 contract bump across all surfaces) and **phase 2** (the
client-side unclaimed-sweep, layered on phase 1).
