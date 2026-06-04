package main

import "testing"

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
