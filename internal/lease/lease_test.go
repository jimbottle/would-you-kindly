package lease

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestOf_ExplicitStamp(t *testing.T) {
	i := beads.Issue{Status: "in_progress", Assignee: "claude-a", Metadata: beads.Metadata{
		KeyOwner: "claude-a", KeyUntil: stamp(now.Add(time.Hour)), KeyBranch: "feat/x",
	}}
	l := Of(i, now, time.Hour)
	if l.State != Live || l.Owner != "claude-a" || l.Branch != "feat/x" || l.Implicit {
		t.Fatalf("got %+v, want live explicit lease for claude-a on feat/x", l)
	}
	l = Of(i, now.Add(2*time.Hour), time.Hour)
	if l.State != Expired {
		t.Fatalf("after the stamp: state=%v, want expired", l.State)
	}
}

func TestOf_MetadataOwnerWinsOverAssignee(t *testing.T) {
	// The actor chain can leave assignee as the git user while the
	// identity went into the stamp; the stamp is authoritative.
	i := beads.Issue{Status: "in_progress", Assignee: "jimbottle", Metadata: beads.Metadata{
		KeyOwner: "claude-a", KeyUntil: stamp(now.Add(time.Hour)),
	}}
	if got := Of(i, now, time.Hour).Owner; got != "claude-a" {
		t.Fatalf("owner=%q, want claude-a", got)
	}
}

func TestOf_ImplicitFromBareClaim(t *testing.T) {
	// A `bd update --claim` with no wyk stamp: owner = assignee, expiry =
	// updated_at + ttl, flagged implicit.
	i := beads.Issue{Status: "in_progress", Assignee: "codex-1",
		StartedAt: now.Add(-10 * time.Hour), // stale: dates an earlier holder
		UpdatedAt: now.Add(-30 * time.Minute)}
	l := Of(i, now, time.Hour)
	if l.State != Live || !l.Implicit || l.Owner != "codex-1" {
		t.Fatalf("got %+v, want live implicit lease for codex-1", l)
	}
	if want := now.Add(30 * time.Minute); !l.Until.Equal(want) {
		t.Fatalf("until=%v, want updated_at+ttl=%v (not started_at-based)", l.Until, want)
	}
	if Of(i, now.Add(time.Hour), time.Hour).State != Expired {
		t.Fatal("implicit lease should expire one TTL after updated_at")
	}
}

func TestOf_ImplicitFallsBackToStartedAt(t *testing.T) {
	i := beads.Issue{Status: "in_progress", Assignee: "x", StartedAt: now.Add(-30 * time.Minute)}
	l := Of(i, now, time.Hour)
	if want := now.Add(30 * time.Minute); !l.Until.Equal(want) {
		t.Fatalf("until=%v, want started_at+ttl=%v", l.Until, want)
	}
}

func TestOf_MalformedStampDegradesToImplicit(t *testing.T) {
	i := beads.Issue{Status: "in_progress", Assignee: "a", UpdatedAt: now.Add(-time.Minute),
		Metadata: beads.Metadata{KeyOwner: "a", KeyUntil: "yesterday-ish"}}
	l := Of(i, now, time.Hour)
	if !l.Implicit || l.State != Live {
		t.Fatalf("got %+v, want implicit live lease", l)
	}
}

func TestOf_StatusGatesTheLease(t *testing.T) {
	// A stale stamp on an issue someone reopened with raw bd must NOT
	// keep it held; status is the gate.
	for _, st := range []string{"open", "closed", "blocked", "deferred", ""} {
		i := beads.Issue{Status: st, Assignee: "a", Metadata: beads.Metadata{
			KeyOwner: "a", KeyUntil: stamp(now.Add(time.Hour))}}
		if l := Of(i, now, time.Hour); l.State != None {
			t.Errorf("status=%q: state=%v, want none", st, l.State)
		}
	}
}

