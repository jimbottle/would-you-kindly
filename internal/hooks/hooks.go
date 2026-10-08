// Package hooks runs the user-configured handoff hook: an external
// command wyk invokes when an issue changes hands between an agent
// and a human through wyk. It is the generic seam that lets any task
// manager (a to-do app, a calendar, a chat channel) mirror the issues
// a human now owns, without wyk knowing anything about that manager.
//
// The contract, in one paragraph: wyk pipes one JSON Payload to the
// command's stdin, with the event name mirrored into the environment
// for one-line shell scripts. The command may print a JSON Result
// ({"ref": …, "url": …}) on stdout; wyk records ref in bd's own
// external_ref field and url in a bd note, and sends both back on
// later events for the same issue so the script can update rather
// than duplicate. A non-zero exit is reported to the caller with the
// script's stderr; wyk never rolls back the bd write that triggered
// the hook. The full contract lives in docs/HOOKS.md.
//
// The hook fires ONLY for the human-labelled side of the contract:
// when `human` is added (handoff), removed (bounce), or the issue is
// closed while carrying it (close). Agent-owned issues never reach
// it. A ping event exists for `wyk doctor` so a misconfigured script
// fails at setup time rather than at the first real handoff.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// SchemaVersion is the payload shape version. Bump it only for an
// incompatible change (a renamed or removed field); additive fields
// keep the version. Scripts should check it before trusting the shape.
const SchemaVersion = 1

// Event names the transition that fired the hook.
type Event string

const (
	// EventHandoff fires after the `human` label is added through wyk
	// (wyk handoff, wyk handoff -create, the TUI's H key).
	EventHandoff Event = "handoff"
	// EventBounce fires after the `human` label is removed through wyk
	// — the human sent the issue back to the agent.
	EventBounce Event = "bounce"
	// EventClose fires after a human-labelled issue is closed through
	// wyk (the TUI's close verb, the post-commit auto-close).
	EventClose Event = "close"
	// EventPing carries no issue. `wyk doctor` sends it so the script
	// can prove it starts, parses the payload, and reaches whatever it
	// talks to. A script should answer with exit 0 and no side effects.
	EventPing Event = "ping"
)

// AllEvents is every event the dispatcher can send, in the order the
// contract documents them. It doubles as the default event filter.
var AllEvents = []Event{EventHandoff, EventBounce, EventClose, EventPing}

// IsValidEvent reports whether e is one of AllEvents.
func IsValidEvent(e Event) bool {
	for _, k := range AllEvents {
		if k == e {
			return true
		}
	}
	return false
}

// Actor names which wyk surface performed the transition.
const (
	ActorCLI  = "cli"  // wyk handoff / wyk hook dispatch
	ActorTUI  = "tui"  // the interactive TUI
	ActorHook = "hook" // the post-commit auto-close
)

// RunbookKind classifies the runbook by the heading it carries, the
// same test `wyk handoff` applies before accepting one.
const (
	RunbookTask     = "task"     // has "## Steps": directions the human follows
	RunbookQuestion = "question" // has "## Question": something the human answers
)

// Repo identifies the bd workspace the issue lives in.
type Repo struct {
	// Name is the registry's short label (the directory basename by
	// default); the TUI's Repo column shows the same string.
	Name string `json:"name"`
	// Path is the absolute workspace root (the directory holding .beads).
	Path string `json:"path"`
	// Prefix is bd's issue_prefix for the workspace (e.g. "wyk" for
	// wyk-42). Empty when bd could not be asked.
	Prefix string `json:"prefix,omitempty"`
}

// External is the reference a previous hook run recorded for this
// issue, echoed back so the script can find the task it created.
type External struct {
	// Ref is the opaque reference the script returned (stored in bd's
	// external_ref field, e.g. "basicdo:task:abc123").
	Ref string `json:"ref,omitempty"`
	// URL is the human-facing link the script returned, if any.
	URL string `json:"url,omitempty"`
}

