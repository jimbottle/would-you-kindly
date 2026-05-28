package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Client shells out to the bd CLI and parses its JSON output. It is
// the single seam between would-you-kindly and bd; later phases that
// add write actions hang their methods here.
type Client struct {
	// Binary is the bd executable name or absolute path.
	// Defaults to "bd" via NewClient.
	Binary string

	// Dir is the working directory the bd commands run in. Empty
	// means inherit the caller's cwd. Passed via bd's global -C flag.
	Dir string

	// Timeout caps a single bd invocation. Zero means no timeout.
	Timeout time.Duration
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{Binary: "bd", Timeout: 10 * time.Second}
}

// Query runs `bd query <expr> --json` and unmarshals the result.
func (c *Client) Query(ctx context.Context, expr string) ([]Issue, error) {
	out, err := c.run(ctx, "query", expr, "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// Ready runs `bd ready --json` — the blocker-aware view that
// PresetReady maps to. Use this rather than reproducing the
// semantics with `bd query`.
func (c *Client) Ready(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, "ready", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// ListAll runs `bd list --all --json` for the "all" preset, which
// must include closed and other states the default `list` omits.
func (c *Client) ListAll(ctx context.Context) ([]Issue, error) {
	out, err := c.run(ctx, "list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	return parseIssues(out)
}

// run executes a bd subcommand and returns its stdout, classifying
// "not found" and "no workspace" errors so callers can render
// targeted messages.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	full := args
	if c.Dir != "" {
		full = append([]string{"-C", c.Dir}, args...)
	}

	cmd := exec.CommandContext(ctx, c.Binary, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// exec.ErrNotFound surfaces as *exec.Error with Err == ErrNotFound;
		// be liberal about how we recognise it.
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return nil, ErrBDNotFound
		}
		// bd writes its error as JSON on stderr; look at the combined
		// stderr+stdout to be robust to either channel.
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = strings.TrimSpace(stdout.String())
		}
		if isNoWorkspaceErr(errOut) {
			return nil, ErrNoWorkspace
		}
		if errOut == "" {
			errOut = err.Error()
		}
		return nil, fmt.Errorf("bd %s: %s", strings.Join(args, " "), errOut)
	}
	return stdout.Bytes(), nil
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
