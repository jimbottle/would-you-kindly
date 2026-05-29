// Package beads is the typed Go model and CLI wrapper for the bd
// (beads) issue tracker. It shells out to the bd binary and parses
// its JSON output. It never reads or writes bd's storage directly.
package beads

import "time"

// Issue mirrors the fields bd emits in `bd list --json`. Unknown
// fields are silently ignored, which absorbs forward-compatible
// additions in newer bd versions without breaking the TUI.
type Issue struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	IssueType   string    `json:"issue_type"`
	Owner       string    `json:"owner"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
	UpdatedAt   time.Time `json:"updated_at"`
	ClosedAt    time.Time `json:"closed_at"`
	Notes       string    `json:"notes"`
	Labels      []string  `json:"labels"`

	DependencyCount int `json:"dependency_count"`
	DependentCount  int `json:"dependent_count"`
	CommentCount    int `json:"comment_count"`

	// Repo and Branch are decorations a multi-repo Source attaches
	// after fetching — they are NOT part of bd's JSON. The json:"-"
	// tags prevent them from leaking back into any Marshal call that
	// re-serialises an Issue, and the absent fields just stay empty
	// in single-repo mode.
	Repo   string `json:"-"`
	Branch string `json:"-"`

	// WykHooked is true when the issue's repo has wyk's post-commit
	// hook installed (plain or chained). Surfaces in the TUI as a
	// per-row indicator so the user can tell which registered repos
	// have the auto-close machinery active vs. which are just being
	// tracked.
	WykHooked bool `json:"-"`

	// BlockedByHuman is true when this issue's `src:agent` AND its
	// dependency set contains at least one issue carrying the
	// `human` label — i.e. the agent owns this task but the next
	// move is a human's. The TUI uses it to render a HUMAN-BLOCK
	// badge so the inbox imperative ('work these now') doesn't
	// fire on rows the agent literally can't unblock. Populated
	// post-Fetch by MultiBDSource after a per-workspace
	// `bd dep list` batch lookup; same-workspace only for v1.
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
