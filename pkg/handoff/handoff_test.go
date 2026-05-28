package handoff

import (
	"context"
	"errors"
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