// Payload is the JSON document piped to the hook's stdin. Every field
// wyk knows about the transition is here; scripts take what they need.
type Payload struct {
	SchemaVersion int    `json:"schema_version"`
	Event         Event  `json:"event"`
	Actor         string `json:"actor"`
	// Timestamp is when wyk fired the hook (UTC).
	Timestamp time.Time `json:"timestamp"`
	// WykVersion is the `wyk --version` string of the binary that
	// fired the hook, so a script can refuse a payload from a build
	// it has not been tested against.
	WykVersion string `json:"wyk_version,omitempty"`

	// Issue is the full bd issue as wyk last read it (labels, notes,
	// due_at, external_ref, dependencies, timestamps…). Nil for ping.
	Issue *beads.Issue `json:"issue,omitempty"`
	// Runbook is the issue description at the moment of the event —
	// for a handoff, the runbook the human will follow. RunbookKind
	// is "task" or "question" by heading, or "" when neither applies.
	Runbook     string `json:"runbook,omitempty"`
	RunbookKind string `json:"runbook_kind,omitempty"`

	// Repo is the workspace. Always set, even for ping (the cwd's).
	Repo Repo `json:"repo"`

	// Identity is the agent identity the handoff was routed to
	// (`-identity` / $WYK_AGENT_IDENTITY), Session the Claude Code
	// session id that filed it, Note the `-note` text, when present.
	Identity string `json:"identity,omitempty"`
	Session  string `json:"session,omitempty"`
	Note     string `json:"note,omitempty"`

	// External is what an earlier run of the hook recorded for this
	// issue (nil on the first handoff). A script receiving it should
	// update that task rather than create another.
	External *External `json:"external,omitempty"`
}

// Result is the optional JSON object a hook prints on stdout. An
// empty stdout is a valid "nothing to record" answer.
type Result struct {
	Ref string `json:"ref,omitempty"`
	URL string `json:"url,omitempty"`
}

// Config is the resolved hook configuration. The zero value means
// "no hook": Enabled reports false and Dispatch is a no-op.
type Config struct {
	// Command is run through `sh -c`, so it may carry arguments and
	// shell expansions ("node ~/bin/basicdo-hook.mjs").
	Command string
	// Timeout bounds one invocation. Zero means DefaultTimeout.
	Timeout time.Duration
	// Events the hook wants. Nil or empty means AllEvents.
	Events []Event
}

// DefaultTimeout is the per-invocation bound when the config sets none.
// Long enough for a cold Node start plus one HTTP round trip; short
// enough that a hung script cannot stall an interactive handoff.
const DefaultTimeout = 15 * time.Second

// Enabled reports whether a command is configured at all.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Command) != "" }

// Wants reports whether the hook is enabled AND subscribed to e. Ping
// is never filtered out: doctor must be able to probe a hook whose
// events list is narrowed to, say, handoff only.
func (c Config) Wants(e Event) bool {
	if !c.Enabled() {
		return false
	}
	if e == EventPing || len(c.Events) == 0 {
		return true
	}
	for _, k := range c.Events {
		if k == e {
			return true
		}
	}
	return false
}

// EffectiveTimeout returns Timeout, or DefaultTimeout when unset.
func (c Config) EffectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// ErrTimedOut is wrapped into the error when the hook exceeds its
// timeout, so callers can name the remedy (raise hooks.handoff.timeout_seconds).
var ErrTimedOut = errors.New("handoff hook timed out")

// ErrMalformedResult is wrapped when stdout is non-empty but is not a
// JSON object. It is an error rather than a silent drop because a
// lost ref is exactly what makes a later handoff create a duplicate.
var ErrMalformedResult = errors.New("handoff hook printed something that is not a JSON result")

// ErrInterrupted is wrapped when wyk itself received SIGINT/SIGTERM
// while the hook was running. The hook's whole process group was killed
// first, so nothing of it survives wyk; the mirror is simply not made.
var ErrInterrupted = errors.New("handoff hook interrupted")

// ExecError is returned when the hook exits non-zero. Stderr is
// captured so the caller can show the script's own explanation.
type ExecError struct {
	ExitCode int
	Stderr   string
}

func (e *ExecError) Error() string {
	msg := fmt.Sprintf("handoff hook exited %d", e.ExitCode)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// Runner executes the hook command. The default shells out through
// `sh -c`; tests inject one that records the call and returns a
// synthetic stdout/stderr/error without a real process. env holds the
// WYK_HOOK_* variables to ADD to the inherited environment.
type Runner func(ctx context.Context, command string, stdin []byte, env []string) (stdout, stderr []byte, err error)

// Dispatcher runs one Config. The zero value is unusable; build one
// with the Config and leave Runner nil for the real shell-out.
type Dispatcher struct {
	Config Config
	Runner Runner
}

// Dispatch runs the hook for p. It returns fired=false (and no error)
// when the hook is disabled or not subscribed to p.Event — callers
// can always call it and let the config decide. When fired, Result is
// what the script printed (zero when it printed nothing). Every
// failure is returned as an error; the caller decides how loud to be,
// since the bd write that triggered the hook has already happened.
func (d *Dispatcher) Dispatch(ctx context.Context, p Payload) (res Result, fired bool, err error) {
	if !d.Config.Wants(p.Event) {
		return Result{}, false, nil
	}
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now().UTC()
	}
	stdin, err := json.Marshal(p)
	if err != nil {
		return Result{}, true, fmt.Errorf("encode hook payload: %w", err)
	}
	stdin = append(stdin, '\n')

	env := []string{
		"WYK_HOOK_EVENT=" + string(p.Event),
		fmt.Sprintf("WYK_HOOK_SCHEMA_VERSION=%d", p.SchemaVersion),
		"WYK_REPO=" + p.Repo.Name,
		"WYK_REPO_PATH=" + p.Repo.Path,
	}
	if p.Issue != nil {
		env = append(env, "WYK_ISSUE_ID="+p.Issue.ID)
	}

	timeout := d.Config.EffectiveTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r := d.Runner
	if r == nil {
		r = shellRunner
	}
	stdout, stderr, err := r(ctx, d.Config.Command, stdin, env)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, true, fmt.Errorf("%w after %s", ErrTimedOut, timeout)
		}
		if errors.Is(err, ErrInterrupted) {
			return Result{}, true, err
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Result{}, true, &ExecError{ExitCode: ee.ExitCode(), Stderr: string(stderr)}
		}
		var xe *ExecError
		if errors.As(err, &xe) {
			return Result{}, true, err
		}
		return Result{}, true, fmt.Errorf("run handoff hook: %w", err)
	}
	res, err = ParseResult(stdout)
	if err != nil {
		return Result{}, true, err
	}
	return res, true, nil
}

