package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/pkg/handoff"
)

// This file is the writer-vs-reader contract test (would-you-kindly-voef).
// The bug it guards against: wyk's filing paths (the WRITERS) and the
// `wyk inbox` query (the READER) were each tested in isolation, so a
// filing path that produced a label set the inbox query could never match
// shipped unnoticed. Here every writer's resulting labels are checked
// against the reader's requirements after a simulated human bounce-back.

// matchesAgentInbox is the label/status predicate form of inboxQuery
// (cmd/wyk/inbox.go): `label=src:agent AND NOT label=human AND NOT
// label=agent-handoff AND status!=closed AND status!=blocked AND
// status!=deferred AND status!=hooked`. Kept in sync with that
// constant by TestInboxQueryMatchesPredicate below.
func matchesAgentInbox(labels []string, status string) bool {
	has := func(want string) bool {
		for _, l := range labels {
			if l == want {
				return true
			}
		}
		return false
	}
	switch {
	case !has("src:agent"):
		return false
	case has("human"), has("agent-handoff"):
		return false
	case status == "closed", status == "blocked", status == "deferred", status == "hooked":
		return false
	default:
		return true
	}
}

// simulateBounce models a human returning work to the agent: they remove
// the `human` label (the documented back-to-you gesture).
func simulateBounce(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l != "human" {
			out = append(out, l)
		}
	}
	return out
}

type recordMutator struct{ labels []string }

func (m *recordMutator) AddLabel(_ context.Context, _, label string) error {
	m.labels = append(m.labels, label)
	return nil
}
func (m *recordMutator) UpdateDescription(_ context.Context, _, _ string) error { return nil }

func TestFilingPathsSatisfyInboxAfterBounce(t *testing.T) {
	// Path 1: `wyk create` inside a Claude session → agent-filed work.
	t.Run("wyk create (agent session)", func(t *testing.T) {
		t.Setenv(sessionEnvVar, "sess-abc")
		cap := withStubCreate(t, "x-1", nil)
		if code := runCreate([]string{"--title", "t"}); code != 0 {
			t.Fatalf("runCreate exit %d", code)
		}
		if !matchesAgentInbox(simulateBounce(cap.labels), "open") {
			t.Fatalf("wyk create labels %v do not satisfy `wyk inbox` after a bounce-back", cap.labels)
		}
	})

	// Path 2 (label-writing half) + Path 3: bare-id handoff via
	// BounceToHuman. This is also the tail of `wyk handoff -create`, whose
	// createLabels are separately seeded with src:agent (main.go).
	t.Run("wyk handoff <id> (BounceToHuman)", func(t *testing.T) {
		rec := &recordMutator{}
		if err := handoff.BounceToHuman(context.Background(), rec, "x-2", "runbook"); err != nil {
			t.Fatalf("BounceToHuman: %v", err)
		}
		// While handed off (still carries `human`) it must NOT be in the inbox.
		if matchesAgentInbox(rec.labels, "open") {
			t.Fatalf("a still-handed-off issue (labels %v) must not appear in the agent inbox", rec.labels)
		}
		// After the human bounces it back, it MUST appear.
		if !matchesAgentInbox(simulateBounce(rec.labels), "open") {
			t.Fatalf("bounced-back handoff labels %v do not satisfy `wyk inbox`", rec.labels)
		}
	})

	// Reader-side negative: a human-filed `wyk create` (no session) is
	// src:human and must stay OUT of the agent inbox.
	t.Run("wyk create (no session → src:human) stays out", func(t *testing.T) {
		t.Setenv(sessionEnvVar, "")
		cap := withStubCreate(t, "x-3", nil)
		if code := runCreate([]string{"--title", "t"}); code != 0 {
			t.Fatalf("runCreate exit %d", code)
		}
		if matchesAgentInbox(simulateBounce(cap.labels), "open") {
			t.Fatalf("human-filed labels %v must not match the agent inbox", cap.labels)
		}
	})
}

// TestInboxQueryMatchesPredicate ties matchesAgentInbox to the real
// inboxQuery string: if a clause is added/removed from the query, this
// fails until the predicate (and the contract test above) is updated,
// preventing the writer/reader guard from silently drifting.
func TestInboxQueryMatchesPredicate(t *testing.T) {
	for _, clause := range []string{
		"label=src:agent",
		"NOT label=human",
		"NOT label=agent-handoff",
		"status!=closed",
		"status!=blocked",
		"status!=deferred",
		"status!=hooked",
	} {
		if !strings.Contains(inboxQuery, clause) {
			t.Errorf("inboxQuery no longer contains %q — update matchesAgentInbox + the contract test to match", clause)
		}
	}
}
