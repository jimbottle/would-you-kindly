package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestInboxQuery_IsTheDocumentedString(t *testing.T) {
	// The inbox subcommand and docs/CONTRACT.md must agree on the
	// canonical query string — drift here means the docs lie about
	// what wyk inbox does. The contract version (wyk-contract/v1)
	// pins this exact string; bumping the contract version is the
	// only license to change it.
	want := `label=src:agent AND NOT label=human AND status!=closed`
	if inboxQuery != want {
		t.Errorf("inboxQuery drift:\n  want: %q\n  got:  %q", want, inboxQuery)
	}
}

func TestFilterByMaxPriority(t *testing.T) {
	in := []beads.Issue{
		{ID: "a", Priority: 0},
		{ID: "b", Priority: 1},
		{ID: "c", Priority: 2},
		{ID: "d", Priority: 3},
	}
	cases := []struct {
		max  int
		want []string
	}{
		{0, []string{"a"}},
		{1, []string{"a", "b"}},
		{2, []string{"a", "b", "c"}},
		{3, []string{"a", "b", "c", "d"}},
	}
	for _, tc := range cases {
		got := filterByMaxPriority(append([]beads.Issue(nil), in...), tc.max)
		if len(got) != len(tc.want) {
			t.Errorf("max=%d: len=%d, want %d", tc.max, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i].ID != tc.want[i] {
				t.Errorf("max=%d: ids=%v, want %v", tc.max, idsOf(got), tc.want)
				break
			}
		}
	}
}

func idsOf(issues []beads.Issue) []string {
	ids := make([]string, len(issues))
	for i, x := range issues {
		ids[i] = x.ID
	}
	return ids
}

// mixedRepoInbox returns a slice mimicking fetchInbox's
// unsorted, repo-concatenated output so the limitByPriority
// tests can exercise the production sort+truncate against a
// realistic shape.
func mixedRepoInbox() []beads.Issue {
	return []beads.Issue{
		{ID: "r1-c", Priority: 3, Repo: "r1"},
		{ID: "r1-a", Priority: 0, Repo: "r1"},
		{ID: "r1-b", Priority: 2, Repo: "r1"},
		{ID: "r2-y", Priority: 1, Repo: "r2"},
		{ID: "r2-x", Priority: 0, Repo: "r2"},
	}
}

func TestLimitByPriority(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  []string // expected order; nil = same as input
	}{
		{"top-3 by priority across repos", 3, []string{"r1-a", "r2-x", "r2-y"}},
		{"top-1 picks lowest priority + lowest ID tiebreak", 1, []string{"r1-a"}},
		{"limit zero empties the result", 0, []string{}},
		{"limit -1 returns input unchanged (no sort)", -1, nil},
		{"limit at len returns input unchanged (no sort)", 5, nil},
		{"limit > len returns input unchanged (no sort)", 99, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := mixedRepoInbox()
			got := limitByPriority(in, tc.limit)
			gotIDs := idsOf(got)
			var want []string
			if tc.want == nil {
				want = idsOf(mixedRepoInbox())
			} else {
				want = tc.want
			}
			if len(gotIDs) != len(want) {
				t.Fatalf("len=%d, want %d (got %v, want %v)", len(gotIDs), len(want), gotIDs, want)
			}
			for i := range want {
				if gotIDs[i] != want[i] {
					t.Errorf("position %d: got %q, want %q (full got=%v, want=%v)", i, gotIDs[i], want[i], gotIDs, want)
				}
			}
		})
	}
}

func TestSplitInboxResults_CollectsAllErrorsAndStampsRepo(t *testing.T) {
	subs := []inboxSub{{name: "a"}, {name: "broken1"}, {name: "c"}, {name: "broken2"}}
	issues := [][]beads.Issue{
		{{ID: "a-1"}},
		nil,
		{{ID: "c-1"}, {ID: "c-2"}},
		nil,
	}
	errs := []error{nil, errors.New("boom1"), nil, errors.New("boom2")}

	all, subErrs := splitInboxResults(subs, issues, errs)
	if len(all) != 3 {
		t.Errorf("expected 3 issues from the healthy repos; got %d", len(all))
	}
	// Every issue must be stamped with its repo for the multi-repo view.
	for _, i := range all {
		if i.Repo == "" {
			t.Errorf("issue %s not stamped with repo", i.ID)
		}
	}
	// BOTH failures must surface — not just the first.
	if len(subErrs) != 2 || subErrs[0].repo != "broken1" || subErrs[1].repo != "broken2" {
		t.Errorf("expected both failures in order; got %+v", subErrs)
	}
}

func TestInboxResult_OmitsErrorsWhenAllHealthy(t *testing.T) {
	b, err := json.Marshal(inboxResult{Issues: []beads.Issue{{ID: "a-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "errors") {
		t.Errorf("clean result should omit the errors key; got %s", b)
	}
	// And it must always carry an issues key (even when empty).
	b2, _ := json.Marshal(inboxResult{Issues: []beads.Issue{}})
	if !strings.Contains(string(b2), `"issues":[]`) {
		t.Errorf("empty result should still render issues:[]; got %s", b2)
	}
}

func TestEmitInboxJSON_EnvelopeShapeWithErrors(t *testing.T) {
	out := captureStdout(t, func() {
		emitInboxJSON(
			[]beads.Issue{{ID: "a-1", Title: "one"}},
			[]subError{{repo: "broken", err: errors.New("timed out")}},
		)
	})
	var res inboxResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("envelope should parse: %v\n%s", err, out)
	}
	if len(res.Issues) != 1 || res.Issues[0].ID != "a-1" {
		t.Errorf("issues lost: %+v", res.Issues)
	}
	if len(res.Errors) != 1 || res.Errors[0].Repo != "broken" || res.Errors[0].Error != "timed out" {
		t.Errorf("errors array wrong: %+v", res.Errors)
	}
}

func TestEmitInboxJSON_TotalFailureEmitsParseableEnvelope(t *testing.T) {
	// Total failure (nil issues) must still emit issues:[] + errors so
	// an agent gets parseable output instead of nothing.
	out := captureStdout(t, func() {
		emitInboxJSON(nil, []subError{{repo: "a", err: errors.New("x")}})
	})
	if !strings.Contains(out, `"issues": []`) || !strings.Contains(out, `"errors"`) {
		t.Errorf("total-failure envelope should be parseable with issues:[] + errors; got %s", out)
	}
}
