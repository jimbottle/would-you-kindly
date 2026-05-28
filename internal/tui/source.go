package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Compile-time check that BDSource satisfies the three interfaces.
var (
	_ Source   = (*BDSource)(nil)
	_ Mutator  = (*BDSource)(nil)
	_ Detailer = (*BDSource)(nil)
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
	hooked := wykHookInstalled(s.Client.Dir)
	decorateIssues(issues, s.Name, func() string { return gitBranch(ctx, s.Client.Dir) }, hooked)
	return issues, nil
}

// decorateIssues stamps every issue with Repo=name, a lazily-
// resolved Branch, and wykHooked — but only when name is non-empty.
// The branch lookup is deferred via a closure so callers don't pay
// the git-shell-out cost when name is empty (the legacy single-repo
// layout). Package-private; the seam exists for tests.
func decorateIssues(issues []beads.Issue, name string, branchFn func() string, wykHooked bool) {
	if name == "" {
		return
	}
	branch := branchFn()
	for i := range issues {
		issues[i].Repo = name
		issues[i].Branch = branch
		issues[i].WykHooked = wykHooked
	}
}

// wykHookInstalled reports whether dir's post-commit hook is wyk's
// (plain or chained). Matches on the unique "wyk hook post-commit"
// invocation present in both variants and absent from foreign hooks.
// Returns false on any I/O error — a missing or unreadable hook is
// effectively "not installed" from the user's perspective.
//
// Resolves the hook path via `git rev-parse --git-path` so gitlinks
// (a `.git` file pointing into a parent repo's git dir, common for
// submodules and worktree-style subdirectory registrations) and
// custom GIT_DIR layouts land on the right hook — matching where
// `wyk init` would have installed it.
func wykHookInstalled(dir string) bool {
	if dir == "" {
		return false
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-path", "hooks/post-commit").Output()
	if err != nil {
		return false
	}
	hookPath := strings.TrimSpace(string(out))
	if hookPath == "" {
		return false
	}
	if !filepath.IsAbs(hookPath) {
		hookPath = filepath.Join(dir, hookPath)
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		return false
	}
	return bytes.Contains(body, []byte("wyk hook post-commit"))
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

// Create runs `bd create` with the given title and the src:human
// label (this user filed it for themselves). The repo arg is ignored
// in single-repo mode — BDSource only has one client to write to.
func (s *BDSource) Create(ctx context.Context, _ /* repo */, title string) (string, error) {
	return s.Client.Create(ctx, beads.CreateOptions{
		Title:     title,
		Labels:    []string{"src:human"},
		IssueType: "task",
	})
}

// Detail runs `bd show <id>` and decorates the resulting issue with
// Repo/Branch so callers can treat it like any other Source-derived
// Issue.
func (s *BDSource) Detail(ctx context.Context, i beads.Issue) (beads.Issue, error) {
	full, err := s.Client.Show(ctx, i.ID)
	if err != nil {
		return beads.Issue{}, err
	}
	if s.Name != "" {
		full.Repo = s.Name
		full.Branch = gitBranch(ctx, s.Client.Dir)
	}
	return full, nil
}

// --- MultiBDSource: union of multiple bd workspaces -----------------

// fullSource is anything that can read, write, AND detail-fetch a bd
// workspace. Used by MultiBDSource so tests can inject a stub
// instead of going through a real BDSource with a hidden runner.
type fullSource interface {
	Source
	Mutator
	Detailer
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

// FetchError pairs a sub-source's display name with the error that
// sub-source returned. Surfaced atomically with the fetched issues
// (via MultiSource.FetchWithSubErrors) so the TUI can render a
// banner — a sub that errors out otherwise contributes zero rows
// and is invisible to the user (the bug that hid domo-mcp's
// broken state).
type FetchError struct {
	Repo string
	Err  error
}

// MultiSource is the optional interface MultiBDSource satisfies so
// callers can fetch issues AND per-sub failures in a single atomic
// snapshot. Single-repo BDSource doesn't implement it — there's no
// "other repo" to fail. The model runtime type-asserts and prefers
// this method over plain Source.Fetch when available.
//
// Returning errors directly (rather than stashing them on the
// source and exposing a getter) is deliberate: a getter races with
// concurrent fetches scheduled by the auto-refresh tick — the model
// could read errors from fetch N+1 alongside issues from fetch N.
// Atomic return eliminates that window.
type MultiSource interface {
	FetchWithSubErrors(ctx context.Context, p filter.Preset) ([]beads.Issue, []FetchError, error)
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
	_ Source      = (*MultiBDSource)(nil)
	_ Mutator     = (*MultiBDSource)(nil)
	_ Detailer    = (*MultiBDSource)(nil)
	_ MultiSource = (*MultiBDSource)(nil)
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

// Fetch satisfies Source. Discards per-sub error detail; callers
// that need it should use FetchWithSubErrors via the MultiSource
// interface.
func (m *MultiBDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	issues, _, err := m.FetchWithSubErrors(ctx, p)
	return issues, err
}

// FetchWithSubErrors queries every sub-source concurrently and
// concatenates their results in stable registry order. Each row is
// decorated with its repo name and the repo's current git branch.
// Per-repo errors are tolerated as long as at least one repo
// returned data; if every repo errored, the first error (in
// registry order) is surfaced as the top-level error. Either way
// the per-sub error slice is returned atomically with the issues so
// callers don't race a concurrent next fetch.
//
// Parallelism matters because each sub.Fetch shells out to `bd`,
// and with 4–5 registered workspaces the sequential cost was
// user-perceptible on every refresh.
func (m *MultiBDSource) FetchWithSubErrors(ctx context.Context, p filter.Preset) ([]beads.Issue, []FetchError, error) {
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
	var fetchErrs []FetchError
	for i, sub := range m.subs {
		r := results[i]
		if r.err != nil {
			fetchErrs = append(fetchErrs, FetchError{Repo: sub.name, Err: r.err})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", sub.name, r.err)
			}
			continue
		}
		// Cross-workspace leak guard. Drop any issue whose ID
		// prefix doesn't match the registered Name — bd has been
		// observed serving foreign workspace data when a sub's
		// `.beads/` is broken (e.g. a dead jsonl-only export
		// alongside other healthy workspaces, bd's daemon then
		// returns whichever workspace is currently warm). Without
		// this guard, those foreign rows render attributed to the
		// wrong repo, hiding a P0 bug as a duplicate-looking row.
		//
		// False positive: a workspace where the user manually set
		// Name (in repos.json) to something other than the bd
		// `issue-prefix`. Surfaced as a fetch error pointing them
		// at the registry; they fix by editing the entry.
		expect := sub.name + "-"
		var clean []beads.Issue
		var foreign int
		for j := range r.issues {
			if !strings.HasPrefix(r.issues[j].ID, expect) {
				foreign++
				continue
			}
			r.issues[j].Repo = sub.name
			r.issues[j].Branch = branches[i]
			clean = append(clean, r.issues[j])
		}
		if foreign > 0 {
			fetchErrs = append(fetchErrs, FetchError{
				Repo: sub.name,
				Err:  fmt.Errorf("%d issue(s) had foreign ID prefix (expected %q*) — bd may be serving the wrong workspace; check `wyk doctor` and ~/.config/wyk/repos.json", foreign, expect),
			})
		}
		all = append(all, clean...)
	}

	if len(all) == 0 && firstErr != nil {
		return nil, fetchErrs, firstErr
	}
	return all, fetchErrs, nil
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

// Detail routes the show request to the issue's repo. Same routing
// guarantees as the write methods — issue.Repo must be set.
func (m *MultiBDSource) Detail(ctx context.Context, i beads.Issue) (beads.Issue, error) {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return beads.Issue{}, err
	}
	return sub.Detail(ctx, i)
}

// Create routes the new issue to a specific sub by name. If repo is
// empty, falls back to the first sub — the registry's first repo.
// Empty repo is the multi-repo equivalent of "I'm not on any row
// right now, just file it somewhere".
func (m *MultiBDSource) Create(ctx context.Context, repo, title string) (string, error) {
	if repo == "" {
		if len(m.subs) == 0 {
			return "", fmt.Errorf("no registered workspaces to create in")
		}
		return m.subs[0].src.Create(ctx, "", title)
	}
	for _, sub := range m.subs {
		if sub.name == repo {
			return sub.src.Create(ctx, "", title)
		}
	}
	return "", fmt.Errorf("repo %q not in subs", repo)
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