func TestOf_HookedIsLiveWithoutExpiry(t *testing.T) {
	i := beads.Issue{Status: "hooked", Assignee: "bot", UpdatedAt: now.Add(-100 * time.Hour)}
	l := Of(i, now, time.Hour)
	if l.State != Live || !l.Until.IsZero() || l.Owner != "bot" {
		t.Fatalf("got %+v, want live, no expiry, owner bot", l)
	}
}

func TestOf_InProgressNobodyRecorded(t *testing.T) {
	// in_progress with no assignee and no stamp: held by an unknown party
	// until one TTL of silence — still not handed to a second agent.
	i := beads.Issue{Status: "in_progress", UpdatedAt: now.Add(-time.Minute)}
	l := Of(i, now, time.Hour)
	if l.State != Live || l.Owner != "" {
		t.Fatalf("got %+v, want live with empty owner", l)
	}
	if !HeldByOther(i, "me", now, time.Hour) {
		t.Fatal("an unknown holder must count as someone else")
	}
}

func TestHeldByOther(t *testing.T) {
	mine := beads.Issue{Status: "in_progress", Metadata: beads.Metadata{KeyOwner: "me", KeyUntil: stamp(now.Add(time.Hour))}}
	theirs := beads.Issue{Status: "in_progress", Metadata: beads.Metadata{KeyOwner: "them", KeyUntil: stamp(now.Add(time.Hour))}}
	lapsed := beads.Issue{Status: "in_progress", Metadata: beads.Metadata{KeyOwner: "them", KeyUntil: stamp(now.Add(-time.Hour))}}
	open := beads.Issue{Status: "open"}
	cases := []struct {
		name string
		i    beads.Issue
		me   string
		want bool
	}{
		{"mine", mine, "me", false},
		{"theirs", theirs, "me", true},
		{"lapsed is free", lapsed, "me", false},
		{"open is free", open, "me", false},
		{"no identity: every live lease is someone else's", mine, "", true},
	}
	for _, tc := range cases {
		if got := HeldByOther(tc.i, tc.me, now, time.Hour); got != tc.want {
			t.Errorf("%s: HeldByOther=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStamp(t *testing.T) {
	m := Stamp("claude-a", now, "feat/x")
	if m[KeyOwner] != "claude-a" || m[KeyUntil] != "2026-10-08T12:00:00Z" || m[KeyBranch] != "feat/x" {
		t.Fatalf("stamp=%v", m)
	}
	if _, ok := Stamp("a", now, "")[KeyBranch]; ok {
		t.Fatal("empty branch must not be stamped")
	}
}

func TestParseTTL(t *testing.T) {
	good := map[string]time.Duration{"2h": 2 * time.Hour, "90m": 90 * time.Minute, "120": 2 * time.Hour, " 45m30s ": 45*time.Minute + 30*time.Second}
	for in, want := range good {
		got, err := ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("ParseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-5m", "soon", "1.5", "2hx"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("ParseTTL(%q) accepted, want error", bad)
		}
	}
}

func TestRemaining(t *testing.T) {
	cases := []struct {
		until time.Time
		want  string
	}{
		{time.Time{}, "no expiry"},
		{now.Add(30 * time.Second), "<1m left"},
		{now.Add(25 * time.Minute), "25m left"},
		{now.Add(90 * time.Minute), "1h30m left"},
		{now.Add(3 * time.Hour), "3h left"},
		{now.Add(72 * time.Hour), "3d left"},
		{now.Add(-5 * time.Minute), "expired 5m ago"},
	}
	for _, tc := range cases {
		if got := Remaining(Lease{Until: tc.until}, now); got != tc.want {
			t.Errorf("until=%v: %q, want %q", tc.until, got, tc.want)
		}
	}
}

func TestLease_JSONCarriesStateByName(t *testing.T) {
	b, err := json.Marshal(Lease{Owner: "a", State: Expired})
	if err != nil || !strings.Contains(string(b), `"state":"expired"`) {
		t.Fatalf("json=%s err=%v", b, err)
	}
}
