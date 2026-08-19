package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// agentInboxQuery and humanTasksQuery are the canonical bd query
// expressions for the two convention-driven views. Kept as
// constants so the prose form (conventionsBody) and the structured
// form (conventionsStructured) interpolate the SAME string —
// previously the two forms duplicated the literal query text and
// could silently drift.
const agentInboxQuery = "label=src:agent AND NOT label=human AND NOT label=agent-handoff AND status!=closed AND status!=blocked AND status!=deferred AND status!=hooked"
const humanTasksQuery = "label=human AND status!=closed"

// conventionsBody is the agent-ready tip printed by `wyk conventions`.
// Kept as a package-level value so doctor.go (the Conventions stanza)
// can reference the same canonical text — drift between what doctor
// says and what `conventions` prints would itself be a discoverability
// failure. Surface from one place.
var conventionsBody = `bd / wyk task labels
====================

wyk filters task issues by these labels. Apply them when filing (with wyk create / bd create).

NOTE the create flag is --labels="a,b" (or -l), comma-separated. 'bd
create' has no --add-label: bd rejects it, prints its global-flags help,
and creates NOTHING -- and because that output reads as help rather than
an error, the issue silently never gets filed. (--add-label/--remove-label
DO exist on 'bd update' — for existing issues only.)

  - Tasks for a HUMAN    → --labels="human,src:agent"
                           (these surface in the TUI's 'h' view and in 'wyk --probe')
  - Tasks the AGENT owns → --labels="src:agent" only
  - Another AGENT's work → --labels="agent-handoff,src:agent" (badge AGENT-HANDOFF):
                           a different agent is working it; do NOT interfere,
                           a human orchestrates. Excluded from the inbox below.

The back-and-forth handshake: a human REMOVES the 'human' label when they're
done. The agent's inbox is then anything matching:

    ` + agentInboxQuery + `

…surfaced by 'wyk inbox' (-json for structured ingest).

By default 'wyk inbox' (and the other multi-repo commands: stats, activity,
dashboard, depgraph, export) query EVERY registered repo, so a human can
bounce work back from any checkout. Set 'wyk config set default_scope cwd'
to default instead to the repo containing the current directory; '-all' (or
'WYK_DEFAULT_SCOPE=all') restores the cross-repo view for a single run.

Per-identity routing (multi-agent workspaces, wyk-contract/v3)
--------------------------------------------------------------

When several agents share a workspace, the collective 'src:agent' inbox
would hand the same items to all of them. Route work to ONE agent by
ALSO applying 'src:agent:<name>' (layered on 'src:agent', which is never
removed): 'wyk handoff --identity <name>' (or '-identity' with -create),
or 'bd label add <id> src:agent:<name>'. That agent then scopes its
inbox with 'wyk inbox --identity <name>' (or the WYK_AGENT_IDENTITY env).
A named inbox shows that identity's routed work PLUS un-routed collective
work (so nothing filed without routing is stranded); add '-strict' for
routed-only. With no identity, everything behaves exactly as the
collective inbox above — single-agent workflows are unchanged.

Prefer 'wyk handoff <id>' over hand-rolling these labels — it applies the
right labels AND lets you attach a runbook from stdin in one shot.
'wyk handoff -create "<title>"' files a new bd issue and hands it off
atomically (with src:agent on creation), the recommended one-step path.

Acting on the inbox (not just noticing it)
------------------------------------------

If 'wyk inbox' returns items at any point in a session, the
default move is to WORK them now, not to acknowledge them and
continue elsewhere. The inbox by construction holds tasks where
the human is no longer blocking the agent — the artifact has
arrived, the decision is made. Treating them as 'things to handle
later' defeats the round-trip.

Exceptions:
  - The user is mid-conversation about something explicitly
    urgent.
  - The 'What unblocks me when this returns' artifact is actually
    missing (re-flag 'human' with a note explaining what's still
    needed; don't sit silently).
  - The row renders as HUMAN-BLOCK in the TUI — an agent task
    whose dependency set includes a human-labeled task. The
    blocker isn't closed yet, so the agent literally can't move
    this row forward. Skip to the next inbox item.

Status lifecycle (pick the right one when filing or updating)
-------------------------------------------------------------

bd (1.0.4) has seven built-in statuses; the convention is when to
use each:

  - open         actionable now; ready to work or to hand off.
  - in_progress  someone has claimed it. 'bd update --claim' is
                 the canonical way to set this; it also assigns.
  - hooked       attached to an agent's hook — bd's own in-flight
                 marker for hook-driven agent work. Treat like
                 in_progress belonging to someone else; excluded
                 from 'bd ready' and the wyk inbox.
  - blocked      waiting on another tracked bd issue. Use
                 '--add-dependency <other-id>' so the blocker is
                 explicit; the dependency closes → this unblocks.
  - deferred     waiting on a subsystem that hasn't stabilised
                 yet. Use this when the task is real but
                 prematurely actionable — screenshots of a WIP
                 UI, automation for an API still being redesigned,
                 etc. Deferred issues are hidden from 'bd ready'
                 and the TUI's 'ready' preset; they reappear when
                 you 'bd update --status open'.
  - pinned       persistent; stays open indefinitely (standing
                 instructions, recurring checklists). Never a work
                 queue entry — 'bd close' needs --force on it.
  - closed       done. The post-commit hook auto-closes from
                 'Closes:'/'Fixes:'/'Resolves:' trailers.

Default to OPEN. Reach for DEFERRED instead of holding-open when
the blocker is "the rest of the project hasn't caught up yet" —
holding-open implies someone should do this now and clutters the
ready view. Reach for BLOCKED when the blocker IS another tracked
issue.

The runbook structure (REQUIRED, not optional)
----------------------------------------------

A handoff is a claim by the agent that the human is genuinely required
AND a spec of what the agent needs back. Both have to be in the
runbook. Every handoff description includes three sections:

  ## Why this needs you (please confirm this is accurate)
      Two-line statement of (a) what the agent tried (three concrete
      attempts), (b) where it hit the wall, (c) why no workaround
      exists. Phrased as a CLAIM the human is asked to validate —
      if it's wrong, the human bounces back with H and the agent
      tries harder.

  ## Steps
      Numbered, concrete, with locations and verification.

  ## What unblocks me when this returns
      The artifact the agent expects to find when this comes back
      (credential at known path, URL in a constant, decision in
      the description). Without this the next agent that picks
      it up cannot resume.

Example: file a P1 human task directly with bd create
-----------------------------------------------------

    bd create --priority=1 --type=task \
        --labels="human,src:agent" \
        --title="<imperative>" \
        --description="$(cat <<'RUNBOOK'
    ## Why this needs you (please confirm this is accurate)
    I cannot <X> because <capability lacked>. What I tried: <three
    attempts>. No workaround because <reason>.

    ## Steps
    1. ...
    2. ...

    ## What unblocks me when this returns
    <concrete artifact>
    RUNBOOK
    )"

Full contract: https://github.com/jimbottle/would-you-kindly/blob/main/docs/CONTRACT.md
`

