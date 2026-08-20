package beads

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every invocation so tests can assert the exact
// argv (and stdin, where relevant) the client constructed. It is the
// only mechanism the write-method tests use to avoid touching real bd.
type fakeRunner struct {
	calls  []fakeCall
	stdout []byte
	stderr []byte
	err    error
}

type fakeCall struct {
	args  []string
	stdin string
}

func (f *fakeRunner) run(_ context.Context, _ string, args []string, stdin io.Reader) ([]byte, []byte, error) {
	c := fakeCall{args: append([]string(nil), args...)}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		c.stdin = string(b)
	}
	f.calls = append(f.calls, c)
	return f.stdout, f.stderr, f.err
}

func newTestClient(r *fakeRunner) *Client {
	return &Client{Binary: "bd", Timeout: 0, runner: r.run}
}

func TestBulkReads_DisableBDResultLimit(t *testing.T) {
	// bd 1.0.4 caps query/list at 50 rows and ready at 100 BY DEFAULT,
	// and --json honors the cap — so without an explicit --limit=0 any
	// workspace past the cap silently loses rows (would-you-kindly-ec7z).
	// Pin the flag on every bulk read so a refactor can't quietly
	// reintroduce the truncation.
	cases := []struct {
		name string
		call func(c *Client) error
		want []string
	}{
		{"Query", func(c *Client) error { _, err := c.Query(context.Background(), "status=open"); return err },
			[]string{"query", "status=open", "--limit=0", "--json"}},
		{"Ready", func(c *Client) error { _, err := c.Ready(context.Background()); return err },
			[]string{"ready", "--limit=0", "--json"}},
		{"List", func(c *Client) error { _, err := c.List(context.Background()); return err },
			[]string{"list", "--limit=0", "--json"}},
		{"ListAll", func(c *Client) error { _, err := c.ListAll(context.Background()); return err },
			[]string{"list", "--all", "--limit=0", "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{stdout: []byte("[]")}
			c := newTestClient(r)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("want 1 bd call, got %d", len(r.calls))
			}
			if got := strings.Join(r.calls[0].args, " "); got != strings.Join(tc.want, " ") {
				t.Errorf("argv = %q, want %q", got, strings.Join(tc.want, " "))
			}
		})
	}
}

