package main

import (
	"strings"
	"testing"
)

func TestCheckRunbookShape(t *testing.T) {
	cases := []struct {
		name    string
		runbook string
		wantOK  bool
	}{
		{"empty is the caller's concern", "", true},
		{"whitespace only", "  \n\t", true},
		{"task: Steps heading", "## Why this needs you\n...\n## Steps\n1. x\n## What unblocks me", true},
		{"question: Question heading", "## Why\n...\n## Question\nA or B?\n## What unblocks me", true},
		{"heading case-insensitive", "## steps\n1. x", true},
		{"heading with trailing decoration", "## Steps (5 min)\n1. x", true},
		{"heading with leading whitespace", "  ## Question\nA or B?", true},
		{"bare prose is rejected", "do the thing", false},
		{"numbered steps without the heading are rejected", "1. step one\n2. step two", false},
		{"other two sections but no middle", "## Why this needs you\n...\n## What unblocks me when this returns\n...", false},
		{"word 'steps' in prose does not count", "follow the steps in the wiki", false},
		{"Stepsize is not Steps", "## Stepsize\n1. x", false},
		{"question mark alone is not a Question section", "Should we use A or B?", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRunbookShape(tc.runbook)
			if tc.wantOK && err != nil {
				t.Errorf("checkRunbookShape(%q) = %v, want nil", tc.runbook, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("checkRunbookShape(%q) = nil, want error", tc.runbook)
			}
		})
	}
}

func TestCheckRunbookShape_ErrorNamesBothShapesAndTemplates(t *testing.T) {
	// The error IS the remediation: an agent that hits it must be able
	// to fix the runbook from the message alone.
	err := checkRunbookShape("do the thing")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		runbookHeadingSteps,
		runbookHeadingQuestion,
		"DO something",
		"ANSWER/DECIDE",
		"wyk handoff -template",
		"wyk handoff -template -question",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("shape error missing %q; got:\n%s", want, err)
		}
	}
}

func TestHandoff_RejectsRunbookWithoutStepsOrQuestion(t *testing.T) {
	// A "please do X" with no directions and no question is the exact
	// failure the shape check exists for. Refused before -dry-run, so
	// a dry run cannot vouch for a runbook the real write rejects.
	clearAmbientIdentity(t)
	path := writeRunbook(t, "Please rotate the staging DB password.")
	var code int
	stdout, stderr := captureOutErr(t, func() {
		code = runHandoff([]string{"-dry-run", "-create", "Rotate creds", "-file", path})
	})
	if code != 64 {
		t.Fatalf("exit = %d, want 64 (usage error)", code)
	}
	if strings.Contains(stdout, "DRY-RUN") {
		t.Errorf("dry-run must not print a plan for a rejected runbook; stdout:\n%s", stdout)
	}
	for _, want := range []string{"wyk handoff:", runbookHeadingSteps, runbookHeadingQuestion} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, stderr)
		}
	}
}

func TestHandoff_AcceptsQuestionShapedRunbook(t *testing.T) {
	clearAmbientIdentity(t)
	path := writeRunbook(t, "## Why this needs you (please confirm this is accurate)\nnot my call\n\n## Question\nAuth0 or Clerk?\n\n## What unblocks me when this returns\nthe choice in a note")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-create", "Which auth provider?", "-file", path}); code != 0 {
			t.Errorf("question-shaped runbook exit %d, want 0", code)
		}
	})
	if !strings.Contains(out, "DRY-RUN: no bd writes performed") {
		t.Errorf("expected dry-run plan; got:\n%s", out)
	}
}

func TestHandoff_TemplateQuestionPrintsQuestionSkeleton(t *testing.T) {
	clearAmbientIdentity(t)
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-template", "-question"}); code != 0 {
			t.Errorf("handoff -template -question exit %d, want 0", code)
		}
	})
	for _, want := range []string{
		runbookHeadingWhy,
		runbookHeadingQuestion,
		runbookHeadingUnblocks,
		"Recommendation:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("question template missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, runbookHeadingSteps) {
		t.Errorf("question template must not carry a Steps section; got:\n%s", out)
	}
	// Both skeletons must pass the shape check they exist to satisfy.
	if err := checkRunbookShape(out); err != nil {
		t.Errorf("question template fails its own shape check: %v", err)
	}
	if err := checkRunbookShape(handoffRunbookTemplate); err != nil {
		t.Errorf("task template fails its own shape check: %v", err)
	}
}

func TestHandoff_QuestionWithoutTemplateIsUsageError(t *testing.T) {
	// -question only selects a skeleton; the runbook itself declares
	// its shape via heading. Passing it on a real handoff is a
	// misunderstanding worth refusing loudly.
	clearAmbientIdentity(t)
	path := writeRunbook(t, "## Question\nA or B?")
	var code int
	_, stderr := captureOutErr(t, func() {
		code = runHandoff([]string{"-question", "-dry-run", "-file", path, "wyk-1"})
	})
	if code != 64 {
		t.Errorf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr, "-question only modifies -template") {
		t.Errorf("stderr should explain -question's scope; got %q", stderr)
	}
}
