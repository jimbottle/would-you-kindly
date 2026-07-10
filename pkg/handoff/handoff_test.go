package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubMutator records the calls and lets a test inject an error at
// either step to verify the partial-failure contract. addedLabels is a
// SLICE now that BounceToHuman applies more than one label (human then
// src:agent), and records only labels that were applied successfully.
type stubMutator struct {
	addedID     string
	addedLabels []string
	updatedID   string
	updated     string
	addLabelErr error  // fails EVERY AddLabel (the first call, i.e. human)
	failOnLabel string // fails only this specific label's AddLabel
	updateErr   error

	updateCalled bool
}

func (s *stubMutator) AddLabel(_ context.Context, id, label string) error {
	s.addedID = id
	if s.addLabelErr != nil {
		return s.addLabelErr
	}
	if s.failOnLabel != "" && label == s.failOnLabel {
		return errors.New("bd: label add failed: " + label)
	}
	s.addedLabels = append(s.addedLabels, label)
	return nil
}

func (s *stubMutator) lastAdded() string {
	if len(s.addedLabels) == 0 {
		return ""
	}
	return s.addedLabels[len(s.addedLabels)-1]
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
	// Both provenance labels are applied, human FIRST (so a concurrent
	// reader sees the human flag before the description overwrite begins),
	// then the collective src:agent so a bounced-back issue matches
	// `wyk inbox` (would-you-kindly-voef).
	want := []string{HumanLabel, SrcAgentLabel}
	if s.addedID != "wyk-42" || len(s.addedLabels) != len(want) {
		t.Fatalf("AddLabel: got id=%q labels=%v, want id=wyk-42 labels=%v",
			s.addedID, s.addedLabels, want)
	}
	for i := range want {
		if s.addedLabels[i] != want[i] {
			t.Errorf("addedLabels[%d] = %q, want %q", i, s.addedLabels[i], want[i])
		}
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
	// The very first AddLabel (human) failed, so we must short-circuit
	// before attempting src:agent — no label was successfully applied.
	if len(s.addedLabels) != 0 {
		t.Errorf("no labels should have been applied after the first AddLabel failed; got %v", s.addedLabels)
	}
}

// TestBounceToHuman_SrcAgentFailureLeavesHumanNoUpdate pins the
// intermediate failure state introduced by the second AddLabel (roborev
// on would-you-kindly-voef): human applied, src:agent failed, description
// NOT yet overwritten — and a retry then completes cleanly.
func TestBounceToHuman_SrcAgentFailureLeavesHumanNoUpdate(t *testing.T) {
	s := &stubMutator{failOnLabel: SrcAgentLabel}
	err := BounceToHuman(context.Background(), s, "wyk-42", "runbook")
	if err == nil {
		t.Fatal("expected error when the src:agent AddLabel fails")
	}
	if s.updateCalled {
		t.Error("UpdateDescription must not run when src:agent add fails — the old description must be preserved")
	}
	if len(s.addedLabels) != 1 || s.addedLabels[0] != HumanLabel {
		t.Errorf("human should be applied but not src:agent; got %v", s.addedLabels)
	}

	// Retry with the failure cleared: idempotent re-issue completes — both
	// labels applied (human again, per bd's idempotency) and the
	// description finally written.
	s.failOnLabel = ""
	if err := BounceToHuman(context.Background(), s, "wyk-42", "runbook"); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if !s.updateCalled {
		t.Error("retry should write the description")
	}
	if s.lastAdded() != SrcAgentLabel {
		t.Errorf("retry should end with src:agent applied; got %v", s.addedLabels)
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
	if s.lastAdded() != SrcAgentLabel {
		t.Errorf("both labels should have been applied before the update; got %v", s.addedLabels)
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

func TestBounceToHuman_RetryChangedRunbookNoOriginal(t *testing.T) {
	// Documented behavior for the ambiguous case (roborev #1846 finding 2):
	// an issue with NO original description, handed off as RB1, then
	// re-handed-off as a CHANGED runbook RB2. The previous runbook is
	// preserved under the heading rather than dropped (a bare prior is
	// indistinguishable from a real original, so we keep the trail).
	s := &readableStubMutator{prior: ""}
	_ = BounceToHuman(context.Background(), s, "wyk-9", "RB1") // stores "RB1"
	s.prior = s.updated
	_ = BounceToHuman(context.Background(), s, "wyk-9", "RB2")
	got := s.updated
	want := "RB2\n\n" + priorDescriptionHeading + "\n\nRB1"
	if got != want {
		t.Errorf("changed-runbook/no-original retry:\n got=%q\nwant=%q", got, want)
	}
	// And a THIRD handoff (RB3) must stay idempotent on the marker, not
	// nest a second one — RB1 stays the single preserved body.
	s.prior = got
	_ = BounceToHuman(context.Background(), s, "wyk-9", "RB3")
	if n := strings.Count(s.updated, priorDescriptionHeading); n != 1 {
		t.Errorf("third handoff must keep exactly one heading; got %d:\n%s", n, s.updated)
	}
	if !contains(s.updated, "RB3") || !contains(s.updated, "RB1") || contains(s.updated, "RB2") {
		t.Errorf("third handoff should be RB3 + preserved RB1 (RB2 gone); got:\n%s", s.updated)
	}
}