func TestParseIssues_Empty(t *testing.T) {
	cases := []string{"", "  ", "[]", "[]\n"}
	for _, in := range cases {
		got, err := parseIssues([]byte(in))
		if err != nil {
			t.Fatalf("parseIssues(%q): %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("parseIssues(%q): want 0 issues, got %d", in, len(got))
		}
	}
}

func TestParseIssues_OneAndHumanLabel(t *testing.T) {
	in := []byte(`[
		{
			"id": "wyk-1",
			"title": "do a thing",
			"description": "the instructions",
			"status": "open",
			"priority": 2,
			"issue_type": "task",
			"labels": ["human", "src:agent"],
			"created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z"
		}
	]`)
	got, err := parseIssues(in)
	if err != nil {
		t.Fatalf("parseIssues: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	if !got[0].IsHuman() {
		t.Error("issue should be flagged human")
	}
	if got[0].ID != "wyk-1" {
		t.Errorf("ID = %q, want wyk-1", got[0].ID)
	}
}

func TestParseIssues_ToleratesUnknownFields(t *testing.T) {
	// Forward-compatibility: a future bd may add fields. Decoding
	// must succeed and populate everything it does recognise.
	in := []byte(`[
		{
			"id": "wyk-2",
			"title": "future field issue",
			"new_field_added_by_future_bd": {"shape": "unknown"},
			"labels": ["human"]
		}
	]`)
	got, err := parseIssues(in)
	if err != nil {
		t.Fatalf("parseIssues with unknown fields: %v", err)
	}
	if len(got) != 1 || got[0].ID != "wyk-2" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if !got[0].IsHuman() {
		t.Error("issue should be flagged human")
	}
}

func TestParseIssues_BadJSONReturnsError(t *testing.T) {
	if _, err := parseIssues([]byte(`{not json`)); err == nil {
		t.Error("expected error for malformed json")
	}
}

// --- write-method command-construction tests ----------------------

func TestClose_BuildsExpectedArgv(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.Close(context.Background(), "wyk-42"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := []string{"close", "wyk-42", "--dolt-auto-commit=on"}
	gotCall(t, r, want, "")
}

func TestAddLabel_BuildsExpectedArgv(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.AddLabel(context.Background(), "wyk-42", "human"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	want := []string{"label", "add", "wyk-42", "human", "--dolt-auto-commit=on"}
	gotCall(t, r, want, "")
}

func TestRemoveLabel_BuildsExpectedArgv(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.RemoveLabel(context.Background(), "wyk-42", "human"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	want := []string{"label", "remove", "wyk-42", "human", "--dolt-auto-commit=on"}
	gotCall(t, r, want, "")
}

func TestNote_PassesTextAsSingleArg(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	// Text with spaces and a newline must survive as a single argv
	// element, not get split on whitespace.
	text := "rotated on 2026-05-28\nclient ID stored in 1Password"
	if err := c.Note(context.Background(), "wyk-42", text); err != nil {
		t.Fatalf("Note: %v", err)
	}
	want := []string{"note", "wyk-42", text, "--dolt-auto-commit=on"}
	gotCall(t, r, want, "")
}

func TestUpdateDescription_PipesBodyViaStdin(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	body := "1. step one\n2. step two\n3. step three"
	if err := c.UpdateDescription(context.Background(), "wyk-42", body); err != nil {
		t.Fatalf("UpdateDescription: %v", err)
	}
	want := []string{"update", "wyk-42", "--stdin", "--allow-empty-description", "--dolt-auto-commit=on"}
	gotCall(t, r, want, body)
}

func TestDirGlobalFlagIsPrefixed(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	c.Dir = "/tmp/elsewhere"
	if err := c.Close(context.Background(), "wyk-1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := []string{"-C", "/tmp/elsewhere", "close", "wyk-1", "--dolt-auto-commit=on"}
	gotCall(t, r, want, "")
}

func TestWriteSurfacesBDError(t *testing.T) {
	// When bd exits non-zero, the client should bubble the stderr in
	// the error message rather than swallowing it.
	r := &fakeRunner{
		stderr: []byte(`{"error":"issue not found","schema_version":1}`),
		err:    errors.New("exit status 1"),
	}
	c := newTestClient(r)
	err := c.Close(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error from failed close")
	}
	if !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("error should include bd's stderr; got %q", err.Error())
	}
}

func TestWriteSurfacesNoWorkspaceAsTypedErr(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte(`{"error":"no beads project found","schema_version":1}`),
		err:    errors.New("exit status 1"),
	}
	c := newTestClient(r)
	err := c.AddLabel(context.Background(), "wyk-1", "human")
	if !errors.Is(err, ErrNoWorkspace) {
		t.Errorf("expected ErrNoWorkspace, got %v", err)
	}
}

func TestNoWorkspacePhrasings(t *testing.T) {
	// bd phrases the not-a-workspace error differently depending on
	// how it was invoked: with -C it says "no beads project found"
	// (JSON on stderr), but resolving from the bare cwd (bd 1.0.4)
	// it prints plain text "Error: no beads database found" plus a
	// Hint block. Missing the latter made `wyk --probe` exit 1 with
	// the raw error instead of the documented exit 2.
	phrasings := []string{
		`{"error":"no beads project found","schema_version":1}`,
		"Error: no beads database found\nHint: run 'bd where' to inspect the resolved workspace, or 'bd init' to create a new database",
		"no .beads directory found",
		"could not find a .beads directory",
	}
	for _, out := range phrasings {
		r := &fakeRunner{
			stderr: []byte(out),
			err:    errors.New("exit status 1"),
		}
		c := newTestClient(r)
		_, err := c.Query(context.Background(), "status=open")
		if !errors.Is(err, ErrNoWorkspace) {
			t.Errorf("stderr %q: expected ErrNoWorkspace, got %v", out, err)
		}
	}
}

// blockingRunner waits for ctx to fire, then returns the
// SIGKILL-shaped error string exec.CommandContext would produce
// when it kills its child. Lets the run-timeout test exercise the
// ctx.Err() classification branch without spawning a real process.
type blockingRunner struct{}

func (b *blockingRunner) run(ctx context.Context, _ string, _ []string, _ io.Reader) ([]byte, []byte, error) {
	<-ctx.Done()
	return nil, nil, errors.New("signal: killed")
}

func TestRunClassifiesContextDeadlineAsTimeout(t *testing.T) {
	// User-facing bug: when our 10s timeout fires on a slow `bd
	// list --json`, the error surfaced as "signal: killed" because
	// exec.CommandContext SIGKILLs the child on ctx.Done. The user
	// can't tell that's OUR timeout, not bd crashing. The fix
	// checks ctx.Err() before exec's error and synthesizes a
	// timed-out classification. This test pins it.
	c := &Client{Binary: "bd", Timeout: 30 * time.Millisecond, runner: (&blockingRunner{}).run}
	_, err := c.Query(context.Background(), "status=open")
	if err == nil {
		t.Fatal("expected timeout error from blocked runner")
	}
	if !strings.Contains(err.Error(), "timed out after ") {
		t.Errorf("expected 'timed out after ...' classification; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("error should not leak 'signal: killed'; got %q", err.Error())
	}
	// The timeout must wrap ErrTimedOut so the fetch fan-out can
	// errors.Is it and retry a transient cold-start timeout.
	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("timeout error should wrap ErrTimedOut; got %q", err.Error())
	}
}

func TestRunClassifiesParentCancelation(t *testing.T) {
	// When the PARENT context is canceled (e.g. TUI quit while a
	// Fetch is in flight), the exec error is still "signal:
	// killed". The classification branch must catch
	// context.Canceled too, not only DeadlineExceeded.
	c := &Client{Binary: "bd", Timeout: 0, runner: (&blockingRunner{}).run}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Query(ctx, "status=open")
	if err == nil {
		t.Fatal("expected canceled error from blocked runner")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected 'canceled' classification; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("error should not leak 'signal: killed'; got %q", err.Error())
	}
}

// gotCall asserts the fake runner saw exactly one invocation matching
// wantArgs (in order) and the given stdin content.
func gotCall(t *testing.T, r *fakeRunner, wantArgs []string, wantStdin string) {
	t.Helper()
	if len(r.calls) != 1 {
		t.Fatalf("want exactly 1 bd call, got %d: %+v", len(r.calls), r.calls)
	}
	got := r.calls[0]
	if !equalStrings(got.args, wantArgs) {
		t.Errorf("argv mismatch\n  want: %v\n  got:  %v", wantArgs, got.args)
	}
	if got.stdin != wantStdin {
		t.Errorf("stdin mismatch\n  want: %q\n  got:  %q", wantStdin, got.stdin)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBDTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want time.Duration
	}{
		{"unset uses default", "", defaultBDTimeout},
		{"go duration", "20s", 20 * time.Second},
		{"bare seconds", "30", 30 * time.Second},
		{"compound duration", "1m500ms", time.Minute + 500*time.Millisecond},
		{"garbage falls back", "soon", defaultBDTimeout},
		{"zero falls back", "0", defaultBDTimeout},
		{"negative falls back", "-5", defaultBDTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WYK_BD_TIMEOUT", c.set)
			if got := BDTimeoutFromEnv(); got != c.want {
				t.Errorf("BDTimeoutFromEnv() with %q = %v, want %v", c.set, got, c.want)
			}
		})
	}
}

// TestErrorSink_FiresOnFailure pins would-you-kindly-w5bf.6: a failed bd
// invocation calls ErrorSink once with the argv, dir, and wrapped error.
func TestErrorSink_FiresOnFailure(t *testing.T) {
	orig := ErrorSink
	t.Cleanup(func() { ErrorSink = orig })
	var gotDir string
	var gotErr error
	calls := 0
	ErrorSink = func(_ []string, dir string, err error) {
		calls++
		gotDir, gotErr = dir, err
	}
	c := newTestClient(&fakeRunner{stderr: []byte(`{"error":"boom"}`), err: errors.New("exit status 1")})
	c.Dir = "/tmp/x"
	if _, err := c.Query(context.Background(), "status!=closed"); err == nil {
		t.Fatal("expected a query error")
	}
	if calls != 1 {
		t.Fatalf("ErrorSink fired %d times, want 1", calls)
	}
	if gotDir != "/tmp/x" || gotErr == nil {
		t.Fatalf("ErrorSink got dir=%q err=%v", gotDir, gotErr)
	}
}

// TestErrorSink_NotCalledOnSuccess: a successful call records nothing.
func TestErrorSink_NotCalledOnSuccess(t *testing.T) {
	orig := ErrorSink
	t.Cleanup(func() { ErrorSink = orig })
	calls := 0
	ErrorSink = func([]string, string, error) { calls++ }
	c := newTestClient(&fakeRunner{stdout: []byte("[]")})
	if _, err := c.Query(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("ErrorSink should not fire on success; fired %d", calls)
	}
}
