package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ErrBDNotFound is returned when the bd binary is not on the PATH.
// The TUI distinguishes this from other errors so it can show a
// "bd is not installed" message instead of a generic exec failure.
var ErrBDNotFound = errors.New("bd binary not found in PATH")

// ErrNoWorkspace is returned when bd reports the working directory
// has no .beads database. The TUI surfaces this as a friendly hint
// rather than a panic.
var ErrNoWorkspace = errors.New("no bd workspace in this directory")

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

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{Binary: "bd", Timeout: 10 * time.Second}
}

// --- read methods --------------------------------------------------

// Query runs `bd query <expr> --json` and unmarshals the result.
func (c *Client) Query(ctx context.Context, expr string) ([]Issue, error) {
	out, err := c.run(ctx, nil, "query", expr, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// Ready runs `bd ready --json` — the blocker-aware view that
// PresetReady maps to. Use this rather than reproducing the
// semantics with `bd query`.
func (c *Client) Ready(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "ready", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// List runs `bd list --json`, returning all non-closed issues. This
// is what the "all" preset maps to — the TUI's default view should
// be "everything you might still need to do", not "everything ever
// filed". Use ListAll when closed issues must be included.
func (c *Client) List(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "list", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListAll runs `bd list --all --json`, including closed issues.
// Kept for future presets (e.g. an explicit "archived" view) or for
// callers that need the unfiltered history.
func (c *Client) ListAll(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, nil, "list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListDeps runs `bd dep list <id> --json` and returns the issues
// that block the given id (i.e. its direct dependencies). Each
// returned Issue carries the full field set including labels, so
// callers checking "is this blocker a human task?" can answer
// without a second lookup. Multi-ID batching is intentionally not
// exposed: bd's batch response is flat and doesn't tag which
// blocker belongs to which queried id, so callers must do
// per-issue lookups to maintain the per-row attribution the TUI's
// HUMAN-BLOCK badge depends on.
func (c *Client) ListDeps(ctx context.Context, id string) ([]Issue, error) {
	out, err := c.run(ctx, nil, "dep", "list", id, "--json")
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

// --- write methods -------------------------------------------------

// Close closes the given issue. Every write passes --dolt-auto-commit=on;
// without it bd's default 'off' policy leaves the change in the working
// set so a subsequent read still returns the unclosed issue.
func (c *Client) Close(ctx context.Context, id string) error {
	_, err := c.run(ctx, nil, "close", id, autoCommitFlag)
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
func (c *Client) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
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
	stdout, stderr, err := r(ctx, c.Binary, full, stdin)
	if err == nil {
		return stdout, nil
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
