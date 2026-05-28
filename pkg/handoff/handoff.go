// Package handoff is the single call an agent makes when it wants to
// hand a beads issue back to a human. It is intentionally tiny — the
// public API is one function and one interface — so an agent tool can
// import it without dragging in the TUI or any other heavy
// dependencies.
//
// The convention this implements lives in docs/CONTRACT.md:
//
//   - The issue is tagged with the `human` label (the canonical
//     human-view query keys off this label).
//   - The description is replaced with the runbook the human will
//     follow. Treat the runbook as the single source of truth for
//     the handoff — clear and complete enough that the human can act
//     without further context.
//
// The agent skill is the "writer" half of the contract; the wyk TUI
// is the "reader" half.
package handoff

import "context"

// Mutator is the subset of a bd client that the handoff package
// needs. internal/beads.Client satisfies it directly, and tests can
// substitute a stub. Keeping the interface small means callers don't
// have to take a dependency on the full bd Client just to make a
// handoff.
type Mutator interface {
	AddLabel(ctx context.Context, id, label string) error
	UpdateDescription(ctx context.Context, id, description string) error
}

// HumanLabel is the label this package writes. Exported as a constant
// so external tools that need to query for "anything handed to a
// human" can use the same string the writer used.
const HumanLabel = "human"

// BounceToHuman hands the named issue back to a human:
//
//  1. Tags the issue with the `human` label.
//  2. Replaces the description with the supplied runbook.
//
// Order matters: the label is applied first so a concurrent reader
// (e.g. a polling TUI) sees the issue as human-flagged the moment
// the description-overwrite begins. If the description write fails,
// the issue is left flagged with its previous description rather
// than silently un-flagged — the agent (or the human) can re-issue
// the call to retry without losing the flag.
//
// The retry story depends on `bd label add` being idempotent —
// adding an already-present label must succeed, not error. This is
// the documented behavior of the bd version this project pins
// (verified against bd 1.0.4: a second `bd label add <id> <label>`
// returns exit 0 with the same "✓ Added label" message). If a future
// bd version changes that contract, this function would also need
// to swallow an "already labeled" error from AddLabel before
// proceeding to UpdateDescription.
//
// runbook may be multi-line and arbitrarily long; the underlying bd
// client pipes it via stdin so argv length and shell quoting are not
// concerns. An empty runbook is allowed and will clear the existing
// description.
func BounceToHuman(ctx context.Context, m Mutator, id, runbook string) error {
	if err := m.AddLabel(ctx, id, HumanLabel); err != nil {
		return err
	}
	return m.UpdateDescription(ctx, id, runbook)
}
