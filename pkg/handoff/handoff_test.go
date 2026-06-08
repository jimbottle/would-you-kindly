package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubMutator records both calls and lets a test inject an error
// at either step to verify the partial-failure contract.
type stubMutator struct {
	addedID, addedLabel string
	updatedID, updated  string
	addLabelErr         error
	updateErr           error

	updateCalled bool
}

func (s *stubMutator) AddLabel(_ context.Context, id, label string) error {
	s.addedID, s.addedLabel = id, label
	return s.addLabelErr
}

func (s *stubMutator) UpdateDescription(_ context.Context, id, desc string) error {
	s.updateCalled = true
	s.updatedID, s.updated = id, desc
	return s.updateErr
}

func TestBounceToHuman_TagsThenUpdates(t *testing.T) {
	s := &stubMutator{}
	err := BounceToHuman(context.Background(), s, "wyk-42", "step 1\nstep 2\nstep 3")
	if err != nil {
		t.Fatalf("BounceToHuman: %v", err)
	}
	if s.addedID != "wyk-42" || s.addedLabel != HumanLabel {
		t.Errorf("AddLabel: got id=%q label=%q, want id=wyk-42 label=%s",
			s.addedID, s.addedLabel, HumanLabel)
	}
	if s.updatedID != "wyk-42" || s.updated != "step 1\nstep 2\nstep 3" {
		t.Errorf("UpdateDescription mismatch: id=%q desc=%q", s.updatedID, s.updated)
	}
}

func TestBounceToHuman_LabelFailureDoesNotUpdate(t *testing.T) {
	// If tagging fails, we must NOT overwrite the description — the
	// issue would otherwise lose its previous content without the
	// human marker that signals the handoff.
	s := &stubMutator{addLabelErr: errors.New("bd: label add failed")}
	err := BounceToHuman(context.Background(), s, "wyk-42", "runbook")
	if err == nil {
		t.Fatal("expected error from label failure")
	}
	if s.updateCalled {
		t.Error("UpdateDescription must not be called when AddLabel fails")
	}
}

func TestBounceToHuman_UpdateFailureLeavesLabel(t *testing.T) {
	// If the description write fails after the label landed, the
	// issue stays flagged. Re-running BounceToHuman is the retry.
	s := &stubMutator{updateErr: errors.New("bd: timeout")}
	err := BounceToHuman(context.Background(), s, "wyk-42", "runbook")
	if err == nil {
		t.Fatal("expected error from update failure")
	}
	if s.addedLabel != HumanLabel {
		t.Errorf("label should have been applied; got %q", s.addedLabel)
	}
}

func TestBounceToHuman_EmptyRunbookAllowed(t *testing.T) {
	s := &stubMutator{}
	if err := BounceToHuman(context.Background(), s, "wyk-42", ""); err != nil {
		t.Fatalf("empty runbook should not error: %v", err)
	}
	if s.updated != "" {
		t.Errorf("expected empty description; got %q", s.updated)
	}
}

// readableStubMutator additionally implements descriptionReader, so
// BounceToHuman preserves a prior description (would-you-kindly-e2a8).
type readableStubMutator struct {
	stubMutator
	prior    string
	priorErr error
}

func (s *readableStubMutator) GetDescription(_ context.Context, id string) (string, error) {
	return s.prior, s.priorErr
}

func TestBounceToHuman_PreservesPriorDescription(t *testing.T) {
	s := &readableStubMutator{prior: "the agent's original working notes"}
	if err := BounceToHuman(context.Background(), s, "wyk-9", "## Steps\n1. do it"); err != nil {
		t.Fatalf("BounceToHuman: %v", err)
	}
	got := s.updated
	if !contains(got, "## Steps") {
		t.Errorf("runbook must stay at the top; got:\n%s", got)
	}
	if !contains(got, "## Prior description") || !contains(got, "the agent's original working notes") {
		t.Errorf("prior description must be preserved under a heading; got:\n%s", got)
	}
}

func TestBounceToHuman_NoPriorNoAppend(t *testing.T) {
	// Empty prior (e.g. a freshly -created issue) appends nothing.
	s := &readableStubMutator{prior: ""}
	if err := BounceToHuman(context.Background(), s, "wyk-9", "runbook"); err != nil {
		t.Fatal(err)
	}
	if s.updated != "runbook" {
		t.Errorf("empty prior should leave runbook untouched; got %q", s.updated)
	}
	// A read error also falls back to runbook-only.
	s2 := &readableStubMutator{priorErr: errors.New("boom")}
	if err := BounceToHuman(context.Background(), s2, "wyk-9", "runbook"); err != nil {
		t.Fatal(err)
	}
	if s2.updated != "runbook" {
		t.Errorf("read error should fall back to runbook-only; got %q", s2.updated)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestBounceToHuman_RetryIsIdempotent(t *testing.T) {
	// Regression for roborev #1845: handing off TWICE (the second call
	// reading back the first call's stored body as prior) must not
	// duplicate the runbook or the heading, and must keep the one
	// original — for both the with-prior and no-prior cases.
	t.Run("with prior original", func(t *testing.T) {
		orig := "the agent's original working notes"
		rb := "## Steps\n1. do it"
		s := &readableStubMutator{prior: orig}
		if err := BounceToHuman(context.Background(), s, "wyk-9", rb); err != nil {
			t.Fatal(err)
		}
		first := s.updated
		// Second handoff: feed the stored body back as prior.
		s.prior = first
		if err := BounceToHuman(context.Background(), s, "wyk-9", rb); err != nil {
			t.Fatal(err)
		}
		second := s.updated
		if second != first {
			t.Errorf("retry not idempotent:\n first=%q\nsecond=%q", first, second)
		}
		if n := strings.Count(second, priorDescriptionHeading); n != 1 {
			t.Errorf("heading appears %d times, want exactly 1:\n%s", n, second)
		}
		if n := strings.Count(second, orig); n != 1 {
			t.Errorf("original appears %d times, want exactly 1:\n%s", n, second)
		}
	})
	t.Run("updated runbook on retry keeps the one original", func(t *testing.T) {
		orig := "ORIGINAL"
		s := &readableStubMutator{prior: orig}
		_ = BounceToHuman(context.Background(), s, "wyk-9", "RB1")
		s.prior = s.updated
		_ = BounceToHuman(context.Background(), s, "wyk-9", "RB2")
		got := s.updated
		if !contains(got, "RB2") || contains(got, "RB1") {
			t.Errorf("retry should swap in the new runbook and drop the old; got:\n%s", got)
		}
		if n := strings.Count(got, priorDescriptionHeading); n != 1 || strings.Count(got, orig) != 1 {
			t.Errorf("heading/original must each appear once; got:\n%s", got)
		}
	})
	t.Run("no prior original stays a bare runbook across retries", func(t *testing.T) {
		s := &readableStubMutator{prior: ""}
		_ = BounceToHuman(context.Background(), s, "wyk-9", "RB")
		s.prior = s.updated // "RB"
		_ = BounceToHuman(context.Background(), s, "wyk-9", "RB")
		if s.updated != "RB" {
			t.Errorf("no-prior retry should stay %q; got %q", "RB", s.updated)
		}
	})
}
