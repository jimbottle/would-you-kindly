package beads

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeRunner records every invocation so tests can assert the exact
// argv (and stdin, where relevant) the client constructed. It is the
// only mechanism the write-method tests use to avoid touching real bd.
type fakeRunner struct {
	calls   []fakeCall
	stdout  []byte
	stderr  []byte
	err     error
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
