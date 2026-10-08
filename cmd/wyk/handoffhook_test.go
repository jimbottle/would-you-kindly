package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/hooks"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// stubHookClient is the bd slice fireHandoffHook touches, recording
// writes so a test can assert what landed on the issue.
type stubHookClient struct {
	issue       beads.Issue
	showErr     error
	refSet      string
	refErr      error
	notes       []string
	prefix      string
	prefixCalls int
}

func (s *stubHookClient) Show(context.Context, string) (beads.Issue, error) {
	return s.issue, s.showErr
}
func (s *stubHookClient) SetExternalRef(_ context.Context, _ string, ref string) error {
	s.refSet = ref
	return s.refErr
}
func (s *stubHookClient) Note(_ context.Context, _ string, text string) error {
	s.notes = append(s.notes, text)
	return nil
}
func (s *stubHookClient) IssuePrefix(context.Context) (string, error) {
	s.prefixCalls++
	return s.prefix, nil
}

// stubHookRunner records the payload the dispatcher sent and answers
// with canned stdout/err.
type stubHookRunner struct {
	payload hooks.Payload
	called  bool
	stdout  string
	err     error
}

func (r *stubHookRunner) run(_ context.Context, _ string, stdin []byte, _ []string) ([]byte, []byte, error) {
	r.called = true
	_ = json.Unmarshal(stdin, &r.payload)
	return []byte(r.stdout), []byte("script said no\n"), r.err
}

func newTestDispatcher(r *stubHookRunner, events ...hooks.Event) *hooks.Dispatcher {
	return &hooks.Dispatcher{Config: hooks.Config{Command: "stub", Events: events}, Runner: r.run}
}

func baseInput() handoffHookInput {
	return handoffHookInput{
		Event: hooks.EventHandoff, Actor: hooks.ActorCLI, ID: "wyk-42",
		Runbook: "## Why\nx\n## Steps\n1. do", Identity: "alice", Note: "see PR 7",
	}
}

func TestFireHandoffHook_DisabledIsSilentNoOp(t *testing.T) {
	c := &stubHookClient{}
	var out, errw bytes.Buffer
	code := fireHandoffHook(context.Background(), c, &hooks.Dispatcher{}, baseInput(), &out, &errw)
	if code != 0 || out.Len() != 0 || errw.Len() != 0 {
		t.Fatalf("disabled: code=%d out=%q err=%q", code, out.String(), errw.String())
	}
	if c.prefixCalls != 0 {
		t.Error("bd was consulted although no hook is configured")
	}
}

