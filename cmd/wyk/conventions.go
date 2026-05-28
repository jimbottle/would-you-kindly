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
const agentInboxQuery = "label=src:agent AND NOT label=human AND status!=closed"
const humanTasksQuery = "label=human AND status!=closed"

// conventionsBody is the agent-ready tip printed by `wyk conventions`.
// Kept as a package-level value so doctor.go (the Conventions stanza)
// can reference the same canonical text — drift between what doctor
// says and what `conventions` prints would itself be a discoverability
// failure. Surface from one place.
var conventionsBody = `bd / wyk task labels
====================

wyk filters task issues by two labels. Apply them when filing with bd create:

  - Tasks for a HUMAN    → --add-label="human" --add-label="src:agent"
                           (these surface in the TUI's 'h' view and in 'wyk --probe')
  - Tasks the AGENT owns → --add-label="src:agent" only

The back-and-forth handshake: a human REMOVES the 'human' label when they're
done. The agent's inbox is then anything matching:

    ` + agentInboxQuery + `

…surfaced by 'wyk inbox' (-json for structured ingest).

Prefer 'wyk handoff <id>' over hand-rolling these labels — it applies the
right labels AND lets you attach a runbook from stdin in one shot.
'wyk handoff -create "<title>"' files a new bd issue and hands it off
atomically (with src:agent on creation), the recommended one-step path.

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
        --add-label="human" --add-label="src:agent" \
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
		Human    string `json:"human"`
		SrcAgent string `json:"src:agent"`
		SrcHuman string `json:"src:human"`
	} `json:"labels"`
	Queries struct {
		HumanTasks string `json:"human_tasks"`
		AgentInbox string `json:"agent_inbox"`
	} `json:"queries"`
	PreferredCommand string           `json:"preferred_command"`
	BdCreateExample  string           `json:"bd_create_example"`
	RunbookSections  []runbookSection `json:"runbook_sections"`
	ContractURL      string           `json:"contract_url"`
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
	c.Queries.HumanTasks = humanTasksQuery
	c.Queries.AgentInbox = agentInboxQuery
	c.PreferredCommand = "wyk handoff <id>   (or 'wyk handoff -create \"<title>\"' to file + hand off in one step)"
	c.BdCreateExample = `bd create --priority=1 --type=task --add-label="human" --add-label="src:agent" --title="<imperative>" --description="<runbook with required sections>"`
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
	asJSON := fs.Bool("json", false, "emit a stable structured JSON schema instead of the human-readable block")
	if err := fs.Parse(args); err != nil {
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
