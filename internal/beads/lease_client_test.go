package beads

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMetadata_UnmarshalCoercesNonStrings(t *testing.T) {
	// bd stores whatever JSON a user hands --metadata; one numeric value
	// must not sink the parse of the whole list payload.
	raw := `[{"id":"x-1","title":"t","status":"open","priority":2,"metadata":{"wyk.lease.owner":"a","retries":3,"flag":true,"obj":{"k":"v"}}}]`
	issues, err := parseIssues([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := Metadata{"wyk.lease.owner": "a", "retries": "3", "flag": "true", "obj": `{"k":"v"}`}
	if !reflect.DeepEqual(issues[0].Metadata, want) {
		t.Fatalf("metadata=%v, want %v", issues[0].Metadata, want)
	}
	var m Metadata
	if err := json.Unmarshal([]byte("null"), &m); err != nil || m != nil {
		t.Fatalf("null: %v %v", m, err)
	}
}

func TestIssue_ParsesStartedAtAndMetadata(t *testing.T) {
	raw := `[{"id":"x-1","title":"t","status":"in_progress","priority":2,"assignee":"agent-a","started_at":"2026-10-08T23:13:50Z","metadata":{"wyk.lease.until":"2026-10-09T01:00:00Z"}}]`
	issues, err := parseIssues([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	i := issues[0]
	if i.StartedAt.IsZero() || i.Metadata["wyk.lease.until"] != "2026-10-09T01:00:00Z" {
		t.Fatalf("got started_at=%v metadata=%v", i.StartedAt, i.Metadata)
	}
	// Marshal round-trip keeps both and elides them when empty.
	b, _ := json.Marshal(Issue{ID: "y"})
	if s := string(b); contains(s, "started_at") || contains(s, "metadata") {
		t.Fatalf("empty issue must elide started_at/metadata: %s", s)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestClaim_ArgvCarriesActorClaimAndSortedMetadata(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	c.Dir = "/ws"
	meta := Metadata{"wyk.lease.until": "2026-10-09T01:00:00Z", "wyk.lease.owner": "claude-a"}
	if err := c.Claim(context.Background(), "x-1", "claude-a", meta); err != nil {
		t.Fatal(err)
	}
	want := []string{"-C", "/ws", "--actor", "claude-a", "update", "x-1", "--claim",
		"--set-metadata", "wyk.lease.owner=claude-a",
		"--set-metadata", "wyk.lease.until=2026-10-09T01:00:00Z",
		"--dolt-auto-commit=on"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
}

func TestClaim_ContentionIsClassified(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 1"), stderr: []byte("Error claiming x-1: issue already claimed by agent-b")}
	c := newTestClient(r)
	err := c.Claim(context.Background(), "x-1", "agent-a", nil)
	if !IsAlreadyClaimed(err) {
		t.Fatalf("err=%v, want already-claimed classification", err)
	}
	if IsAlreadyClaimed(errors.New("bd update x-1: some other failure")) {
		t.Fatal("unrelated error must not classify as contention")
	}
	if IsAlreadyClaimed(nil) {
		t.Fatal("nil must not classify")
	}
}

func TestReassign_ForcesAssigneeAndStatus(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.Reassign(context.Background(), "x-1", "agent-b", Metadata{"wyk.lease.owner": "agent-b"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"--actor", "agent-b", "update", "x-1", "--assignee", "agent-b", "--status", "in_progress",
		"--set-metadata", "wyk.lease.owner=agent-b", "--dolt-auto-commit=on"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
}

func TestSetMetadata_NoKeysIsNoop(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.SetMetadata(context.Background(), "x-1", nil); err != nil || len(r.calls) != 0 {
		t.Fatalf("calls=%d err=%v, want no bd call", len(r.calls), err)
	}
	if err := c.SetMetadata(context.Background(), "x-1", Metadata{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "x-1", "--set-metadata", "k=v", "--dolt-auto-commit=on"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
}

func TestRelease_ClearsAssigneeStatusAndKeysInOneWrite(t *testing.T) {
	r := &fakeRunner{}
	c := newTestClient(r)
	if err := c.Release(context.Background(), "x-1", "agent-a", []string{"wyk.lease.until", "wyk.lease.owner"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"--actor", "agent-a", "update", "x-1", "--assignee", "", "--status", "open",
		"--unset-metadata", "wyk.lease.owner", "--unset-metadata", "wyk.lease.until", "--dolt-auto-commit=on"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
	if len(r.calls) != 1 {
		t.Fatalf("release must be ONE bd write, got %d", len(r.calls))
	}
}

func TestListInProgressBy_Argv(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[]`)}
	c := newTestClient(r)
	if _, err := c.ListInProgressBy(context.Background(), "agent-a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"list", "--status", "in_progress", "--assignee", "agent-a", "--limit=0", "--json"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
}

func TestListInProgress_Argv(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[]`)}
	c := newTestClient(r)
	if _, err := c.ListInProgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"list", "--status", "in_progress", "--limit=0", "--json"}
	if got := r.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q\nwant %q", got, want)
	}
}
