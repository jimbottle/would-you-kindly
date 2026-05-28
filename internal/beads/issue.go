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
