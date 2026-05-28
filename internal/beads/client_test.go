package beads

import "testing"

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
