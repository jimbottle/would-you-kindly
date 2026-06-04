package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// stubLabelAdder records AddLabel calls and can be primed to fail, so
// applyIdentityRouting's real-write path is exercised without bd.
type stubLabelAdder struct {
	calls [][2]string // {id, label}
	err   error
}

func (s *stubLabelAdder) AddLabel(_ context.Context, id, label string) error {
	s.calls = append(s.calls, [2]string{id, label})
	return s.err
}

// captureStdouterr runs fn with both os.Stdout and os.Stderr redirected
// and returns (stdout, stderr).
func captureStdouterr(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	outCh, errCh := make(chan string), make(chan string)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()
	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	return <-outCh, <-errCh
}

func TestApplyIdentityRouting_AddsLabelAndConfirms(t *testing.T) {
	s := &stubLabelAdder{}
	out, errOut := captureStdouterr(t, func() {
		applyIdentityRouting(context.Background(), s, "wyk-9", "claude")
	})
	if len(s.calls) != 1 || s.calls[0] != [2]string{"wyk-9", "src:agent:claude"} {
		t.Fatalf("AddLabel calls = %v, want one {wyk-9, src:agent:claude}", s.calls)
	}
	if !strings.Contains(out, `routed wyk-9 to identity "claude"`) {
		t.Errorf("stdout should confirm routing; got %q", out)
	}
	if errOut != "" {
		t.Errorf("no stderr expected on success; got %q", errOut)
	}
}

func TestApplyIdentityRouting_FailureIsNonFatalWarning(t *testing.T) {
	s := &stubLabelAdder{err: errors.New("bd boom")}
	out, errOut := captureStdouterr(t, func() {
		applyIdentityRouting(context.Background(), s, "wyk-9", "claude")
	})
	// Still attempts the label, but reports the failure on stderr and
	// emits NO "routed" confirmation (the handoff itself already landed).
	if len(s.calls) != 1 {
		t.Fatalf("AddLabel should be attempted once; got %d calls", len(s.calls))
	}
	if strings.Contains(out, "routed") {
		t.Errorf("no routing confirmation expected on failure; got stdout %q", out)
	}
	if !strings.Contains(errOut, "identity routing failed") || !strings.Contains(errOut, "bd boom") {
		t.Errorf("stderr should warn non-fatally with the bd error; got %q", errOut)
	}
}

func TestValidateIdentity(t *testing.T) {
	valid := []string{"alice", "claude", "agent-2", "a", "x9", "team-blue-1"}
	for _, n := range valid {
		if err := validateIdentity(n); err != nil {
			t.Errorf("validateIdentity(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", "-leading", "Alice", "has space", "a:b", "under_score"}
	for _, n := range invalid {
		if err := validateIdentity(n); err == nil {
			t.Errorf("validateIdentity(%q) = nil, want error", n)
		}
	}
}

func TestResolveIdentity(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(identityEnvVar, "fromenv")
		got, err := resolveIdentity("fromflag")
		if err != nil || got != "fromflag" {
			t.Errorf("resolveIdentity(flag) = %q, %v; want fromflag, nil", got, err)
		}
	})
	t.Run("env when flag empty", func(t *testing.T) {
		t.Setenv(identityEnvVar, "fromenv")
		got, err := resolveIdentity("")
		if err != nil || got != "fromenv" {
			t.Errorf("resolveIdentity('') = %q, %v; want fromenv, nil", got, err)
		}
	})
	t.Run("unset is collective (empty, no error)", func(t *testing.T) {
		t.Setenv(identityEnvVar, "")
		got, err := resolveIdentity("")
		if err != nil || got != "" {
			t.Errorf("resolveIdentity('') with no env = %q, %v; want '', nil", got, err)
		}
	})
	t.Run("malformed is an error, not a silent collective fallback", func(t *testing.T) {
		t.Setenv(identityEnvVar, "")
		if _, err := resolveIdentity("Bad Name"); err == nil {
			t.Error("resolveIdentity('Bad Name') = nil error; want validation error")
		}
	})
}

func TestInboxQueryFor(t *testing.T) {
	if got := inboxQueryFor(""); got != inboxQuery {
		t.Errorf("inboxQueryFor('') = %q, want the collective query", got)
	}
	got := inboxQueryFor("alice")
	want := "label=src:agent:alice AND NOT label=human AND NOT label=agent-handoff AND status!=closed"
	if got != want {
		t.Errorf("inboxQueryFor(alice) = %q, want %q", got, want)
	}
	if identityLabel("alice") != "src:agent:alice" {
		t.Errorf("identityLabel(alice) = %q, want src:agent:alice", identityLabel("alice"))
	}
}

func TestInbox_MalformedIdentityIsUsageError(t *testing.T) {
	t.Setenv(identityEnvVar, "")
	if code := runInbox([]string{"-identity", "Bad Name"}); code != 64 {
		t.Errorf("runInbox(-identity 'Bad Name') = %d, want 64 (usage error)", code)
	}
}
