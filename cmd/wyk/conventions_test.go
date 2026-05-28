package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConventions_BodyMentionsTheCanonicalLabels(t *testing.T) {
	// The whole point of this subcommand is to surface the labels
	// to agents. Verify they're all present in the prose form so a
	// future "tighten this up" edit can't accidentally drop them.
	body := conventionsBody
	for _, want := range []string{"human", "src:agent", "wyk handoff", "wyk inbox", "label=src:agent AND NOT label=human"} {
		if !strings.Contains(body, want) {
			t.Errorf("conventionsBody missing %q\n---\n%s", want, body)
		}
	}
}

func TestConventions_StructuredHasFixedSchema(t *testing.T) {
	// The JSON form is the agent-facing structured ingest. Schema
	// drift breaks tools that index on these keys, so pin them.
	c := conventionsStructured()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"labels"`,
		`"human"`,
		`"src:agent"`,
		`"src:human"`,
		`"queries"`,
		`"human_tasks"`,
		`"agent_inbox"`,
		`"preferred_command"`,
		`"bd_create_example"`,
		`"contract_url"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("structured form missing key %s in:\n%s", want, s)
		}
	}
	if c.Queries.AgentInbox != "label=src:agent AND NOT label=human AND status!=closed" {
		t.Errorf("agent inbox query drifted: %q", c.Queries.AgentInbox)
	}
}

func TestConventions_RunDefaultIsText(t *testing.T) {
	if code := runConventions(nil); code != 0 {
		t.Errorf("runConventions(nil) = %d, want 0", code)
	}
}

func TestConventions_RunJSONExits0(t *testing.T) {
	if code := runConventions([]string{"-json"}); code != 0 {
		t.Errorf("runConventions(-json) = %d, want 0", code)
	}
}