// ParseResult decodes the hook's stdout. Empty (or whitespace-only)
// stdout is a valid empty Result. Otherwise the LAST non-empty line
// must be a JSON object: scripts commonly log progress to stdout
// before printing their answer, and this lets them without forcing
// every log line through stderr. Anything else is ErrMalformedResult.
func ParseResult(stdout []byte) (Result, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return Result{}, nil
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	last := bytes.TrimSpace(lines[len(lines)-1])
	var res Result
	if err := json.Unmarshal(last, &res); err != nil {
		snippet := string(last)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		return Result{}, fmt.Errorf("%w: %q", ErrMalformedResult, snippet)
	}
	return res, nil
}

// shellRunner is the default Runner: `sh -c <command>` with the
// payload on stdin and the WYK_HOOK_* variables layered over the
// inherited environment. Stdout and stderr are captured separately
// so a Result can be parsed and an error can quote the script.
//
// Going through the shell is deliberate and safe here: the ONLY
// string that reaches `sh -c` is the operator's own configured
// command (config.json or $WYK_HANDOFF_HOOK), exactly like a git
// hook or a shell alias. Nothing from the issue — title, runbook,
// labels — is ever interpolated into it; all of that travels on
// stdin as JSON and in WYK_* variables, which the shell does not
// re-parse. Keep it that way when touching this function.
//
// Two things make the run END when it should:
//
//   - The timeout has to end the whole run, not just `sh`. With
//     buffered stdout/stderr, Run waits until every writer has closed
//     the pipes; a child the shell forked (`cd x && node hook.mjs`,
//     `a | b`, a script that daemonises) would keep them open after sh
//     was killed and hang `wyk handoff` past the deadline. So the hook
//     runs in its own process group and the group is killed on cancel
//     (isolateProcessGroup, Unix only), and WaitDelay caps the wait
//     after cancel so even a stray grandchild cannot hold Run open.
//   - Its own process group means the terminal's Ctrl-C no longer
//     reaches the hook on its own. So the run is also cancelled on
//     SIGINT/SIGTERM to wyk: the same cancel kills the group, and the
//     caller gets ErrInterrupted rather than a half-finished mirror
//     whose ref wyk is no longer around to record.
func shellRunner(ctx context.Context, command string, stdin []byte, env []string) ([]byte, []byte, error) {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := exec.CommandContext(sigCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	isolateProcessGroup(cmd)
	cmd.WaitDelay = waitDelayAfterCancel
	err := cmd.Run()
	if err != nil && sigCtx.Err() != nil && ctx.Err() == nil {
		return out.Bytes(), errb.Bytes(), fmt.Errorf("%w (the hook and its children were killed)", ErrInterrupted)
	}
	return out.Bytes(), errb.Bytes(), err
}

// waitDelayAfterCancel bounds how long Run may block on the hook's pipes
// after the context is cancelled (exec.Cmd.WaitDelay). Short: by then the
// process group has been killed and anything still writing is a stray.
const waitDelayAfterCancel = 2 * time.Second

// ClassifyRunbook returns RunbookTask when the runbook carries a
// "## Steps" heading, RunbookQuestion for "## Question", else "". It
// mirrors the shape check `wyk handoff` applies, so the hook sees
// the same classification the human will.
func ClassifyRunbook(runbook string) string {
	for _, line := range strings.Split(runbook, "\n") {
		h := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(h, "## Steps"):
			return RunbookTask
		case strings.HasPrefix(h, "## Question"):
			return RunbookQuestion
		}
	}
	return ""
}