// conventionsJSON is the structured form for programmatic ingestion.
// Agents pipe `wyk conventions -json` into their tooling and can
// branch on the schema without parsing prose. Schema is intentionally
// stable: callers index by the exact keys here.
type conventionsJSON struct {
	Labels struct {
		Human        string `json:"human"`
		SrcAgent     string `json:"src:agent"`
		SrcHuman     string `json:"src:human"`
		AgentHandoff string `json:"agent-handoff"`
	} `json:"labels"`
	Queries struct {
		HumanTasks string `json:"human_tasks"`
		AgentInbox string `json:"agent_inbox"`
		// AgentInboxIdentity is the `-strict` per-identity inbox query
		// (wyk-contract/v3): substitute <name> for the identity. It is
		// layered on the collective src:agent umbrella, so collective
		// consumers are unaffected. NOTE: the DEFAULT `wyk inbox
		// --identity <name>` (without -strict) ALSO includes un-routed
		// collective work (the unclaimed sweep), which bd can't express as
		// a single query — wyk filters the collective set in Go.
		AgentInboxIdentity string `json:"agent_inbox_identity"`
	} `json:"queries"`
	// IdentityEnvVar names the env var that sets the ambient agent
	// identity for `wyk inbox` / `wyk handoff` when no -identity flag is
	// given (wyk-contract/v3).
	IdentityEnvVar   string           `json:"identity_env_var"`
	Statuses         []statusGuidance `json:"statuses"`
	InboxRule        string           `json:"inbox_rule"`
	PreferredCommand string           `json:"preferred_command"`
	BdCreateExample  string           `json:"bd_create_example"`
	RunbookSections  []runbookSection `json:"runbook_sections"`
	ContractURL      string           `json:"contract_url"`
}

