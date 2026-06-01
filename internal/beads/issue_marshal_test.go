package beads

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestIssue_MarshalElidesEmptyButKeepsLoadBearing pins the W1
// token-elision contract: optional/zero fields drop out of the
// agent-facing JSON, while the always-present identity/state fields —
// crucially priority (0 == P0) — are never silently omitted.
func TestIssue_MarshalElidesEmptyButKeepsLoadBearing(t *testing.T) {
	open := Issue{ID: "a-1", Title: "t", Status: "open", Priority: 0} // P0, no body, no deps
	b, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	// Elided when empty/zero.
	for _, gone := range []string{"closed_at", "dependency_count", "dependent_count", "comment_count", "notes", "description", "owner", "created_by", "labels"} {
		if strings.Contains(s, `"`+gone+`"`) {
			t.Errorf("empty/zero %q should be elided from an open P0 issue; got %s", gone, s)
		}
	}
	// Load-bearing fields ALWAYS present — priority 0 must NOT be dropped.
	for _, keep := range []string{`"id"`, `"title"`, `"status"`, `"priority"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("load-bearing field %s must always serialise; got %s", keep, s)
		}
	}
	if !strings.Contains(s, `"priority":0`) {
		t.Errorf("priority 0 (P0/critical) must survive marshaling; got %s", s)
	}
}

func TestIssue_MarshalKeepsClosedAtWhenClosed(t *testing.T) {
	closed := Issue{ID: "a-1", Title: "t", Status: "closed", ClosedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	b, _ := json.Marshal(closed)
	if !strings.Contains(string(b), `"closed_at"`) {
		t.Errorf("a non-zero closed_at must be serialised; got %s", b)
	}
}

func TestIssue_MarshalRoundTripsThroughParse(t *testing.T) {
	// Elision is marshal-only: a marshaled-then-parsed issue must
	// reproduce the meaningful fields (parsing is unaffected).
	orig := Issue{ID: "a-1", Title: "t", Status: "open", Priority: 2, Labels: []string{"src:agent"}}
	b, _ := json.Marshal(orig)
	got, err := parseIssues([]byte("[" + string(b) + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a-1" || got[0].Priority != 2 || !got[0].HasLabel("src:agent") {
		t.Errorf("round-trip lost data: %+v", got)
	}
}
