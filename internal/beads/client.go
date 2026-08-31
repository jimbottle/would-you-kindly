package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrorSink, when non-nil, is called for every bd invocation that FAILS,
// regardless of Debug. wyk wires it to an always-on, size-bounded error
// log so a failure that already happened can be inspected after the fact
// (no need to reproduce with WYK_DEBUG on). args is the bd argv without
// the -C prefix, dir is the workspace, and err is the final wrapped error
// (errors.Is-able against the sentinels below). Library default is nil =
// no I/O policy baked into the package. (would-you-kindly-w5bf.6)
var ErrorSink func(args []string, dir string, err error)

// ErrBDNotFound is returned when the bd binary is not on the PATH.
// The TUI distinguishes this from other errors so it can show a
// "bd is not installed" message instead of a generic exec failure.
var ErrBDNotFound = errors.New("bd binary not found in PATH")

// ErrNoWorkspace is returned when bd reports the working directory
// has no .beads database. The TUI surfaces this as a friendly hint
// rather than a panic.
var ErrNoWorkspace = errors.New("no bd workspace in this directory")

// ErrTimedOut is wrapped into the error returned when a bd call
// exceeds its deadline (the per-call Client.Timeout, or a shorter
// parent context). It's a distinct sentinel so callers can tell a
// TRANSIENT timeout — typically a cold embedded-Dolt engine under
// concurrent-cold-start contention, which a warm retry clears — apart
// from a permanent failure (ErrBDNotFound, ErrNoWorkspace, a real bd
// error), and retry only the former. The human-readable message still
// reports the elapsed time; errors.Is(err, ErrTimedOut) is the
// machine check.
var ErrTimedOut = errors.New("bd call timed out")

// dolt-auto-commit=on is the project-wide policy for every write the
// client issues. bd defaults to "off", and writes silently revert if
// it isn't passed — see the saved bd memory.
const autoCommitFlag = "--dolt-auto-commit=on"

// runner is the function the client uses to invoke bd. The default
// implementation shells out via os/exec; tests replace it to inspect
// the constructed argv and return synthetic stdout/error without
// touching a real bd binary.
type runner func(ctx context.Context, binary string, args []string, stdin io.Reader) (stdout, stderr []byte, err error)

// Client shells out to the bd CLI and parses its JSON output. It is
// the single seam between would-you-kindly and bd; all reads and
// writes hang their methods here.
type Client struct {
	// Binary is the bd executable name or absolute path.
	// Defaults to "bd" via NewClient.
	Binary string

	// Dir is the working directory the bd commands run in. Empty
	// means inherit the caller's cwd. Passed via bd's global -C flag.
	Dir string

	// Timeout caps a single bd invocation. Zero means no timeout.
	Timeout time.Duration

	// runner is the exec function. Zero value uses the real binary.
	runner runner
}

// defaultBDTimeout is the per-call deadline when WYK_BD_TIMEOUT is unset.
const defaultBDTimeout = 10 * time.Second

// NewClient returns a Client with sensible defaults. The per-call
// timeout can be overridden with WYK_BD_TIMEOUT — either a Go duration
// ("20s", "1m500ms") or a bare number of seconds ("20") — so large
// workspaces / slow filesystems can extend it without a recompile.
// An unparseable or non-positive value falls back to the default.
// (would-you-kindly-qhdf)
func NewClient() *Client {
	return &Client{Binary: "bd", Timeout: BDTimeoutFromEnv()}
}

// BDTimeoutFromEnv resolves the per-call bd timeout from WYK_BD_TIMEOUT,
// falling back to defaultBDTimeout. Exported so other entry points
// (e.g. doctor's per-repo probe) can honor the same override.
func BDTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("WYK_BD_TIMEOUT"))
	if raw == "" {
		return defaultBDTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultBDTimeout
}

// --- read methods --------------------------------------------------

// noLimitFlag disables bd's default result cap on the bulk read
// commands: query and list default to 50 rows, ready to 100 (bd
// 1.0.4), and --json output honors the cap the same as the human
// display. Without the explicit 0, any workspace past the cap
// silently loses rows and the TUI just looks incomplete
// (would-you-kindly-ec7z).
const noLimitFlag = "--limit=0"

