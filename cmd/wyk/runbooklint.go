package main

import (
	"errors"
	"strings"
)

// Runbook shapes. A human task is one of two things, and the runbook
// must say which by carrying the matching heading:
//
//   - a TASK — the human is asked to DO something. The runbook must carry
//     "## Steps": numbered directions the human follows without
//     re-deriving context.
//   - a QUESTION — the human is asked to ANSWER or DECIDE something. The
//     runbook must carry "## Question": the exact question, the options
//     considered, the agent's recommendation, and where to record the
//     answer.
//
// The headings are the literal markers `wyk handoff` checks for (see
// checkRunbookShape) and the ones `wyk conventions` / docs/CONTRACT.md
// document. Change them in one place.
const (
	runbookHeadingWhy      = "## Why this needs you (please confirm this is accurate)"
	runbookHeadingSteps    = "## Steps"
	runbookHeadingQuestion = "## Question"
	runbookHeadingUnblocks = "## What unblocks me when this returns"
)

// errRunbookShape is returned by checkRunbookShape when a non-empty
// runbook is neither a task (has "## Steps") nor a question (has
// "## Question"). The message is the whole remediation: it names both
// shapes and the template flags that print each skeleton.
var errRunbookShape = errors.New(
	"runbook has neither a \"" + runbookHeadingSteps + "\" nor a \"" + runbookHeadingQuestion + "\" section.\n" +
		"  A human task must say what kind it is:\n" +
		"    - asking the human to DO something?     add \"" + runbookHeadingSteps + "\" with numbered directions they can follow\n" +
		"    - asking the human to ANSWER/DECIDE?    add \"" + runbookHeadingQuestion + "\" with the exact question, options, and your recommendation\n" +
		"  Skeletons: wyk handoff -template   |   wyk handoff -template -question")

// checkRunbookShape enforces the contract's minimum structure on a
// runbook about to be handed to a human: it must contain a "## Steps"
// heading (a task with directions) or a "## Question" heading (a
// question for the human to answer). An empty runbook is not this
// function's concern — the caller gates that on -allow-empty.
//
// Matching is on the heading line, case-insensitive, tolerant of
// leading whitespace and of a trailing suffix ("## Steps (5 min)"),
// so a slightly-decorated heading still counts while a stray
// "steps" in prose does not.
func checkRunbookShape(runbook string) error {
	if strings.TrimSpace(runbook) == "" {
		return nil
	}
	if hasHeading(runbook, runbookHeadingSteps) || hasHeading(runbook, runbookHeadingQuestion) {
		return nil
	}
	return errRunbookShape
}

// hasHeading reports whether any line of body is a markdown heading
// that starts with want (e.g. "## Steps"), case-insensitively.
func hasHeading(body, want string) bool {
	want = strings.ToLower(want)
	for _, line := range strings.Split(body, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(line, want) {
			continue
		}
		// "## Stepsize" must not count; require the heading to end
		// there or be followed by a non-word character.
		rest := line[len(want):]
		if rest == "" || !isWordByte(rest[0]) {
			return true
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