// statusGuidance pairs a bd status name with a one-line rule for
// when it's the right choice. Agents consuming the JSON form can
// branch on this without parsing the prose.
type statusGuidance struct {
	Status string `json:"status"`
	When   string `json:"when"`
}

// runbookSection is one of the three required sections in a wyk
// handoff runbook. The Heading is the literal text the agent
// writes; Purpose is what the section is for (consumed by agent
// tooling, not rendered to the human).
type runbookSection struct {
	Heading string `json:"heading"`
	Purpose string `json:"purpose"`
}

func conventionsStructured() conventionsJSON {
	var c conventionsJSON
	c.Labels.Human = "task is for a human to act on; surfaced in TUI 'h' view and 'wyk --probe'"
	c.Labels.SrcAgent = "filed by an agent (provenance); persists across the back-and-forth"
	c.Labels.SrcHuman = "filed by a human (provenance); applied by the TUI's N quick-add and wyk handoff -create when stdin is absent"
	c.Labels.AgentHandoff = "another agent is working this task; THIS agent must not interfere (badge AGENT-HANDOFF). A human orchestrates the coordination; excluded from the agent inbox query"
	c.Queries.HumanTasks = humanTasksQuery
	c.Queries.AgentInbox = agentInboxQuery
	c.Queries.AgentInboxIdentity = inboxQueryFor("<name>")
	c.IdentityEnvVar = identityEnvVar
	c.InboxRule = "If `wyk inbox` returns items, work them now rather than acknowledging and moving on. The inbox holds tasks where the human is no longer blocking; treating them as 'handle later' defeats the round-trip. Exception: the user is mid-conversation about something explicitly urgent, or the expected unblocker artifact is missing (re-flag `human` and note, don't sit)."
	c.Statuses = []statusGuidance{
		{Status: "open", When: "actionable now; default for newly-filed issues"},
		{Status: "in_progress", When: "someone has claimed it; set via `bd update --claim` which also assigns"},
		{Status: "blocked", When: "waiting on another tracked bd issue; pair with `--add-dependency <id>`"},
		{Status: "deferred", When: "waiting on a subsystem that hasn't stabilised yet (WIP UI, redesigned API, etc.); hidden from `bd ready` and the TUI's `ready` preset"},
		{Status: "closed", When: "done; the post-commit hook auto-closes from `Closes:`/`Fixes:`/`Resolves:` trailers"},
	}
	c.PreferredCommand = "wyk handoff <id>   (or 'wyk handoff -create \"<title>\"' to file + hand off in one step)"
	c.BdCreateExample = `bd create --priority=1 --type=task --labels="human,src:agent" --title="<imperative>" --description="<runbook with required sections>"`
	c.RunbookSections = []runbookSection{
		{
			Heading: "## Why this needs you (please confirm this is accurate)",
			Purpose: "Agent's claim of self-verification. State (a) what was tried (three concrete attempts), (b) where the wall was hit, (c) why no workaround exists. The human is invited to push back by bouncing it back with H if the claim is wrong.",
		},
		{
			Heading: "## Steps",
			Purpose: "Numbered steps with concrete locations and a verification step. Last step is 'Close this issue when complete.'",
		},
		{
			Heading: "## What unblocks me when this returns",
			Purpose: "The concrete artifact the agent expects to find when the issue returns (credential at known path, URL in a constant, decision in the description). The next agent that picks up the bounce-back needs this to resume.",
		},
	}
	c.ContractURL = "https://github.com/jimbottle/would-you-kindly/blob/main/docs/CONTRACT.md"
	return c
}

// runConventions handles `wyk conventions` and `wyk conventions -json`.
// No bd workspace required — this is purely about the convention text,
// not project state. Always exits 0.
func runConventions(args []string) int {
	fs := flag.NewFlagSet("conventions", flag.ContinueOnError)
	fs.Usage = subcommandUsage(fs, "conventions")
	asJSON := fs.Bool("json", false, "emit a stable structured JSON schema instead of the human-readable block")
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+usageLine("conventions"))
		return 64
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(conventionsStructured()); err != nil {
			fmt.Fprintln(os.Stderr, "wyk conventions:", err)
			return 1
		}
		return 0
	}
	fmt.Print(conventionsBody)
	return 0
}
