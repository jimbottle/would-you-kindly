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
// to write to. The Issue.ID field is the only thing that reaches bd.

func (s *BDSource) Close(ctx context.Context, i beads.Issue) error {
	return s.Client.Close(ctx, i.ID)
}

func (s *BDSource) AddLabel(ctx context.Context, i beads.Issue, label string) error {
	return s.Client.AddLabel(ctx, i.ID, label)
}

func (s *BDSource) RemoveLabel(ctx context.Context, i beads.Issue, label string) error {
	return s.Client.RemoveLabel(ctx, i.ID, label)
}

func (s *BDSource) Note(ctx context.Context, i beads.Issue, text string) error {
	return s.Client.Note(ctx, i.ID, text)
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
// (client, displayName) pairs. The two slices are positionally
// coupled, so an explicit length check up front turns a programmer
// error into a real error instead of an `index out of range` panic
// at the first Fetch.
func NewMultiBDSource(clients []*beads.Client, names []string, me string) (*MultiBDSource, error) {
	if len(clients) != len(names) {
		return nil, fmt.Errorf("clients/names length mismatch: %d clients, %d names",
			len(clients), len(names))
	}
	subs := make([]subRepo, len(clients))
	for i, c := range clients {
		dir := c.Dir
		subs[i] = subRepo{
			name:     names[i],
			src:      &BDSource{Client: c, Me: me},
			branchFn: func() string { return gitBranch(context.Background(), dir) },
		}
	}
	return &MultiBDSource{
		subs:     subs,
		idToRepo: make(map[string]string),
	}, nil
}

// Fetch queries every sub-source concurrently and concatenates their
// results in stable registry order. Each row is decorated with its
// repo name and the repo's current git branch. Per-repo errors are
// tolerated as long as at least one repo returned data; if every
// repo errored, the first error (in registry order) is surfaced.
//
// Parallelism matters because each sub.Fetch shells out to `bd`,
// and with 4–5 registered workspaces the sequential cost was
// user-perceptible on every refresh.
func (m *MultiBDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	type result struct {
		issues []beads.Issue
		err    error
	}
	results := make([]result, len(m.subs))
	branches := make([]string, len(m.subs))

	var wg sync.WaitGroup
	for i, sub := range m.subs {
		wg.Add(1)
		go func(i int, sub subRepo) {
			defer wg.Done()
			issues, err := sub.src.Fetch(ctx, p)
			results[i] = result{issues: issues, err: err}
			if err == nil {
				branches[i] = sub.branchFn()
			}
		}(i, sub)
	}
	wg.Wait()

	var all []beads.Issue
	var firstErr error
	idMap := make(map[string]string)
	for i, sub := range m.subs {
		r := results[i]
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", sub.name, r.err)
			}
			continue
		}
		for j := range r.issues {
			r.issues[j].Repo = sub.name
			r.issues[j].Branch = branches[i]
			idMap[r.issues[j].ID] = sub.name
		}
		all = append(all, r.issues...)
	}

	// Atomically replace the id→repo map so a subsequent write's
	// legacy-fallback path (used when Issue.Repo is empty) routes
	// against THIS fetch's view of the world.
	m.mu.Lock()
	m.idToRepo = idMap
	m.mu.Unlock()

	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// repoForIssue returns the sub whose name matches issue.Repo.
// Routing on the issue's Repo field (set at Fetch time) rather than
// a bare ID guarantees writes can't mis-route on ID collisions
// across workspaces. Falls back to the idToRepo map only when the
// issue lacks a Repo (e.g. a stale call site that hadn't been
// updated yet) — the map can return wrong-repo results on
// collisions, so a missing Repo logs visibly via the error string.
func (m *MultiBDSource) repoForIssue(i beads.Issue) (fullSource, error) {
	if i.Repo != "" {
		for _, sub := range m.subs {
			if sub.name == i.Repo {
				return sub.src, nil
			}
		}
		return nil, fmt.Errorf("issue %q claims repo %q which is not in the registry", i.ID, i.Repo)
	}
	// Legacy path: bare ID lookup. Kept so callers that obtain an
	// Issue from a non-multi source still work; multi-source Fetch
	// always populates Repo so this branch isn't reached for them.
	m.mu.RLock()
	name, ok := m.idToRepo[i.ID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no repo known for issue %q (try refreshing)", i.ID)
	}
	for _, sub := range m.subs {
		if sub.name == name {
			return sub.src, nil
		}
	}
	return nil, fmt.Errorf("internal: repo %q not in subs", name)
}

func (m *MultiBDSource) Close(ctx context.Context, i beads.Issue) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.Close(ctx, i)
}

func (m *MultiBDSource) AddLabel(ctx context.Context, i beads.Issue, label string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.AddLabel(ctx, i, label)
}

func (m *MultiBDSource) RemoveLabel(ctx context.Context, i beads.Issue, label string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.RemoveLabel(ctx, i, label)
}

func (m *MultiBDSource) Note(ctx context.Context, i beads.Issue, text string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.Note(ctx, i, text)
}

// gitBranch returns the current branch name of the repo at dir, or
// the empty string if the lookup fails. A detached HEAD comes back
// as "HEAD"; we leave that as-is so the TUI shows the truth rather
// than masking the state. exec.CommandContext respects ctx, so a
// canceled fetch (TUI quit) doesn't leave a stranded git process.
func gitBranch(ctx context.Context, dir string) string {
	args := []string{"rev-parse", "--abbrev-ref", "HEAD"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
