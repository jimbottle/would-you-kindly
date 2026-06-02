// Package beads is the typed Go model and CLI wrapper for the bd
// (beads) issue tracker. It shells out to the bd binary and parses
// its JSON output. It never reads or writes bd's storage directly.
package beads

import "time"

// Issue mirrors the fields bd emits in `bd list --json`. Unknown
// fields are silently ignored, which absorbs forward-compatible
// additions in newer bd versions without breaking the TUI.
type Issue struct {
	// Always-present identity/state fields carry no omit option — id,
	// title, status, and priority in particular are load-bearing for
	// every consumer, and priority MUST stay (0 == P0/critical, which
	// omitempty would silently drop).
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	IssueType string `json:"issue_type"`
	// created_at/updated_at use omitzero (not omit) — a real issue's
	// timestamps are never zero, so full output is unchanged; the
	// option exists only so `-slim` (which zeroes them via slimIssue)
	// drops them too.
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`

	// Optional/often-empty fields are elided when empty to cut the
	// per-issue token cost of the agent-facing -json outputs (export,
	// inbox, activity). omitempty/omitzero affect MARSHALING only —
	// bd's JSON still parses into these fields unchanged. ClosedAt uses
	// omitzero (Go 1.24+) because time.Time is a struct omitempty can't
	// elide: this drops the `closed_at:"0001-01-01T00:00:00Z"` that
	// every OPEN issue would otherwise emit (a closed issue's non-zero
	// ClosedAt is still serialised). Description/Notes are the heaviest
	// optional fields; an empty one now costs nothing.
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
	// Assignee is bd's responsible-person field. This project doesn't use
	// it for ownership — ownership is the TUI's HUMAN/AGENT/HUMAN-BLOCK
	// badge (the `human` / `src:agent` labels). Parsed for completeness.
	Assignee  string    `json:"assignee,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	Notes     string    `json:"notes,omitempty"`
	Labels    []string  `json:"labels,omitempty"`

	DependencyCount int `json:"dependency_count,omitempty"`
	DependentCount  int `json:"dependent_count,omitempty"`
	CommentCount    int `json:"comment_count,omitempty"`

	// Repo and Branch are decorations a multi-repo Source attaches
	// after fetching — they are NOT part of bd's JSON. The json:"-"
	// tags prevent them from leaking back into any Marshal call that
	// re-serialises an Issue, and the absent fields just stay empty
	// in single-repo mode.
	Repo   string `json:"-"`
	Branch string `json:"-"`

	// BlockedByHuman is true when this issue's `src:agent` AND its
	// dependency set contains at least one issue carrying the
	// `human` label — i.e. the agent owns this task but the next
	// move is a human's. The TUI uses it to render a HUMAN-BLOCK
	// badge so the inbox imperative ('work these now') doesn't
	// fire on rows the agent literally can't unblock. Populated
	// inside `BDSource.Fetch` via a per-issue `bd dep list`
	// lookup (same-workspace only); concurrency capped by
	// markBlockedByHumanConcurrency.
	BlockedByHuman bool `json:"-"`
}

// HasLabel reports whether the issue carries the given label.
func (i Issue) HasLabel(label string) bool {
	for _, l := range i.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// IsHuman reports whether the issue is flagged for human action
// per docs/CONTRACT.md (the "human" label).
func (i Issue) IsHuman() bool {
	return i.HasLabel("human")
}
