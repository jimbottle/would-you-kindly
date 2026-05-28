package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// BDSource is a single-repo Source backed by the bd CLI. It centralises
// the preset → bd-command mapping so the TUI itself stays free of
// command-line semantics. It also satisfies Mutator so the write
// keystrokes (c / H / n) dispatch through it.
type BDSource struct {
	Client *beads.Client
	// Me is the current user, used by PresetMine. Empty means
	// "mine" degrades to all open issues.
	Me string
}

// Compile-time check that BDSource satisfies both interfaces.
var (
	_ Source  = (*BDSource)(nil)
	_ Mutator = (*BDSource)(nil)
)

// Fetch dispatches to the right bd subcommand for the preset.
func (s *BDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	switch p {
	case filter.PresetReady:
		// bd ready has blocker-aware semantics that bd query cannot
		// reproduce; defer to it.
		return s.Client.Ready(ctx)
	case filter.PresetAll:
		// "all" in the TUI means "all non-closed" — opening wyk
		// should show actionable work, not the full history.
		return s.Client.List(ctx)
	default:
		return s.Client.Query(ctx, filter.Query(p, s.Me))
	}
}

// --- Mutator implementation (single-repo) ---
// BDSource ignores Repo on the issue — it has only one workspace
// to write to.

func (s *BDSource) Close(ctx context.Context, id string) error {
	return s.Client.Close(ctx, id)
}

func (s *BDSource) AddLabel(ctx context.Context, id, label string) error {
	return s.Client.AddLabel(ctx, id, label)
}

func (s *BDSource) RemoveLabel(ctx context.Context, id, label string) error {
	return s.Client.RemoveLabel(ctx, id, label)
}

func (s *BDSource) Note(ctx context.Context, id, text string) error {
	return s.Client.Note(ctx, id, text)
}

// --- MultiBDSource: union of multiple bd workspaces -----------------

// fullSource is anything that can both read and write a bd workspace.
// Used by MultiBDSource so tests can inject a stub instead of going
// through a real BDSource with a hidden runner.
type fullSource interface {
	Source
	Mutator
}

// subRepo is one row in MultiBDSource's per-repo table. Held as an
// interface (fullSource) so tests can substitute a stub for the real
// BDSource; `dir` is the path used to query the current git branch
// and is empty for stubs (and for that case `branchFn` provides the
// value tests want to assert against).
type subRepo struct {
	name     string
	src      fullSource
	branchFn func() string
}

// MultiBDSource queries every registered bd workspace and unions
// the results, populating Issue.Repo and Issue.Branch on each row
// so the TUI can show them as columns. Mutator methods route to the
// right sub by remembering which repo each issue ID came from on
// the most recent Fetch.
type MultiBDSource struct {
	subs []subRepo

	// idToRepo is rebuilt on every Fetch and lets Mutator methods
	// route writes without changing the Source/Mutator interface
	// shape (still id-string-based, same as BDSource).
	mu       sync.RWMutex
	idToRepo map[string]string
}

// Compile-time check.
var (
	_ Source  = (*MultiBDSource)(nil)
	_ Mutator = (*MultiBDSource)(nil)
)

// NewMultiBDSource constructs a multi-repo source from a list of
// (client, displayName) pairs. The names are typically taken from
// the registry's Repo.Name field.
func NewMultiBDSource(clients []*beads.Client, names []string, me string) *MultiBDSource {
	subs := make([]subRepo, len(clients))
	for i, c := range clients {
		dir := c.Dir
		subs[i] = subRepo{
			name:     names[i],
			src:      &BDSource{Client: c, Me: me},
			branchFn: func() string { return gitBranch(dir) },
		}
	}
	return &MultiBDSource{
		subs:     subs,
		idToRepo: make(map[string]string),
	}
}

// Fetch queries every sub-source and concatenates their results,
// decorating each issue with its repo name and the repo's current
// git branch. Per-repo errors are tolerated as long as at least one
// repo returned data; if every repo errored, the first error is
// surfaced so the user sees something actionable.
func (m *MultiBDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	var all []beads.Issue
	var firstErr error
	idMap := make(map[string]string)

	for _, sub := range m.subs {
		issues, err := sub.src.Fetch(ctx, p)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", sub.name, err)
			}
			continue
		}
		branch := sub.branchFn()
		for j := range issues {
			issues[j].Repo = sub.name
			issues[j].Branch = branch
			idMap[issues[j].ID] = sub.name
		}
		all = append(all, issues...)
	}

	// Atomically replace the id→repo map so a subsequent write
	// routes against THIS fetch's view of the world, not a stale one.
	m.mu.Lock()
	m.idToRepo = idMap
	m.mu.Unlock()

	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// repoFor looks up which sub-source owns the given issue ID.
func (m *MultiBDSource) repoFor(id string) (fullSource, error) {
	m.mu.RLock()
	name, ok := m.idToRepo[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no repo known for issue %q (try refreshing)", id)
	}
	for _, sub := range m.subs {
		if sub.name == name {
			return sub.src, nil
		}
	}
	return nil, fmt.Errorf("internal: repo %q not in subs", name)
}

func (m *MultiBDSource) Close(ctx context.Context, id string) error {
	sub, err := m.repoFor(id)
	if err != nil {
		return err
	}
	return sub.Close(ctx, id)
}

func (m *MultiBDSource) AddLabel(ctx context.Context, id, label string) error {
	sub, err := m.repoFor(id)
	if err != nil {
		return err
	}
	return sub.AddLabel(ctx, id, label)
}

func (m *MultiBDSource) RemoveLabel(ctx context.Context, id, label string) error {
	sub, err := m.repoFor(id)
	if err != nil {
		return err
	}
	return sub.RemoveLabel(ctx, id, label)
}

func (m *MultiBDSource) Note(ctx context.Context, id, text string) error {
	sub, err := m.repoFor(id)
	if err != nil {
		return err
	}
	return sub.Note(ctx, id, text)
}

// gitBranch returns the current branch name of the repo at dir, or
// the empty string if the lookup fails. A detached HEAD comes back
// as "HEAD"; we leave that as-is so the TUI shows the truth rather
// than masking the state.
func gitBranch(dir string) string {
	args := []string{"rev-parse", "--abbrev-ref", "HEAD"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
