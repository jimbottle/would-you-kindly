package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// conventionsBody is the agent-ready tip printed by `wyk conventions`.
// Kept as a package-level const so doctor.go (the Conventions stanza)
// can reference the same canonical text — drift between what doctor
// says and what `conventions` prints would itself be a discoverability
// failure. Surface from one place.
const conventionsBody = `bd / wyk task labels
====================

wyk filters task issues by two labels. Apply them when filing with bd create:

  - Tasks for a HUMAN    → --add-label="human" --add-label="src:agent"
                           (these surface in the TUI's 'h' view and in 'wyk --probe')
  - Tasks the AGENT owns → --add-label="src:agent" only

The back-and-forth handshake: a human REMOVES the 'human' label when they're
done. The agent's inbox is then anything matching:

    label=src:agent AND NOT label=human AND status!=closed

…surfaced by 'wyk inbox' (-json for structured ingest).

Prefer 'wyk handoff <id>' over hand-rolling these labels — it applies the
right labels AND lets you attach a runbook from stdin in one shot.
'wyk handoff -create "<title>"' files a new bd issue and hands it off
atomically (with src:agent on creation), the recommended one-step path.

Example: file a P1 human task directly with bd create
-----------------------------------------------------

    bd create --priority=1 --type=task \
        --add-label="human" --add-label="src:agent" \
        --title="<imperative>" --description="<numbered runbook steps>"

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
		HumanTasks  string `json:"human_tasks"`
		AgentInbox  string `json:"agent_inbox"`
	} `json:"queries"`
	PreferredCommand string `json:"preferred_command"`
	BdCreateExample  string `json:"bd_create_example"`
	ContractURL      string `json:"contract_url"`
}

func conventionsStructured() conventionsJSON {
	var c conventionsJSON
	c.Labels.Human = "task is for a human to act on; surfaced in TUI 'h' view and 'wyk --probe'"
	c.Labels.SrcAgent = "filed by an agent (provenance); persists across the back-and-forth"
	c.Labels.SrcHuman = "filed by a human (provenance); applied by the TUI's N quick-add and wyk handoff -create when stdin is absent"
	c.Queries.HumanTasks = "label=human"
	c.Queries.AgentInbox = "label=src:agent AND NOT label=human AND status!=closed"
	c.PreferredCommand = "wyk handoff <id>   (or 'wyk handoff -create \"<title>\"' to file + hand off in one step)"
	c.BdCreateExample = `bd create --priority=1 --type=task --add-label="human" --add-label="src:agent" --title="<imperative>" --description="<numbered runbook steps>"`
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