func TestFireHandoffHook_HappyPathRecordsRefAndNote(t *testing.T) {
	t.Setenv(sessionEnvVar, "sess-1")
	c := &stubHookClient{
		issue:  beads.Issue{ID: "wyk-42", Title: "Rotate creds", Priority: 1, Labels: []string{"human", "src:agent"}},
		prefix: "wyk",
	}
	r := &stubHookRunner{stdout: `{"ref":"basicdo:task:abc","url":"http://x/t/abc"}`}
	var out, errw bytes.Buffer
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r), baseInput(), &out, &errw)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errw.String())
	}
	if !r.called {
		t.Fatal("hook not run")
	}
	p := r.payload
	if p.Event != hooks.EventHandoff || p.Actor != hooks.ActorCLI || p.SchemaVersion != hooks.SchemaVersion {
		t.Errorf("payload header = %+v", p)
	}
	if p.Issue == nil || p.Issue.Title != "Rotate creds" || len(p.Issue.Labels) != 2 {
		t.Errorf("issue not embedded from bd show: %+v", p.Issue)
	}
	if p.Runbook != baseInput().Runbook || p.RunbookKind != hooks.RunbookTask {
		t.Errorf("runbook = %q kind = %q", p.Runbook, p.RunbookKind)
	}
	if p.Identity != "alice" || p.Session != "sess-1" || p.Note != "see PR 7" {
		t.Errorf("identity/session/note = %q/%q/%q", p.Identity, p.Session, p.Note)
	}
	if p.Repo.Prefix != "wyk" || p.Repo.Path == "" || p.Repo.Name == "" {
		t.Errorf("repo = %+v", p.Repo)
	}
	if p.External != nil {
		t.Errorf("first handoff must not carry an external ref: %+v", p.External)
	}
	if p.WykVersion == "" {
		t.Error("wyk_version missing")
	}
	// What landed on the issue.
	if c.refSet != "basicdo:task:abc" {
		t.Errorf("external_ref written = %q", c.refSet)
	}
	if len(c.notes) != 1 || !strings.Contains(c.notes[0], "http://x/t/abc") {
		t.Errorf("notes = %v, want one with the url", c.notes)
	}
	if !strings.Contains(out.String(), "hook (handoff): http://x/t/abc") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestFireHandoffHook_EchoesExistingRefAndSkipsRewrite(t *testing.T) {
	c := &stubHookClient{issue: beads.Issue{ID: "wyk-42", ExternalRef: "basicdo:task:abc", Description: "## Question\nwhich?"}}
	r := &stubHookRunner{stdout: `{"ref":"basicdo:task:abc","url":"http://x/t/abc"}`}
	var out, errw bytes.Buffer
	in := baseInput()
	in.Runbook = "" // replay path: use the description bd has
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r), in, &out, &errw)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	if r.payload.External == nil || r.payload.External.Ref != "basicdo:task:abc" {
		t.Errorf("existing ref not echoed: %+v", r.payload.External)
	}
	if r.payload.Runbook != "## Question\nwhich?" || r.payload.RunbookKind != hooks.RunbookQuestion {
		t.Errorf("runbook fallback = %q / %q", r.payload.Runbook, r.payload.RunbookKind)
	}
	if c.refSet != "" {
		t.Errorf("unchanged ref was rewritten: %q", c.refSet)
	}
	if len(c.notes) != 0 {
		t.Errorf("duplicate note on an already-linked issue: %v", c.notes)
	}
}

func TestFireHandoffHook_FailureReportsReplayAndExit3(t *testing.T) {
	c := &stubHookClient{issue: beads.Issue{ID: "wyk-42"}}
	r := &stubHookRunner{err: &hooks.ExecError{ExitCode: 1, Stderr: "BASICDO_API_KEY is not set"}}
	var out, errw bytes.Buffer
	in := baseInput()
	in.Dir = "/tmp/repo"
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r), in, &out, &errw)
	if code != exitHookFailed {
		t.Fatalf("exit %d, want %d", code, exitHookFailed)
	}
	for _, want := range []string{
		"handoff hook failed for handoff wyk-42 (bd is unaffected",
		"BASICDO_API_KEY is not set",
		"wyk hook dispatch -C /tmp/repo handoff wyk-42",
	} {
		if !strings.Contains(errw.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errw.String())
		}
	}
	if c.refSet != "" || len(c.notes) != 0 {
		t.Error("a failed hook must record nothing on the issue")
	}
}

func TestFireHandoffHook_RefWriteFailureIsAHookFailure(t *testing.T) {
	c := &stubHookClient{issue: beads.Issue{ID: "wyk-42"}, refErr: errors.New("dolt locked")}
	r := &stubHookRunner{stdout: `{"ref":"r1"}`}
	var out, errw bytes.Buffer
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r), baseInput(), &out, &errw)
	if code != exitHookFailed || !strings.Contains(errw.String(), "dolt locked") || !strings.Contains(errw.String(), "wyk hook dispatch handoff wyk-42") {
		t.Fatalf("exit %d stderr %q", code, errw.String())
	}
}

func TestFireHandoffHook_ShowFailureStillFires(t *testing.T) {
	c := &stubHookClient{showErr: errors.New("bd exploded")}
	r := &stubHookRunner{stdout: `{}`}
	var out, errw bytes.Buffer
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r), baseInput(), &out, &errw)
	if code != 0 || !r.called {
		t.Fatalf("exit %d called=%v", code, r.called)
	}
	if r.payload.Issue == nil || r.payload.Issue.ID != "wyk-42" {
		t.Errorf("minimal issue missing: %+v", r.payload.Issue)
	}
	if !strings.Contains(errw.String(), "minimal issue") {
		t.Errorf("show failure not reported: %q", errw.String())
	}
}

