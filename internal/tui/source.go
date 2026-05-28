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
//
// When Name is non-empty, Fetch decorates each returned Issue with
// Repo=Name and Branch=<git branch of Client.Dir>. The TUI uses
// those to render the Repo/Branch columns; setting Name is the way
// a caller in single-repo mode opts into the roborev-like layout
// rather than hiding the columns.
type BDSource struct {
	Client *beads.Client
	// Me is the current user, used by PresetMine. Empty means
	// "mine" degrades to all open issues.
	Me string
	// Name is the display label for the Repo column. Empty leaves
	// Repo blank on each issue (legacy behaviour).
	Name string
}

// Compile-time check that BDSource satisfies both interfaces.
var (
	_ Source  = (*BDSource)(nil)
	_ Mutator = (*BDSource)(nil)
)

// Fetch dispatches to the right bd subcommand for the preset, then
// decorates the result with Repo/Branch when Name is set.
func (s *BDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	var issues []beads.Issue
	var err error
	switch p {
	case filter.PresetReady:
		// bd ready has blocker-aware semantics that bd query cannot
		// reproduce; defer to it.
		issues, err = s.Client.Ready(ctx)
	case filter.PresetAll:
		// "all" in the TUI means "all non-closed" — opening wyk
		// should show actionable work, not the full history.
		issues, err = s.Client.List(ctx)
	default:
		issues, err = s.Client.Query(ctx, filter.Query(p, s.Me))
	}
	if err != nil {
		return nil, err
	}
	decorateIssues(issues, s.Name, func() string { return gitBranch(ctx, s.Client.Dir) })
	return issues, nil
}

// decorateIssues stamps every issue with Repo=name and a lazily-
// resolved Branch — but only when name is non-empty. The branch
// lookup is deferred via a closure so callers don't pay the
// git-shell-out cost when name is empty (the legacy single-repo
// layout). Package-private; the seam exists for tests.
func decorateIssues(issues []beads.Issue, name string, branchFn func() string) {
	if name == "" {
		return
	}
	branch := branchFn()
	for i := range issues {
		issues[i].Repo = name
		issues[i].Branch = branch
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
// BDSource; `branchFn` takes a context so a canceled Fetch (TUI
// quit, refresh-during-refresh) actually unblocks any in-flight
// `git rev-parse`. Tests pass a constant.
type subRepo struct {
	name     string
	src      fullSource
	branchFn func(context.Context) string
}

// MultiBDSource queries every registered bd workspace and unions
// the results, populating Issue.Repo and Issue.Branch on each row
// so the TUI can show them as columns. Mutator methods route to the
// right sub by reading Issue.Repo (which Fetch populates) — there
// is no bare-ID fallback. Issues with an empty Repo are a
// programmer error in this package and produce a clear "repo not
// set" failure rather than a silent ID-collision mis-route.
type MultiBDSource struct {
	subs []subRepo
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
			branchFn: func(ctx context.Context) string { return gitBranch(ctx, dir) },
		}
	}
	return &MultiBDSource{subs: subs}, nil
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
				branches[i] = sub.branchFn(ctx)
			}
		}(i, sub)
	}
	wg.Wait()

	var all []beads.Issue
	var firstErr error
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
		}
		all = append(all, r.issues...)
	}

	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// repoForIssue returns the sub whose name matches issue.Repo.
// Routing strictly on Issue.Repo (populated by Fetch) guarantees
// writes can never mis-route via ID collisions across workspaces.
// An empty Repo is a programmer error: every in-tree caller obtains
// the Issue from a Source.Fetch which populates Repo. The explicit
// error is louder than a silent fallback would be.
func (m *MultiBDSource) repoForIssue(i beads.Issue) (fullSource, error) {
	if i.Repo == "" {
		return nil, fmt.Errorf("issue %q has no Repo set (multi-repo Mutator requires it; did you obtain the Issue from Fetch?)", i.ID)
	}
	for _, sub := range m.subs {
		if sub.name == i.Repo {
			return sub.src, nil
		}
	}
	return nil, fmt.Errorf("issue %q claims repo %q which is not in the registry", i.ID, i.Repo)
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
