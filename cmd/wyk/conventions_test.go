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

func TestConventions_InboxRulePinned(t *testing.T) {
	// The "work inbox items now" imperative must be present in
	// both the structured form (agents may key on it) and the
	// prose body (readers learn the convention from the body
	// alone).
	c := conventionsStructured()
	if c.InboxRule == "" {
		t.Error("conventionsStructured.InboxRule must be non-empty")
	}
	for _, want := range []string{"wyk inbox", "work", "no longer blocking"} {
		if !strings.Contains(c.InboxRule, want) {
			t.Errorf("InboxRule missing %q; got %q", want, c.InboxRule)
		}
	}
	if !strings.Contains(conventionsBody, "Acting on the inbox") {
		t.Errorf("conventionsBody missing 'Acting on the inbox' heading")
	}
	// Case-insensitive on the imperative — the body shouts "WORK
	// them now" intentionally; future copy edits may lowercase.
	if !strings.Contains(strings.ToLower(conventionsBody), "work them now") {
		t.Errorf("conventionsBody missing the 'work them now' imperative")
	}
}

func TestConventions_StatusGuidancePinned(t *testing.T) {
	// Lifecycle order matters — agents may key on the index.
	// Pin both the status names and that deferred carries the
	// "WIP subsystem" guidance that triggered this contract bit.
	c := conventionsStructured()
	wantOrder := []string{"open", "in_progress", "blocked", "deferred", "closed"}
	if len(c.Statuses) != len(wantOrder) {
		t.Fatalf("expected %d statuses; got %d", len(wantOrder), len(c.Statuses))
	}
	for i, want := range wantOrder {
		if c.Statuses[i].Status != want {
			t.Errorf("statuses[%d].Status = %q, want %q", i, c.Statuses[i].Status, want)
		}
		if c.Statuses[i].When == "" {
			t.Errorf("statuses[%d] missing When guidance", i)
		}
	}
	// deferred specifically must mention the "not stabilised" /
	// "hidden from bd ready" framing — that's the rule the user
	// fed back to encode.
	deferred := c.Statuses[3]
	if !strings.Contains(deferred.When, "stabilis") || !strings.Contains(deferred.When, "ready") {
		t.Errorf("deferred guidance lost its core framing; got %q", deferred.When)
	}
}

func TestConventions_ProseBodyDocumentsStatusLifecycle(t *testing.T) {
	for _, want := range []string{
		"Status lifecycle",
		"open ",
		"in_progress",
		"blocked",
		"deferred",
		"closed ",
		"stabilised",
	} {
		if !strings.Contains(conventionsBody, want) {
			t.Errorf("conventionsBody missing lifecycle keyword %q", want)
		}
	}
}

func TestConventions_RunbookSectionsPinned(t *testing.T) {
	// Three required sections, in this order. Both the schema
	// shape and the literal headings matter — agent tooling
	// parses descriptions for these exact strings.
	c := conventionsStructured()
	wantHeadings := []string{
		"## Why this needs you (please confirm this is accurate)",
		"## Steps",
		"## What unblocks me when this returns",
	}
	if len(c.RunbookSections) != len(wantHeadings) {
		t.Fatalf("expected %d runbook sections; got %d", len(wantHeadings), len(c.RunbookSections))
	}
	for i, want := range wantHeadings {
		if c.RunbookSections[i].Heading != want {
			t.Errorf("section[%d].Heading = %q, want %q", i, c.RunbookSections[i].Heading, want)
		}
		if c.RunbookSections[i].Purpose == "" {
			t.Errorf("section[%d] missing Purpose", i)
		}
	}
}

func TestConventions_ProseBodyDocumentsRequiredSections(t *testing.T) {
	// The human-readable body MUST mention all three section
	// headings — that's how a reader learns the convention from
	// `wyk conventions` alone, without having to dig into the
	// skill file or CONTRACT.md.
	for _, want := range []string{
		"## Why this needs you (please confirm this is accurate)",
		"## Steps",
		"## What unblocks me when this returns",
	} {
		if !strings.Contains(conventionsBody, want) {
			t.Errorf("conventionsBody missing required section heading %q", want)
		}
	}
}

func TestConventions_QueriesAlignAcrossForms(t *testing.T) {
	// Both the prose body and the structured form now interpolate
	// the same agentInboxQuery / humanTasksQuery constants — pin
	// that they're literally the same string in both forms so a
	// future edit can't silently drift one side.
	c := conventionsStructured()
	if c.Queries.AgentInbox != agentInboxQuery {
		t.Errorf("structured agent_inbox = %q, want shared const %q", c.Queries.AgentInbox, agentInboxQuery)
	}
	if c.Queries.HumanTasks != humanTasksQuery {
		t.Errorf("structured human_tasks = %q, want shared const %q", c.Queries.HumanTasks, humanTasksQuery)
	}
	if !strings.Contains(conventionsBody, agentInboxQuery) {
		t.Errorf("prose form should embed the canonical agentInboxQuery (%q); got:\n%s", agentInboxQuery, conventionsBody)
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