func TestFireHandoffHook_EventFilterRespected(t *testing.T) {
	c := &stubHookClient{issue: beads.Issue{ID: "wyk-42"}}
	r := &stubHookRunner{}
	var out, errw bytes.Buffer
	in := baseInput()
	in.Event = hooks.EventBounce
	code := fireHandoffHook(context.Background(), c, newTestDispatcher(r, hooks.EventHandoff), in, &out, &errw)
	if code != 0 || r.called {
		t.Fatalf("bounce with handoff-only filter: exit %d called=%v", code, r.called)
	}
}

func TestResolveHandoffHookConfig(t *testing.T) {
	t.Setenv(handoffHookEnvVar, "")
	if c := resolveHandoffHookConfig(wykconfig.Config{}); c.Enabled() {
		t.Errorf("empty config enabled: %+v", c)
	}
	cfg := wykconfig.Config{Hooks: wykconfig.HooksConfig{Handoff: &wykconfig.HandoffHook{
		Command: "node hook.mjs", TimeoutSeconds: 30, Events: []string{"handoff", "close"},
	}}}
	c := resolveHandoffHookConfig(cfg)
	if c.Command != "node hook.mjs" || c.EffectiveTimeout().Seconds() != 30 || len(c.Events) != 2 || !c.Wants(hooks.EventClose) || c.Wants(hooks.EventBounce) {
		t.Errorf("resolved = %+v", c)
	}
	t.Setenv(handoffHookEnvVar, "echo override")
	if c := resolveHandoffHookConfig(cfg); c.Command != "echo override" || len(c.Events) != 2 {
		t.Errorf("env override: %+v", c)
	}
	if c := resolveHandoffHookConfig(wykconfig.Config{}); c.Command != "echo override" || !c.Wants(hooks.EventHandoff) {
		t.Errorf("env alone should enable with all events: %+v", c)
	}
}

// The handoff dry run must disclose the hook: an agent should know an
// external task will be created before committing the human to it.
func TestHandoff_DryRunNamesTheHook(t *testing.T) {
	clearAmbientIdentity(t)
	t.Setenv(handoffHookEnvVar, "")
	prev := newHandoffHookDispatcher
	t.Cleanup(func() { newHandoffHookDispatcher = prev })
	newHandoffHookDispatcher = func() *hooks.Dispatcher {
		return &hooks.Dispatcher{Config: hooks.Config{Command: "node ~/bin/hook.mjs"}}
	}
	path := writeRunbook(t, "## Steps\n1. step one")
	out := captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-file", path, "wyk-42"}); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "would run handoff hook: node ~/bin/hook.mjs") {
		t.Errorf("dry run did not name the hook:\n%s", out)
	}
	// -create -due shows in the plan too.
	out = captureHandoffStdout(t, func() {
		if code := runHandoff([]string{"-dry-run", "-create", "Rotate creds", "-due", "+2d", "-file", path}); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if !strings.Contains(out, "would set due: +2d") {
		t.Errorf("dry run did not show -due:\n%s", out)
	}
}

func TestHandoff_DueRequiresCreate(t *testing.T) {
	clearAmbientIdentity(t)
	withSilencedStderr(t)
	path := writeRunbook(t, "## Steps\n1. step one")
	if code := runHandoff([]string{"-due", "+1d", "-file", path, "wyk-42"}); code != 64 {
		t.Errorf("-due without -create: exit %d, want 64", code)
	}
}

func TestHookDispatch_Usage(t *testing.T) {
	withSilencedStderr(t)
	for _, args := range [][]string{
		{},
		{"reopen", "wyk-1"},
		{"handoff"},
		{"ping", "wyk-1"},
	} {
		if code := runHookDispatch(args); code != 64 {
			t.Errorf("%v: exit %d, want 64", args, code)
		}
	}
	// No hook configured: say so, exit 0, touch nothing.
	t.Setenv(handoffHookEnvVar, "")
	prev := newHandoffHookDispatcher
	t.Cleanup(func() { newHandoffHookDispatcher = prev })
	newHandoffHookDispatcher = func() *hooks.Dispatcher { return &hooks.Dispatcher{} }
	if code := runHookDispatch([]string{"handoff", "wyk-1"}); code != 0 {
		t.Errorf("unconfigured dispatch: exit %d, want 0", code)
	}
}
