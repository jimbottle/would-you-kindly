package main

import (
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// evilTitle carries an OSC 52 clipboard-write + a CSI run.
const evilTitle = "x\x1b]52;c;ZWdpdA==\x07\x1b[31m"

func TestInboxText_SanitizesTitle(t *testing.T) {
	out := captureRunStdout(t, func() int {
		renderInboxText([]beads.Issue{{ID: "a-1", Title: evilTitle, Repo: evilTitle}}, nil)
		return 0
	})
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("wyk inbox text output leaked a raw ESC from a hostile title/repo:\n%q", out)
	}
}

func TestDepNodeLabel_SanitizesTitle(t *testing.T) {
	got := depNodeLabel(depGraphNode{ID: "a-1", Title: evilTitle, Status: "open"})
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("depNodeLabel leaked a raw ESC from a hostile title: %q", got)
	}
}

func TestJoinRepoErrors_Sanitizes(t *testing.T) {
	got := joinRepoErrors([]repoError{
		{Repo: evilTitle, Error: "boom" + evilTitle},
		{Error: evilTitle},
	})
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("error footer leaked a raw ESC from a hostile repo/error: %q", got)
	}
}