// Query runs `bd query <expr> --limit=0 --json` and unmarshals the result.
func (c *Client) Query(ctx context.Context, expr string) ([]Issue, error) {
	out, err := c.run(ctx, nil, "query", expr, noLimitFlag, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// Ready runs `bd ready --limit=0 --json` — the blocker-aware view
// that PresetReady maps to. Use this rather than reproducing the
// semantics with `bd query`.
func (c *Client) Ready(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "ready", noLimitFlag, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// List runs `bd list --limit=0 --json`, returning all non-closed
// issues. This is what the "all" preset maps to — the TUI's default
// view should be "everything you might still need to do", not
// "everything ever filed". Use ListAll when closed issues must be
// included.
func (c *Client) List(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "list", noLimitFlag, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListAll runs `bd list --all --limit=0 --json`, including closed
// issues. Kept for future presets (e.g. an explicit "archived" view)
// or for callers that need the unfiltered history.
func (c *Client) ListAll(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "list", "--all", noLimitFlag, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListDeps runs `bd dep list <id> --json` and returns the issues
// that block the given id (i.e. its direct dependencies). Each
// returned Issue carries the full field set including labels, so
// callers checking "is this blocker a human task?" can answer
// without a second lookup.
//
// This single-ID form is for genuinely one-at-a-time, on-demand
// lookups (the detail view, depgraph's walk). To resolve MANY issues'
// dependencies use ListDepsBatch — bd 1.0.4 tags each batch record
// with its issue_id, so the attribution that once forced a per-issue
// fan-out is available in one call.
func (c *Client) ListDeps(ctx context.Context, id string) ([]Issue, error) {
	out, err := c.run(ctx, nil, "dep", "list", id, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// Dependency is one dependency edge: the issue that has it, the issue
// it depends on, and the edge kind ("blocks", "parent-child", …). bd
// emits these both from a batch `dep list` and inline on each issue in
// `bd list` / `bd ready`, in the same shape.
type Dependency struct {
	IssueID     string `json:"issue_id,omitempty"`
	DependsOnID string `json:"depends_on_id,omitempty"`
	Type        string `json:"type,omitempty"`
}

// ErrUnattributableDeps is returned by ListDepsBatch when bd answered
// with the single-issue shape for a MULTI-id request, leaving no way
// to tell which requested issue each row belongs to.
//
// bd picks its output shape from the number of ids it RESOLVES, not
// the number asked for: `bd dep list <valid> <bogus>` warns about the
// unresolvable one and then emits the issue shape. Returning an empty
// map there would silently drop every dependency edge in the
// workspace — and since the only caller is best-effort, every
// HUMAN-BLOCK badge with it. Callers must fall back to per-issue
// ListDeps on this error.
var ErrUnattributableDeps = errors.New("bd dep list returned an unattributable response for a multi-id request")

// depRow decodes either shape `bd dep list --json` can return. With
// MULTIPLE ids bd emits dependency records (issue_id/depends_on_id);
// with a SINGLE id it emits the full blocker issues instead. Decoding
// both through one tolerant struct means ListDepsBatch doesn't have to
// branch on arity at the JSON layer, and a future bd that settles on
// one shape still parses.
type depRow struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	ID          string `json:"id"` // set only in the single-id (issue) shape
}

// ListDepsBatch runs ONE `bd dep list id1 id2 … --json` and returns the
// direct dependencies of every requested issue, keyed by the issue they
// belong to.
//
// This replaces a per-issue fan-out. wyk used to call ListDeps once per
// candidate row because bd's batch response was said to be flat and
// unattributable — as of bd 1.0.4 that is no longer true: each record
// carries issue_id, so one subprocess answers for the whole batch.
// The old fan-out spawned 100+ concurrent `bd dep list` processes on a
// large multi-repo refresh, which saturated Dolt and made the ordinary
// per-repo `bd list` fetches miss their deadline — the "N repos failed
// to load" the user actually saw (would-you-kindly-3frr).
//
// Issues with no dependencies are simply absent from the map.
func (c *Client) ListDepsBatch(ctx context.Context, ids []string) (map[string][]Dependency, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// No --limit here: `bd dep list` (unlike list/query/ready) has no
	// such flag and cobra rejects the whole invocation with "unknown
	// flag", which markBlockedByHuman would swallow as a best-effort
	// miss — every HUMAN-BLOCK badge silently gone.
	args := append([]string{"dep", "list"}, ids...)
	args = append(args, "--json")
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	b := bytes.TrimSpace(out)
	if len(b) == 0 {
		return nil, nil
	}
	var rows []depRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("parse bd dep list json: %w", err)
	}
	// Shape-driven, NOT arity-driven: bd chooses its shape from the
	// ids it RESOLVES, so a single unresolvable id in a large batch
	// flips the whole response to the issue shape. Decide from what
	// actually came back.
	deps := make(map[string][]Dependency, len(ids))
	tagged := false
	for _, r := range rows {
		if r.IssueID == "" {
			continue
		}
		tagged = true
		deps[r.IssueID] = append(deps[r.IssueID], Dependency{
			IssueID: r.IssueID, DependsOnID: r.DependsOnID, Type: r.Type,
		})
	}
	if tagged {
		return deps, nil
	}
	// No rows at all is a complete answer — bd prints `[]` when none of
	// the requested issues has edges — NOT an unattributable one.
	// Conflating them made "no dependencies" fire the per-issue
	// fallback, turning one definitive subprocess into one per
	// candidate that each return nothing (roborev #4033).
	if len(rows) == 0 {
		return deps, nil
	}
	// Untagged rows: the issue shape. Attributable only when we asked
	// about exactly one issue; otherwise say so rather than handing
	// back a silently-empty map.
	if len(ids) != 1 {
		return nil, ErrUnattributableDeps
	}
	for _, r := range rows {
		if r.ID != "" {
			deps[ids[0]] = append(deps[ids[0]], Dependency{IssueID: ids[0], DependsOnID: r.ID})
		}
	}
	return deps, nil
}

// IssuePrefix returns the workspace's bd `issue_prefix` — the string
// every issue ID in it begins with, chosen at `bd init`.
//
// It is NOT reliably the directory name, which is where wyk's registry
// name comes from: a repo in `louisville-open-data-expenditure-bot/`
// can perfectly legitimately carry `louisville-open-data-*` IDs. Any
// code deciding "does this row belong to this workspace?" has to ask
// bd rather than infer it from the folder (would-you-kindly-qp14).
func (c *Client) IssuePrefix(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "config", "get", "issue_prefix", "--json")
	if err != nil {
		return "", err
	}
	var resp struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return "", fmt.Errorf("parse bd config get issue_prefix: %w", err)
	}
	return strings.TrimSpace(resp.Value), nil
}

// ListByIDs runs `bd list --id a,b,c --all --json` and returns those
// issues with their full field set. `--all` keeps closed issues in the
// result: a caller resolving a dependency edge needs to see the blocker
// whatever state it's in, and deciding what a closed blocker means is
// the caller's business, not this method's.
func (c *Client) ListByIDs(ctx context.Context, ids []string) ([]Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := c.run(ctx, nil, "list", "--id", strings.Join(ids, ","), "--all", noLimitFlag, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListDependents runs `bd dep list <id> --direction=up --json` and
// returns the issues that the given id BLOCKS (i.e. its direct
// dependents — the reverse edge of ListDeps). The JSON shape is the
// same flat []Issue, so it parses through parseIssues unchanged. Like
// ListDeps it's per-issue rather than batched and carries the full
// field set, so the detail view can render `ID — title (status)`
// without a second lookup.
func (c *Client) ListDependents(ctx context.Context, id string) ([]Issue, error) {
	out, err := c.run(ctx, nil, "dep", "list", id, "--direction=up", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// Show runs `bd show <id> --json` and returns the single Issue,
// which carries the full field set (description AND notes — the
// list/query endpoints drop one or the other for efficiency).
// Used by the TUI's detail view to enrich the row on enter.
func (c *Client) Show(ctx context.Context, id string) (Issue, error) {
	out, err := c.run(ctx, nil, "show", id, "--json")
	if err != nil {
		return Issue{}, err
	}
	issues, err := parseIssues(out)
	if err != nil {
		return Issue{}, err
	}
	if len(issues) == 0 {
		return Issue{}, fmt.Errorf("bd show %s: no issue returned", id)
	}
	return issues[0], nil
}

// GetDescription returns the issue's current description via `bd show`.
// Used by pkg/handoff to preserve a prior description across a handoff
// (would-you-kindly-e2a8). Read-only despite living near the writes.
func (c *Client) GetDescription(ctx context.Context, id string) (string, error) {
	i, err := c.Show(ctx, id)
	if err != nil {
		return "", err
	}
	return i.Description, nil
}

// --- write methods -------------------------------------------------

// Close closes the given issue. Every write passes --dolt-auto-commit=on;
// without it bd's default 'off' policy leaves the change in the working
// set so a subsequent read still returns the unclosed issue.
func (c *Client) Close(ctx context.Context, id string) error {
	_, err := c.run(ctx, nil, "close", id, autoCommitFlag)
	return err
}

// CloseMany closes every id in ONE `bd close id1 id2 …` invocation.
// bd accepts a list, and each subprocess pays Dolt's per-call latency,
// so the TUI's bulk close (mark N rows, a, y) went from N round-trips
// to one per workspace (would-you-kindly-cexj). A nil ids is a no-op;
// an error means the batch as a whole failed — bd reports per-ID
// problems on stderr, which the wrapped error carries.
func (c *Client) CloseMany(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]string, 0, len(ids)+2)
	args = append(args, "close")
	args = append(args, ids...)
	args = append(args, autoCommitFlag)
	_, err := c.run(ctx, nil, args...)
	return err
}

// Reopen sets a closed issue back to status=open via `bd reopen`,
// which clears closed_at and emits a Reopened event. Used by the
// TUI's `u` undo-last-close key — preferred over `update --status
// open` because the explicit subcommand preserves the audit trail
// (an external `bd audit` walk can tell "this issue was closed
// then reopened" from "this issue was opened in the first place").
func (c *Client) Reopen(ctx context.Context, id string) error {
	_, err := c.run(ctx, nil, "reopen", id, autoCommitFlag)
	return err
}

// SetDefer hides an issue from `bd ready` until the given date.
// when accepts the same formats `bd update --defer` does: relative
// offsets (`+1d`, `+1w`, `+2mo`), natural-language anchors
// (`tomorrow`, `next monday`), and absolute dates (`2026-06-15`).
// Empty when clears the defer. wyk passes the value through
// verbatim; bd is the source of truth on what parses.
func (c *Client) SetDefer(ctx context.Context, id, when string) error {
	_, err := c.run(ctx, nil, "update", id, "--defer", when, autoCommitFlag)
	return err
}

// SetPriority sets the issue's priority (0–4, 0 = highest). The
// caller is responsible for clamping into range; an out-of-range
// value is rejected by bd.
func (c *Client) SetPriority(ctx context.Context, id string, priority int) error {
	_, err := c.run(ctx, nil, "update", id, "--priority", fmt.Sprintf("%d", priority), autoCommitFlag)
	return err
}

// SetDescription rewrites the issue's description via `bd update
// --description-file <tmp>`. Multi-line content + arbitrary
// characters (quotes, backticks, $()) flow through unescaped
// because the body is read from a file, not parsed as a shell
// arg. The caller is responsible for cleaning up the temp file
// — actually we own it here so the caller has no leakage
// surface. Empty body is honored as a deliberate clear.
func (c *Client) SetDescription(ctx context.Context, id, body string) error {
	f, err := os.CreateTemp("", "wyk-desc-*.md")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	_, err = c.run(ctx, nil, "update", id, "--description-file", f.Name(), autoCommitFlag)
	return err
}

// SetAssignee changes the issue's owner via `bd update --assignee`.
// Empty assignee clears the owner — bd accepts this when the user
// wants to mark a row as un-owned (the inverse of wyk's
// QuickAdd-requires-owner rule, which only governs creation).
func (c *Client) SetAssignee(ctx context.Context, id, assignee string) error {
	_, err := c.run(ctx, nil, "update", id, "--assignee", assignee, autoCommitFlag)
	return err
}

// SetIssueType changes the issue's type via `bd update --type`.
// Caller is responsible for passing one of bd's accepted values
// (task / bug / feature / chore / epic / decision / spike /
// story / milestone); bd rejects unknown types.
func (c *Client) SetIssueType(ctx context.Context, id, issueType string) error {
	_, err := c.run(ctx, nil, "update", id, "--type", issueType, autoCommitFlag)
	return err
}

// RawRun invokes bd with the supplied args verbatim and returns
// stdout. Used by the TUI's `:bd <args>` command palette entry so
// the user can run arbitrary bd subcommands without leaving the
// TUI. No --dolt-auto-commit injection — the user owns the args.
// The stderr stream is folded into the returned error on failure,
// matching how every other Client write-method surfaces bd's own
// diagnostics to the user.
func (c *Client) RawRun(ctx context.Context, args []string) ([]byte, error) {
	out, err := c.run(ctx, nil, args...)
	return out, err
}

// CreateOptions configures `bd create` invocations.
type CreateOptions struct {
	Title     string
	Labels    []string // applied as --labels=a,b
	Priority  string   // empty means bd's default ("2")
	IssueType string   // task / bug / feature / chore / epic / decision / spike / story / milestone
	// Assignee is the owner the new issue should land on (bd's
	// `--assignee` flag). wyk enforces non-empty assignee for
	// every TUI-filed issue — orphan tasks are the failure mode
	// we want to make impossible at creation rather than chase
	// down later.
	Assignee string
}

// Create runs `bd create <title> --silent` with the given options
// and returns the new issue's ID. `--silent` makes bd emit only the
// ID on stdout — clean for programmatic chaining.
func (c *Client) Create(ctx context.Context, opts CreateOptions) (string, error) {
	args := []string{"create", opts.Title, "--silent", autoCommitFlag}
	if len(opts.Labels) > 0 {
		args = append(args, "--labels="+strings.Join(opts.Labels, ","))
	}
	if opts.Priority != "" {
		args = append(args, "--priority="+opts.Priority)
	}
	if opts.IssueType != "" {
		args = append(args, "--type="+opts.IssueType)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// AddLabel adds a label to an issue (`bd label add <id> <label>`).
func (c *Client) AddLabel(ctx context.Context, id, label string) error {
	_, err := c.run(ctx, nil, "label", "add", id, label, autoCommitFlag)
	return err
}

// RemoveLabel removes a label from an issue (`bd label remove <id> <label>`).
func (c *Client) RemoveLabel(ctx context.Context, id, label string) error {
	_, err := c.run(ctx, nil, "label", "remove", id, label, autoCommitFlag)
	return err
}

// Note appends a note to an issue (`bd note <id> <text>`). The text is
// passed as a single argv element so multi-word / multi-line content
// doesn't need shell quoting.
func (c *Client) Note(ctx context.Context, id, text string) error {
	_, err := c.run(ctx, nil, "note", id, text, autoCommitFlag)
	return err
}

// UpdateDescription replaces an issue's description. The description
// is piped to bd via stdin (`bd update <id> --stdin`) so callers can
// pass arbitrarily long runbooks without hitting argv length limits
// or shell quoting concerns. Empty strings are rejected by bd unless
// --allow-empty-description is set; we pass it so the agent skill
// can choose to clear a description if it wants.
func (c *Client) UpdateDescription(ctx context.Context, id, description string) error {
	_, err := c.run(ctx, strings.NewReader(description),
		"update", id, "--stdin", "--allow-empty-description", autoCommitFlag)
	return err
}

// --- runner plumbing ----------------------------------------------

// run executes a bd subcommand and returns its stdout, classifying
// "not found" and "no workspace" errors so callers can render
// targeted messages. stdin may be nil for commands that don't need it.
func (c *Client) run(ctx context.Context, stdin io.Reader, args ...string) (out []byte, err error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	full := args
	if c.Dir != "" {
		full = append([]string{"-C", c.Dir}, args...)
	}

	r := c.runner
	if r == nil {
		r = execRunner
	}
	// Capture the start time so the timeout message can report
	// the actual elapsed duration. Reporting c.Timeout would lie
	// when a shorter parent deadline fires first (the effective
	// deadline is min(c.Timeout, parent.Deadline)).
	start := time.Now()
	// Debug tracing (would-you-kindly-2vyt, w5bf.4): when the default slog
	// logger is enabled at Debug level (wyk configures it from WYK_DEBUG /
	// WYK_LOG_FILE / WYK_LOG_LEVEL), record every bd invocation's argv +
	// elapsed + error as a structured event. Gated on Enabled so it costs
	// nothing — not even the deferred closure — when debug logging is off.
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		defer func() {
			slog.Debug("bd invocation",
				"argv", strings.Join(args, " "),
				"dir", c.Dir,
				"elapsed", time.Since(start).Round(time.Millisecond).String(),
				"err", err)
		}()
	}
	// Persist every failure to the always-on error sink (independent of
	// Debug), so a field failure leaves a trace. Single deferred hook so
	// all the error returns below are covered uniformly with the final
	// wrapped err. (would-you-kindly-w5bf.6)
	if ErrorSink != nil {
		defer func() {
			if err != nil {
				ErrorSink(args, c.Dir, err)
			}
		}()
	}
	stdout, stderr, err := r(ctx, c.Binary, full, stdin)
	if err == nil {
		return stdout, nil
	}

	// Context-driven cancellation surfaces from cmd.Run as a
	// SIGKILL-shaped *exec.ExitError whose .Error() is "signal:
	// killed" — exec.CommandContext kills the process when ctx
	// fires. Without this branch the user-visible error reads
	// "signal: killed", looking like bd crashed when in fact OUR
	// timeout fired. Check ctx.Err() before the exec error so the
	// real cause wins.
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			elapsed := time.Since(start).Round(time.Millisecond)
			// Wrap ErrTimedOut so the fetch fan-out can retry a
			// transient cold-start timeout (errors.Is) while keeping
			// the elapsed-time message for the user.
			return nil, fmt.Errorf("bd %s: timed out after %s: %w", strings.Join(args, " "), elapsed, ErrTimedOut)
		case errors.Is(ctxErr, context.Canceled):
			return nil, fmt.Errorf("bd %s: canceled", strings.Join(args, " "))
		}
	}

	// exec.ErrNotFound surfaces as *exec.Error with Err == ErrNotFound;
	// be liberal about how we recognise it.
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return nil, ErrBDNotFound
	}
	// bd writes its error as JSON on stderr; look at the combined
	// stderr+stdout to be robust to either channel.
	errOut := strings.TrimSpace(string(stderr))
	if errOut == "" {
		errOut = strings.TrimSpace(string(stdout))
	}
	if isNoWorkspaceErr(errOut) {
		return nil, ErrNoWorkspace
	}
	if errOut == "" {
		errOut = err.Error()
	}
	return nil, fmt.Errorf("bd %s: %s", strings.Join(args, " "), errOut)
}

// execRunner is the default runner: shells out to the real bd binary.
func execRunner(ctx context.Context, binary string, args []string, stdin io.Reader) (stdoutBytes, stderrBytes []byte, err error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// isNoWorkspaceErr matches the various ways bd has phrased the
// "you're not in a beads-initialised directory" error across
// versions. Conservative substring matching is fine — bd's other
// error messages don't collide with these phrasings.
func isNoWorkspaceErr(s string) bool {
	for _, marker := range []string{
		"no beads project found",
		"no beads database found",
		"no .beads",
		"no workspace",
		"could not find a .beads",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// parseIssues unmarshals bd JSON output. bd emits an empty array
// "[]" for no results — handled naturally by encoding/json. Some
// commands prepend whitespace or a header on stderr; only stdout
// is fed here, so we can decode strictly.
func parseIssues(b []byte) ([]Issue, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, nil
	}
	var issues []Issue
	if err := json.Unmarshal(b, &issues); err != nil {
		return nil, fmt.Errorf("parse bd json: %w", err)
	}
	return issues, nil
}
