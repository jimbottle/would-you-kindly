package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	// DepSem is an optional shared semaphore for the HUMAN-BLOCK
	// dep-scan. nil means "allocate a local channel" (the
	// single-repo path). MultiBDSource sets this to a single
	// shared channel across every sub so the global bd-subprocess
	// concurrency is bounded regardless of registry size.
	DepSem chan struct{}
	// IncludeClosed routes PresetAll through Client.ListAll
	// (bd list --all) and drops the status!=closed clause from
	// other presets' queries. Toggled by the C key.
	IncludeClosed bool
}

// Compile-time check that BDSource satisfies the three interfaces.
var (
	_ Source    = (*BDSource)(nil)
	_ Mutator   = (*BDSource)(nil)
	_ Detailer  = (*BDSource)(nil)
	_ DepLister = (*BDSource)(nil)
)

// Fetch dispatches to the right bd subcommand for the preset, then
// decorates the result with Repo/Branch when Name is set.
// depLister is the minimum surface markBlockedByHuman needs from
// a Client — the real *beads.Client satisfies it, and tests inject
// stubs that return canned dep lists so the dep-scan loop can be
// exercised without a real bd binary.
type depLister interface {
	ListDeps(ctx context.Context, id string) ([]beads.Issue, error)
}

// fetchCall identifies which bd subcommand BDSource.Fetch will
// invoke for a given preset + IncludeClosed combination. Exposing
// the routing decision as a value (rather than burying it inside
// Fetch's switch) lets tests assert "this combination resolves to
// list-all, not query-empty-string" without standing up a fake bd
// Client.
type fetchCall int

const (
	fetchReady fetchCall = iota
	fetchList
	fetchListAll
	fetchQuery
)

// pickFetchCall resolves the preset + flags to a bd subcommand
// choice. The query string is meaningful only when call==fetchQuery.
// Default-branch presets whose QueryWithClosed result collapses to
// "" (e.g. mine with no resolved identity and IncludeClosed=true)
// are routed to list/listall — bd query "" would error.
func (s *BDSource) pickFetchCall(p filter.Preset) (fetchCall, string) {
	switch p {
	case filter.PresetReady:
		// bd ready has blocker-aware semantics that bd query cannot
		// reproduce; defer to it. IncludeClosed has no effect — bd
		// ready is open-only by definition.
		return fetchReady, ""
	case filter.PresetAll:
		// "all" in the TUI means "all non-closed" by default —
		// opening wyk should show actionable work, not the full
		// history. C toggles IncludeClosed and switches us to
		// `bd list --all` for the archive view.
		if s.IncludeClosed {
			return fetchListAll, ""
		}
		return fetchList, ""
	default:
		q := filter.QueryWithClosed(p, s.Me, s.IncludeClosed)
		if q == "" {
			// QueryWithClosed can legitimately return "" for a
			// preset where every clause has been dropped — e.g.
			// `mine` with no resolved identity (Me=="") and
			// IncludeClosed=true. The preset chip stays accurate
			// (user still sees the "mine" filter) even though the
			// result is "everything" — there's no narrower query
			// we can express, so degrading to all is more useful
			// than the bd-error banner an empty query produces.
			if s.IncludeClosed {
				return fetchListAll, ""
			}
			return fetchList, ""
		}
		return fetchQuery, q
	}
}

