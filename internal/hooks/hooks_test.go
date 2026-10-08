package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// recordingRunner captures the one call Dispatch makes and answers
// with canned output, so no test needs a real shell or script.
type recordingRunner struct {
	called  bool
	command string
	stdin   []byte
	env     []string
	stdout  []byte
	stderr  []byte
	err     error
	// block, when set, makes the runner wait for ctx to expire — the
	// timeout test.
	block bool
}

func (r *recordingRunner) run(ctx context.Context, command string, stdin []byte, env []string) ([]byte, []byte, error) {
	r.called = true
	r.command = command
	r.stdin = stdin
	r.env = env
	if r.block {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return r.stdout, r.stderr, r.err
}

func samplePayload() Payload {
	return Payload{
		Event: EventHandoff,
		Actor: ActorCLI,
		Issue: &beads.Issue{ID: "wyk-42", Title: "Rotate the staging DB password", Priority: 1, Labels: []string{"human", "src:agent"}},
		Repo:  Repo{Name: "wyk", Path: "/tmp/wyk", Prefix: "wyk"},
	}
}

func TestDispatch_DisabledIsNoOp(t *testing.T) {
	r := &recordingRunner{}
	d := &Dispatcher{Config: Config{}, Runner: r.run}
	res, fired, err := d.Dispatch(context.Background(), samplePayload())
	if err != nil || fired || res != (Result{}) {
		t.Fatalf("disabled hook: res=%+v fired=%v err=%v; want zero, false, nil", res, fired, err)
	}
	if r.called {
		t.Fatal("runner must not be invoked when no command is configured")
	}
}

func TestDispatch_EventFilterSkipsUnsubscribed(t *testing.T) {
	r := &recordingRunner{}
	d := &Dispatcher{Config: Config{Command: "x", Events: []Event{EventHandoff}}, Runner: r.run}
	p := samplePayload()
	p.Event = EventBounce
	if _, fired, err := d.Dispatch(context.Background(), p); err != nil || fired {
		t.Fatalf("bounce with handoff-only filter: fired=%v err=%v; want false, nil", fired, err)
	}
	if r.called {
		t.Fatal("runner invoked for an unsubscribed event")
	}
	// ping is never filtered: doctor must be able to probe a narrowed hook.
	p.Event = EventPing
	p.Issue = nil
	if _, fired, err := d.Dispatch(context.Background(), p); err != nil || !fired {
		t.Fatalf("ping with handoff-only filter: fired=%v err=%v; want true, nil", fired, err)
	}
}

func TestDispatch_HappyPathPipesPayloadAndParsesResult(t *testing.T) {
	r := &recordingRunner{stdout: []byte("creating…\n{\"ref\":\"basicdo:task:abc\",\"url\":\"http://x/t/abc\"}\n")}
	d := &Dispatcher{Config: Config{Command: "node hook.mjs"}, Runner: r.run}
	res, fired, err := d.Dispatch(context.Background(), samplePayload())
	if err != nil || !fired {
		t.Fatalf("Dispatch: fired=%v err=%v", fired, err)
	}
	if res.Ref != "basicdo:task:abc" || res.URL != "http://x/t/abc" {
		t.Fatalf("result = %+v", res)
	}
	if r.command != "node hook.mjs" {
		t.Errorf("command = %q", r.command)
	}
	// The payload on stdin is one JSON document with the schema version
	// and timestamp filled in, and the issue embedded whole.
	var got Payload
	if err := json.Unmarshal(r.stdin, &got); err != nil {
		t.Fatalf("stdin is not JSON: %v\n%s", err, r.stdin)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.Timestamp.IsZero() {
		t.Error("timestamp not filled in")
	}
	if got.Issue == nil || got.Issue.ID != "wyk-42" || len(got.Issue.Labels) != 2 {
		t.Errorf("issue not carried whole: %+v", got.Issue)
	}
	if got.Repo.Name != "wyk" || got.Repo.Prefix != "wyk" {
		t.Errorf("repo = %+v", got.Repo)
	}
	// Env mirrors for one-line shell scripts.
	for _, want := range []string{"WYK_HOOK_EVENT=handoff", "WYK_ISSUE_ID=wyk-42", "WYK_REPO=wyk", "WYK_REPO_PATH=/tmp/wyk", "WYK_HOOK_SCHEMA_VERSION=1"} {
		found := false
		for _, e := range r.env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("env missing %q; got %v", want, r.env)
		}
	}
}

func TestDispatch_NonZeroExitCarriesStderr(t *testing.T) {
	// A real ExitError from a real process, so errors.As matches the
	// same type the shell runner produces.
	exitErr := exec.Command("sh", "-c", "exit 7").Run()
	var ee *exec.ExitError
	if !errors.As(exitErr, &ee) {
		t.Skip("could not produce an ExitError on this platform")
	}
	r := &recordingRunner{stderr: []byte("BASICDO_API_KEY is not set\n"), err: exitErr}
	d := &Dispatcher{Config: Config{Command: "x"}, Runner: r.run}
	_, fired, err := d.Dispatch(context.Background(), samplePayload())
	if !fired {
		t.Fatal("fired = false")
	}
	var xe *ExecError
	if !errors.As(err, &xe) {
		t.Fatalf("err = %v (%T), want *ExecError", err, err)
	}
	if xe.ExitCode != 7 || !strings.Contains(xe.Stderr, "BASICDO_API_KEY") {
		t.Errorf("ExecError = %+v", xe)
	}
	if !strings.Contains(err.Error(), "exited 7") || !strings.Contains(err.Error(), "BASICDO_API_KEY") {
		t.Errorf("Error() = %q should name the exit code and quote stderr", err.Error())
	}
}

func TestDispatch_TimeoutIsErrTimedOut(t *testing.T) {
	r := &recordingRunner{block: true}
	d := &Dispatcher{Config: Config{Command: "x", Timeout: 20 * time.Millisecond}, Runner: r.run}
	_, fired, err := d.Dispatch(context.Background(), samplePayload())
	if !fired || !errors.Is(err, ErrTimedOut) {
		t.Fatalf("fired=%v err=%v, want true, ErrTimedOut", fired, err)
	}
}

func TestDispatch_MalformedStdoutIsAnError(t *testing.T) {
	r := &recordingRunner{stdout: []byte("created task 12")}
	d := &Dispatcher{Config: Config{Command: "x"}, Runner: r.run}
	_, fired, err := d.Dispatch(context.Background(), samplePayload())
	if !fired || !errors.Is(err, ErrMalformedResult) {
		t.Fatalf("fired=%v err=%v, want true, ErrMalformedResult", fired, err)
	}
}

func TestParseResult(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Result
		wantErr bool
	}{
		{"empty", "", Result{}, false},
		{"whitespace", " \n\t\n", Result{}, false},
		{"object", `{"ref":"a","url":"b"}`, Result{Ref: "a", URL: "b"}, false},
		{"last line wins", "log 1\nlog 2\n{\"ref\":\"z\"}\n", Result{Ref: "z"}, false},
		{"empty object", `{}`, Result{}, false},
		{"not json", "ok", Result{}, true},
		{"array", "[1]", Result{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseResult([]byte(c.in))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestConfig_WantsAndTimeout(t *testing.T) {
	var zero Config
	if zero.Enabled() || zero.Wants(EventHandoff) || zero.Wants(EventPing) {
		t.Error("zero config must be disabled for every event, ping included")
	}
	c := Config{Command: " x "}
	if !c.Wants(EventClose) {
		t.Error("no events list means all events")
	}
	if c.EffectiveTimeout() != DefaultTimeout {
		t.Errorf("EffectiveTimeout = %s, want default", c.EffectiveTimeout())
	}
	c.Timeout = time.Second
	if c.EffectiveTimeout() != time.Second {
		t.Errorf("EffectiveTimeout = %s, want 1s", c.EffectiveTimeout())
	}
	for _, e := range AllEvents {
		if !IsValidEvent(e) {
			t.Errorf("IsValidEvent(%q) = false", e)
		}
	}
	if IsValidEvent("reopen") {
		t.Error("unknown event accepted")
	}
}

func TestClassifyRunbook(t *testing.T) {
	if got := ClassifyRunbook("## Why\nx\n## Steps\n1. go"); got != RunbookTask {
		t.Errorf("task runbook classified as %q", got)
	}
	if got := ClassifyRunbook("## Why\nx\n  ## Question\nwhich?"); got != RunbookQuestion {
		t.Errorf("question runbook classified as %q", got)
	}
	if got := ClassifyRunbook("please do the thing"); got != "" {
		t.Errorf("bare runbook classified as %q", got)
	}
}

// TestShellRunner_RealProcess exercises the default runner once, end to
// end, so the sh -c / stdin / env plumbing is covered by something
// other than the stub. It relies only on sh and cat.
func TestShellRunner_RealProcess(t *testing.T) {
	d := &Dispatcher{Config: Config{Command: `test "$WYK_HOOK_EVENT" = handoff && cat >/dev/null && printf '{"ref":"%s"}' "$WYK_ISSUE_ID"`}}
	res, fired, err := d.Dispatch(context.Background(), samplePayload())
	if err != nil || !fired {
		t.Fatalf("fired=%v err=%v", fired, err)
	}
	if res.Ref != "wyk-42" {
		t.Errorf("ref = %q, want the issue id echoed from the env", res.Ref)
	}
	d = &Dispatcher{Config: Config{Command: `echo nope >&2; exit 3`}}
	_, _, err = d.Dispatch(context.Background(), samplePayload())
	var xe *ExecError
	if !errors.As(err, &xe) || xe.ExitCode != 3 || !strings.Contains(xe.Stderr, "nope") {
		t.Errorf("real non-zero exit: err = %v", err)
	}
}

// wykconfig validates `hooks.handoff.events` against its own copy of the
// event names (it must stay dependency-free); this is the pin that keeps
// that copy equal to what the dispatcher actually sends.
func TestEventNamesMatchConfigVocabulary(t *testing.T) {
	var ours []string
	for _, e := range AllEvents {
		ours = append(ours, string(e))
	}
	if strings.Join(ours, ",") != strings.Join(wykconfig.HandoffHookEvents, ",") {
		t.Fatalf("hooks.AllEvents = %v but wykconfig.HandoffHookEvents = %v; keep them identical", ours, wykconfig.HandoffHookEvents)
	}
}