func (s *BDSource) Fetch(ctx context.Context, p filter.Preset) ([]beads.Issue, error) {
	var issues []beads.Issue
	var err error
	call, q := s.pickFetchCall(p)
	switch call {
	case fetchReady:
		issues, err = s.Client.Ready(ctx)
	case fetchListAll:
		issues, err = s.Client.ListAll(ctx)
	case fetchList:
		issues, err = s.Client.List(ctx)
	case fetchQuery:
		issues, err = s.Client.Query(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	decorateIssues(issues, s.Name, func() string { return gitBranch(ctx, s.Client.Dir) })
	markBlockedByHuman(ctx, s.Client, issues, s.DepSem)
	return issues, nil
}

// markBlockedByHumanConcurrency is the default cap on in-flight
// `bd dep list` subprocesses when markBlockedByHuman runs on its
// own (single-repo Source). Multi-repo callers (MultiBDSource)
// inject a shared semaphore sized to this same value, so the
// GLOBAL bd-subprocess count per refresh is bounded by this
// constant regardless of how many workspaces are registered —
// rather than scaling as M*8 with a per-workspace cap.
const markBlockedByHumanConcurrency = 8

// fetchConcurrency caps how many per-repo `bd list` fetches run at
// once inside FetchWithSubErrors. Each fetch can cold-start that
// workspace's embedded-Dolt engine, and launching all N at once
// (the old unbounded fan-out) made every engine contend for CPU/IO
// at the same instant — a thundering herd that routinely blew past
// the bd client's per-call 10s deadline once a few repos were
// registered. Throttling entry to a small window keeps each
// individual fetch under that deadline while still overlapping
// enough to beat the sequential cost. Kept below
// markBlockedByHumanConcurrency on purpose: the dep-scan is cheap
// `bd dep list` calls against an already-warm engine, whereas these
// are the expensive cold-start `bd list` calls we actually need to
// rate-limit.
const fetchConcurrency = 4

// fetchRetryBaseBackoff is the floor delay before a transient-timeout
// retry. fetchRetryBackoff staggers per-index on top of it so a wave
// of simultaneous timeouts doesn't all retry at the same instant and
// re-create the thundering herd. By retry time the first wave has
// warmed the OS/Dolt cache, so a single concurrency-capped retry
// almost always lands well under the deadline — the same thing the
// user's manual `r` does.
const fetchRetryBaseBackoff = 150 * time.Millisecond

func fetchRetryBackoff(i int) time.Duration {
	return fetchRetryBaseBackoff + time.Duration(i%fetchConcurrency)*80*time.Millisecond
}

// markBlockedByHuman runs `bd dep list <id>` for every candidate
// issue (src:agent + NOT human + DependencyCount > 0) and stamps
// Issue.BlockedByHuman=true on any whose blocker set includes a
// human-labeled task. Concurrency is bounded by sem; passing nil
// allocates a local channel of size markBlockedByHumanConcurrency
// (the single-repo path). MultiBDSource constructs one shared
// channel and threads it into every sub's BDSource so the global
// concurrent-subprocess count stays bounded across the whole
// fetch. Best-effort: ListDeps errors are swallowed so a flaky
// bd call only loses the badge for that row, not the whole fetch.
//
// Same-workspace only — the blocker has to be reachable via the
// same bd Client. Cross-workspace deps (rare in practice) fall
// through and the row keeps the plain AGENT badge.
func markBlockedByHuman(ctx context.Context, c depLister, issues []beads.Issue, sem chan struct{}) {
	if c == nil {
		return
	}
	if sem == nil {
		sem = make(chan struct{}, markBlockedByHumanConcurrency)
	}
	var wg sync.WaitGroup
	for i := range issues {
		if !isAgentInboxCandidate(issues[i]) {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			deps, err := c.ListDeps(ctx, issues[i].ID)
			if err != nil {
				return
			}
			for _, d := range deps {
				if d.IsHuman() {
					issues[i].BlockedByHuman = true
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// isAgentInboxCandidate reports whether the issue is in scope for
// the HUMAN-BLOCK dep-aware check: src:agent (the agent owns it),
// NOT human-flagged (so the badge isn't HUMAN already), and has
// at least one dependency (no point shelling out otherwise).
func isAgentInboxCandidate(i beads.Issue) bool {
	return i.HasLabel("src:agent") && !i.IsHuman() && i.DependencyCount > 0
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

func (s *BDSource) Reopen(ctx context.Context, i beads.Issue) error {
	return s.Client.Reopen(ctx, i.ID)
}

func (s *BDSource) SetDefer(ctx context.Context, i beads.Issue, when string) error {
	return s.Client.SetDefer(ctx, i.ID, when)
}

func (s *BDSource) SetPriority(ctx context.Context, i beads.Issue, p int) error {
	return s.Client.SetPriority(ctx, i.ID, p)
}

func (s *BDSource) SetIssueType(ctx context.Context, i beads.Issue, issueType string) error {
	return s.Client.SetIssueType(ctx, i.ID, issueType)
}

func (s *BDSource) RawBD(ctx context.Context, _ /* repo */ string, args []string) ([]byte, error) {
	return s.Client.RawRun(ctx, args)
}

func (s *BDSource) SetAssignee(ctx context.Context, i beads.Issue, assignee string) error {
	return s.Client.SetAssignee(ctx, i.ID, assignee)
}

func (s *BDSource) SetDescription(ctx context.Context, i beads.Issue, body string) error {
	return s.Client.SetDescription(ctx, i.ID, body)
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
// assignee is the owner the new issue lands on; the caller is
// responsible for refusing to dispatch when assignee is empty so
// the orphan case never makes it to bd.
func (s *BDSource) Create(ctx context.Context, _ /* repo */, title, assignee string) (string, error) {
	return s.Client.Create(ctx, beads.CreateOptions{
		Title:     title,
		Labels:    []string{"src:human"},
		IssueType: "task",
		Assignee:  assignee,
	})
}

// ListDeps returns the direct dependencies of issue id, shelling
// through to the underlying bd Client. Satisfies DepLister so the
// TUI's topological deps-sort can resolve the visible set's edges
// without reaching past the Source. Best-effort by virtue of
// Client.ListDeps — a missing/malformed dep block yields an empty
// slice rather than an error.
func (s *BDSource) ListDeps(ctx context.Context, id string) ([]beads.Issue, error) {
	return s.Client.ListDeps(ctx, id)
}

// ListDependents returns the direct dependents of issue id (the
// issues it blocks), shelling through to the underlying bd Client.
// Satisfies DepLister's reverse-direction half so the detail view's
// "dependents" section can render. Thin pass-through, same contract
// as ListDeps — see Client.ListDependents.
func (s *BDSource) ListDependents(ctx context.Context, id string) ([]beads.Issue, error) {
	return s.Client.ListDependents(ctx, id)
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
	DepLister
}

// subRepo is one row in MultiBDSource's per-repo table. Held as an
// interface (fullSource) so tests can substitute a stub for the real
// BDSource; `branchFn` takes a context so a canceled Fetch (TUI
// quit, refresh-during-refresh) actually unblocks any in-flight
// `git rev-parse`. Tests pass a constant.
type subRepo struct {
	name string
	src  fullSource
	// path is the repo's working dir, used to stat .beads for the
	// per-repo fetch cache (would-you-kindly-jipr). Empty disables
	// caching for this sub (e.g. test stubs) — the stat fails, so the
	// fetch always runs live.
	path     string
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

	// Per-repo fetch cache (would-you-kindly-jipr). A refresh re-queries
	// every registered repo via bd, which scales poorly with registry
	// size; most refreshes (a tick, or an fs-event from ONE repo's write)
	// leave the other repos unchanged. We skip the bd subprocess for a
	// repo whose .beads/ mtime AND preset match a recent cache entry.
	// Invalidated by: an mtime change (covers every bd write, local or
	// external, since bd's temp-file+rename writes bump the dir mtime),
	// SetIncludeClosed (changes query semantics without touching .beads),
	// an explicit InvalidateCache (the manual `r` refresh), and a TTL
	// backstop that self-heals any missed mtime signal.
	cacheMu sync.Mutex
	cache   map[string]subCacheEntry
}

// subCacheEntry is one repo's cached fetch result. Keyed by sub name in
// MultiBDSource.cache.
type subCacheEntry struct {
	preset filter.Preset
	mtime  time.Time
	at     time.Time
	issues []beads.Issue
	branch string
}

// fetchCacheTTL bounds how long a cache entry is trusted even if its
// .beads mtime never changes — a backstop against a missed mtime signal.
// Generous because mtime + the manual `r` refresh are the real freshness
// mechanisms; the TTL just guarantees eventual self-heal.
const fetchCacheTTL = 5 * time.Minute

// beadsMtime returns the modification time of repoPath/.beads and ok=true
// when it can be stat'd. ok=false (empty path, missing dir, stat error)
// means "freshness unknown" — the caller must NOT serve from cache.
func beadsMtime(repoPath string) (time.Time, bool) {
	if repoPath == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(filepath.Join(repoPath, ".beads"))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// cacheGet returns the cached issues+branch for name when the entry
// matches the requested preset, the current .beads mtime, and is within
// the TTL. Returns a COPY so a downstream in-place re-stamp can't corrupt
// the cached slice.
func (m *MultiBDSource) cacheGet(name string, p filter.Preset, mtime time.Time) ([]beads.Issue, string, bool) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	e, ok := m.cache[name]
	if !ok || e.preset != p || !e.mtime.Equal(mtime) || time.Since(e.at) > fetchCacheTTL {
		return nil, "", false
	}
	return cloneIssues(e.issues), e.branch, true
}

// cachePut snapshots a fresh fetch result for name. Stores a COPY so the
// fetch path's subsequent in-place re-stamp doesn't mutate the entry.
func (m *MultiBDSource) cachePut(name string, p filter.Preset, mtime time.Time, issues []beads.Issue, branch string) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if m.cache == nil {
		m.cache = make(map[string]subCacheEntry)
	}
	m.cache[name] = subCacheEntry{preset: p, mtime: mtime, at: time.Now(), issues: cloneIssues(issues), branch: branch}
}

// InvalidateCache drops every cached fetch result, forcing the next
// FetchWithSubErrors to re-query every repo live. Called on the manual
// `r` refresh and whenever query semantics change (SetIncludeClosed).
func (m *MultiBDSource) InvalidateCache() {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	m.cache = nil
}

// cloneIssues returns a shallow copy of the slice (new backing array,
// shared inner slices like Labels — which the render/leak-guard paths
// never mutate). Enough to stop top-level struct aliasing between the
// cache and the live fetch result.
func cloneIssues(in []beads.Issue) []beads.Issue {
	if in == nil {
		return nil
	}
	out := make([]beads.Issue, len(in))
	copy(out, in)
	return out
}

// ClosedToggler is the optional interface a Source implements to
// honor the C-key "show closed" toggle. BDSource and MultiBDSource
// both satisfy it; the model runtime type-asserts.
type ClosedToggler interface {
	SetIncludeClosed(v bool)
}

// cacheInvalidator is the optional interface a Source implements to drop
// any internal fetch cache on demand. MultiBDSource satisfies it; the
// model calls it on the manual `r` refresh so the user can always force
// fresh data (would-you-kindly-jipr).
type cacheInvalidator interface {
	InvalidateCache()
}

// SetIncludeClosed makes BDSource satisfy ClosedToggler so a
// single-repo model can flip the state through the same seam as
// the multi-repo path.
func (s *BDSource) SetIncludeClosed(v bool) {
	s.IncludeClosed = v
}

// SetIncludeClosed propagates the C-key toggle to every sub-source
// in registry order so the next Fetch returns the requested scope
// across all repos in one shot.
func (m *MultiBDSource) SetIncludeClosed(v bool) {
	for _, sub := range m.subs {
		if bds, ok := sub.src.(*BDSource); ok {
			bds.IncludeClosed = v
		}
	}
	// The cache is keyed on (preset, mtime) but NOT on IncludeClosed, and
	// toggling it changes what a fetch returns without touching .beads —
	// so drop the cache to avoid serving the old closed-ness.
	m.InvalidateCache()
}

// Compile-time check.
var (
	_ Source      = (*MultiBDSource)(nil)
	_ Mutator     = (*MultiBDSource)(nil)
	_ Detailer    = (*MultiBDSource)(nil)
	_ DepLister   = (*MultiBDSource)(nil)
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
	// One shared semaphore across every sub so the GLOBAL count
	// of in-flight `bd dep list` subprocesses per refresh stays
	// bounded by markBlockedByHumanConcurrency, not M * that.
	// Without sharing, a 10-repo registry could fan out 80
	// concurrent subprocesses on each refresh tick.
	depSem := make(chan struct{}, markBlockedByHumanConcurrency)
	subs := make([]subRepo, len(clients))
	for i, c := range clients {
		dir := c.Dir
		subs[i] = subRepo{
			name:     names[i],
			src:      &BDSource{Client: c, Me: me, DepSem: depSem},
			path:     dir,
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

	// fetchSem bounds how many sub-fetches cold-start at once. It's
	// allocated per-call rather than held as a shared field (like
	// depSem): in practice only one refresh is ever in flight — the
	// model's tick chain coalesces overlapping ticks (see model.go's
	// in-flight guard) — so a per-call channel is simpler and just as
	// correct, and it can't accidentally serialise two legitimately
	// concurrent FetchWithSubErrors callers against each other.
	// Acquiring from a buffered channel never blocks past a token
	// release, and the bd call inside already respects ctx, so a
	// canceled fetch still unwinds promptly.
	fetchSem := make(chan struct{}, fetchConcurrency)

	var wg sync.WaitGroup
	for i, sub := range m.subs {
		wg.Add(1)
		go func(i int, sub subRepo) {
			defer wg.Done()
			// Per-repo cache fast path (would-you-kindly-jipr): if .beads
			// can be stat'd and a recent entry matches this preset + mtime,
			// reuse it and SKIP the bd subprocess entirely. A failed stat
			// (ok==false) disables caching for this sub — always fetch live.
			mtime, statOK := beadsMtime(sub.path)
			if statOK {
				if cached, branch, hit := m.cacheGet(sub.name, p, mtime); hit {
					results[i] = result{issues: cached}
					branches[i] = branch
					return
				}
			}
			// Each attempt acquires the semaphore for its own duration
			// so the retry stays concurrency-capped too — a retry wave
			// can't blow past fetchConcurrency.
			fetchOnce := func() ([]beads.Issue, error) {
				fetchSem <- struct{}{}
				defer func() { <-fetchSem }()
				return sub.src.Fetch(ctx, p)
			}
			issues, err := fetchOnce()
			// Retry ONCE on a transient timeout (cold Dolt engine under
			// concurrent-cold-start contention). errors.Is gates strictly
			// on ErrTimedOut so permanent failures (ErrNoWorkspace,
			// ErrBDNotFound, real bd errors) fail fast without doubling
			// latency. ctx.Err() guards against retrying a fetch the
			// caller already abandoned.
			if err != nil && errors.Is(err, beads.ErrTimedOut) && ctx.Err() == nil {
				select {
				case <-time.After(fetchRetryBackoff(i)):
				case <-ctx.Done():
				}
				if ctx.Err() == nil {
					issues, err = fetchOnce()
				}
			}
			results[i] = result{issues: issues, err: err}
			if err == nil {
				branches[i] = sub.branchFn(ctx)
				// Cache the fresh result so the next refresh can skip this
				// repo's bd call until its .beads/ changes (only when the
				// mtime is known — never cache on an unknown stat).
				if statOK {
					m.cachePut(sub.name, p, mtime, issues, branches[i])
				}
			}
		}(i, sub)
	}
	wg.Wait()

	// Cross-workspace leak guard. Precompute the registered names
	// sorted longest-first so we can do longest-prefix-match on
	// each issue ID. A naive "ID starts with sub.name + '-'" check
	// misroutes when registrations have nested prefixes — e.g.
	// subs `foo` and `foo-bar`: an issue `foo-bar-1` matches both
	// `foo-` and `foo-bar-`, and the shorter sub would mis-claim
	// it. Resolving to the LONGEST matching prefix and requiring
	// it to equal sub.name catches both classes of leak (foreign-
	// prefix and nested-prefix collision) in one rule.
	namesByLen := make([]string, len(m.subs))
	for i, sub := range m.subs {
		namesByLen[i] = sub.name
	}
	sort.Slice(namesByLen, func(i, j int) bool { return len(namesByLen[i]) > len(namesByLen[j]) })

	// longestPrefixMatch returns the longest registered sub name N
	// such that id begins with N + "-". Empty string means no
	// registered sub claims this ID.
	longestPrefixMatch := func(id string) string {
		for _, n := range namesByLen {
			if strings.HasPrefix(id, n+"-") {
				return n
			}
		}
		return ""
	}

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
		// Drop any issue whose longest-prefix-match is not THIS
		// sub. bd has been observed serving foreign workspace data
		// when a sub's `.beads/` is broken (e.g. a dead jsonl-only
		// export alongside other healthy workspaces, bd's daemon
		// then returns whichever workspace is currently warm).
		// Without this guard, those foreign rows render attributed
		// to the wrong repo, hiding a P0 bug as a duplicate-looking
		// row.
		//
		// False positive: a workspace where the user manually set
		// Name (in repos.json) to something other than the bd
		// `issue-prefix`. Surfaced as a fetch error pointing them
		// at the registry; they fix by editing the entry.
		var clean []beads.Issue
		var foreign int
		for j := range r.issues {
			if longestPrefixMatch(r.issues[j].ID) != sub.name {
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
				Err:  fmt.Errorf("%d issue(s) had foreign or nested-prefix ID (expected %q*) — bd may be serving the wrong workspace; check `wyk doctor` and ~/.config/wyk/repos.json", foreign, sub.name+"-"),
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

func (m *MultiBDSource) Reopen(ctx context.Context, i beads.Issue) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.Reopen(ctx, i)
}

func (m *MultiBDSource) SetDefer(ctx context.Context, i beads.Issue, when string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.SetDefer(ctx, i, when)
}

func (m *MultiBDSource) SetPriority(ctx context.Context, i beads.Issue, p int) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.SetPriority(ctx, i, p)
}

func (m *MultiBDSource) SetIssueType(ctx context.Context, i beads.Issue, issueType string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.SetIssueType(ctx, i, issueType)
}

func (m *MultiBDSource) SetAssignee(ctx context.Context, i beads.Issue, assignee string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.SetAssignee(ctx, i, assignee)
}

func (m *MultiBDSource) SetDescription(ctx context.Context, i beads.Issue, body string) error {
	sub, err := m.repoForIssue(i)
	if err != nil {
		return err
	}
	return sub.SetDescription(ctx, i, body)
}

// RawBD routes a raw bd invocation to the named workspace. Empty
// repo falls back to the first sub — matches Create's behaviour
// for "I'm not on any row, just run it somewhere". Returns the
// stdout bytes; bd's stderr is folded into the error.
func (m *MultiBDSource) RawBD(ctx context.Context, repo string, args []string) ([]byte, error) {
	if repo == "" {
		if len(m.subs) == 0 {
			return nil, fmt.Errorf("no registered workspaces to run bd in")
		}
		if raw, ok := m.subs[0].src.(interface {
			RawBD(context.Context, string, []string) ([]byte, error)
		}); ok {
			return raw.RawBD(ctx, "", args)
		}
		return nil, fmt.Errorf("sub %q does not support raw bd invocation", m.subs[0].name)
	}
	for _, sub := range m.subs {
		if sub.name == repo {
			if raw, ok := sub.src.(interface {
				RawBD(context.Context, string, []string) ([]byte, error)
			}); ok {
				return raw.RawBD(ctx, "", args)
			}
			return nil, fmt.Errorf("sub %q does not support raw bd invocation", repo)
		}
	}
	return nil, fmt.Errorf("repo %q not in subs", repo)
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

// ListDeps routes a `bd dep list` to the workspace that owns id,
// resolved by longest-prefix match on the registered sub names
// (the same rule FetchWithSubErrors uses to guard against foreign
// rows). Satisfies DepLister so the TUI's topological deps-sort can
// resolve edges across the union. Unlike the Mutator/Detailer
// methods, ListDeps takes a bare ID — the deps sort works off
// Issue.ID, and threading the full Repo would just duplicate the
// prefix routing the IDs already encode. An ID that no registered
// sub claims returns an error so a caller can degrade rather than
// silently mis-route.
func (m *MultiBDSource) ListDeps(ctx context.Context, id string) ([]beads.Issue, error) {
	sub := m.subForID(id)
	if sub == nil {
		return nil, fmt.Errorf("no registered workspace claims issue %q", id)
	}
	return sub.ListDeps(ctx, id)
}

// ListDependents routes a `bd dep list --direction=up` to the
// workspace that owns id, returning the issues it blocks. Mirrors
// ListDeps's longest-prefix-ID routing exactly — an ID no registered
// sub claims returns an error rather than silently mis-routing, so a
// caller can degrade.
func (m *MultiBDSource) ListDependents(ctx context.Context, id string) ([]beads.Issue, error) {
	sub := m.subForID(id)
	if sub == nil {
		return nil, fmt.Errorf("no registered workspace claims issue %q", id)
	}
	return sub.ListDependents(ctx, id)
}

// subForID returns the sub whose name is the longest registered
// prefix of id (matching N such that id begins with N+"-"). nil
// means no registered sub claims the ID. Mirrors the longest-
// prefix-match rule in FetchWithSubErrors so a nested-prefix
// registry (foo and foo-bar) routes to the more specific repo.
func (m *MultiBDSource) subForID(id string) fullSource {
	var best subRepo
	var bestLen = -1
	for _, sub := range m.subs {
		if strings.HasPrefix(id, sub.name+"-") && len(sub.name) > bestLen {
			best = sub
			bestLen = len(sub.name)
		}
	}
	if bestLen < 0 {
		return nil
	}
	return best.src
}

// Create routes the new issue to a specific sub by name. If repo is
// empty, falls back to the first sub — the registry's first repo.
// Empty repo is the multi-repo equivalent of "I'm not on any row
// right now, just file it somewhere".
func (m *MultiBDSource) Create(ctx context.Context, repo, title, assignee string) (string, error) {
	if repo == "" {
		if len(m.subs) == 0 {
			return "", fmt.Errorf("no registered workspaces to create in")
		}
		return m.subs[0].src.Create(ctx, "", title, assignee)
	}
	for _, sub := range m.subs {
		if sub.name == repo {
			return sub.src.Create(ctx, "", title, assignee)
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
