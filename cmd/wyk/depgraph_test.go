package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// stubDepGraphClient is a minimal depGraphClient: ListAll returns a
// canned issue list and ListDeps returns canned edges per ID. Either
// call can be made to error to exercise the error-folding path.
type stubDepGraphClient struct {
	issues   []beads.Issue
	listErr  error
	deps     map[string][]beads.Issue
	depErr   error
	depCalls []string // IDs ListDeps was invoked for, in order
}

func (s *stubDepGraphClient) ListAll(_ context.Context) ([]beads.Issue, error) {
	return s.issues, s.listErr
}
func (s *stubDepGraphClient) ListDeps(_ context.Context, id string) ([]beads.Issue, error) {
	s.depCalls = append(s.depCalls, id)
	if s.depErr != nil {
		return nil, s.depErr
	}
	return s.deps[id], nil
}

// synthGraph builds a small two-issue chain (a-2 depends on a-1) plus
// an unrelated isolated issue, the shared fixture for the emit tests.
func synthRaw() rawDepGraph {
	return rawDepGraph{
		nodes: map[string]depGraphNode{
			"a-1": {ID: "a-1", Title: "root", Status: "open", Priority: 0, Repo: "wyk"},
			"a-2": {ID: "a-2", Title: "leaf", Status: "open", Priority: 1, Repo: "wyk"},
			"a-3": {ID: "a-3", Title: "lonely", Status: "open", Priority: 2, Repo: "wyk"},
		},
		edges: []depGraphEdge{{From: "a-2", To: "a-1"}},
	}
}

func TestCollectDepGraph_BuildsNodesAndEdges(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "wyk", Path: "/tmp/wyk"}}}
	stub := &stubDepGraphClient{
		issues: []beads.Issue{
			{ID: "a-1", Title: "root", Status: "open"},
			{ID: "a-2", Title: "leaf", Status: "open", DependencyCount: 1},
		},
		deps: map[string][]beads.Issue{
			"a-2": {{ID: "a-1", Title: "root", Status: "open"}},
		},
	}
	raw, hadError := collectDepGraph(reg, func(_ string) depGraphClient { return stub }, false, -1)
	if hadError {
		t.Errorf("clean walk should not flag hadError")
	}
	if len(raw.nodes) != 2 {
		t.Errorf("expected 2 nodes; got %d", len(raw.nodes))
	}
	if raw.nodes["a-1"].Repo != "wyk" {
		t.Errorf("node a-1 should be tagged with its repo; got %q", raw.nodes["a-1"].Repo)
	}
	if len(raw.edges) != 1 || raw.edges[0] != (depGraphEdge{From: "a-2", To: "a-1"}) {
		t.Errorf("expected one a-2→a-1 edge; got %+v", raw.edges)
	}
}

func TestCollectDepGraph_AddsCrossRepoTargetNode(t *testing.T) {
	// a-2 depends on b-9, which lives in another (un-walked) workspace.
	// The edge target must still appear as a node, sourced from the
	// ListDeps payload, with an empty Repo.
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "wyk", Path: "/tmp/wyk"}}}
	stub := &stubDepGraphClient{
		issues: []beads.Issue{{ID: "a-2", Title: "leaf", Status: "open", DependencyCount: 1}},
		deps:   map[string][]beads.Issue{"a-2": {{ID: "b-9", Title: "foreign", Status: "open"}}},
	}
	raw, _ := collectDepGraph(reg, func(_ string) depGraphClient { return stub }, false, -1)
	n, ok := raw.nodes["b-9"]
	if !ok {
		t.Fatal("cross-repo dependency target b-9 should be added as a node")
	}
	if n.Title != "foreign" || n.Repo != "" {
		t.Errorf("cross-repo node should carry ListDeps fields and empty repo; got %+v", n)
	}
}

func TestCollectDepGraph_FoldsErrors(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "ok", Path: "/tmp/ok"},
		{Name: "broken", Path: "/tmp/broken"},
	}}
	stubs := map[string]depGraphClient{
		"/tmp/ok":     &stubDepGraphClient{issues: []beads.Issue{{ID: "a-1", Status: "open"}}},
		"/tmp/broken": &stubDepGraphClient{listErr: errors.New("boom")},
	}
	raw, hadError := collectDepGraph(reg, func(dir string) depGraphClient { return stubs[dir] }, false, -1)
	if !hadError {
		t.Error("a failing repo should set hadError")
	}
	if _, ok := raw.nodes["a-1"]; !ok {
		t.Error("the healthy repo's nodes should still be collected")
	}
}

func TestFinalizeDepGraph_DropsIsolatedAndClosed(t *testing.T) {
	raw := synthRaw()
	// Add a closed node depending on a-1 so we can assert it's dropped.
	raw.nodes["a-4"] = depGraphNode{ID: "a-4", Title: "done", Status: "closed", Priority: 0}
	raw.edges = append(raw.edges, depGraphEdge{From: "a-4", To: "a-1"})

	g := finalizeDepGraph(raw, false, -1)
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	if ids["a-3"] {
		t.Error("isolated node a-3 should be dropped (no edges)")
	}
	if ids["a-4"] {
		t.Error("closed node a-4 should be dropped without -closed")
	}
	if !ids["a-1"] || !ids["a-2"] {
		t.Errorf("the open a-2→a-1 chain should survive; got nodes %v", ids)
	}
	// Edge to the dropped closed node must be gone too.
	for _, e := range g.Edges {
		if e.From == "a-4" {
			t.Error("edge from dropped closed node should not survive")
		}
	}
}

func TestFinalizeDepGraph_PriorityCap(t *testing.T) {
	raw := synthRaw() // a-1 P0, a-2 P1
	// Cap at P0: a-2 (P1) is excluded, which orphans a-1 → empty graph.
	g := finalizeDepGraph(raw, false, 0)
	if len(g.Nodes) != 0 {
		t.Errorf("priority cap 0 should exclude the P1 dependent and orphan the rest; got %+v", g.Nodes)
	}
	// Cap at P1: both survive.
	g = finalizeDepGraph(raw, false, 1)
	if len(g.Nodes) != 2 {
		t.Errorf("priority cap 1 should keep both nodes; got %d", len(g.Nodes))
	}
}

func TestFinalizeDepGraph_IncludeClosed(t *testing.T) {
	raw := synthRaw()
	raw.nodes["a-1"] = depGraphNode{ID: "a-1", Title: "root", Status: "closed", Priority: 0, Repo: "wyk"}
	g := finalizeDepGraph(raw, true, -1)
	if len(g.Nodes) != 2 {
		t.Errorf("-closed should keep the closed blocker and its edge; got %d nodes", len(g.Nodes))
	}
}

func TestEmitDepJSON_ShapeAndEmptySlices(t *testing.T) {
	var buf bytes.Buffer
	emitDepJSON(&buf, depGraph{})
	var decoded depGraph
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("emitted JSON should parse: %v", err)
	}
	// Empty graph must render [] not null so consumers can iterate.
	if !strings.Contains(buf.String(), `"nodes": []`) || !strings.Contains(buf.String(), `"edges": []`) {
		t.Errorf("empty graph should render [] slices; got:\n%s", buf.String())
	}
}

func TestEmitDepJSON_RoundTrips(t *testing.T) {
	g := finalizeDepGraph(synthRaw(), false, -1)
	var buf bytes.Buffer
	emitDepJSON(&buf, g)
	var got depGraph
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || len(got.Edges) != 1 {
		t.Errorf("roundtrip lost data: %+v", got)
	}
	if got.Edges[0].From != "a-2" || got.Edges[0].To != "a-1" {
		t.Errorf("edge direction mangled: %+v", got.Edges[0])
	}
}

func TestEmitDepDOT_ValidStructure(t *testing.T) {
	var buf bytes.Buffer
	emitDepDOT(&buf, finalizeDepGraph(synthRaw(), false, -1))
	out := buf.String()
	for _, want := range []string{"digraph deps {", `"a-2" -> "a-1";`, `"a-1" [label=`, "}"} {
		if !strings.Contains(out, want) {
			t.Errorf("DOT missing %q; got:\n%s", want, out)
		}
	}
}

func TestEmitDepTree_RootsThenDependents(t *testing.T) {
	var buf bytes.Buffer
	emitDepTree(&buf, finalizeDepGraph(synthRaw(), false, -1))
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected root + 1 nested dependent; got %d lines:\n%s", len(lines), buf.String())
	}
	// Root a-1 unindented, dependent a-2 indented beneath it.
	if !strings.HasPrefix(lines[0], "a-1 ") {
		t.Errorf("first line should be root a-1; got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  a-2 ") {
		t.Errorf("second line should be a-2 indented under a-1; got %q", lines[1])
	}
}

func TestEmitDepTree_CycleTerminates(t *testing.T) {
	// a-1 ↔ a-2 mutual dependency. The walk must terminate and mark
	// the back-edge rather than recurse forever.
	raw := rawDepGraph{
		nodes: map[string]depGraphNode{
			"a-1": {ID: "a-1", Title: "x", Status: "open"},
			"a-2": {ID: "a-2", Title: "y", Status: "open"},
		},
		edges: []depGraphEdge{{From: "a-2", To: "a-1"}, {From: "a-1", To: "a-2"}},
	}
	var buf bytes.Buffer
	emitDepTree(&buf, finalizeDepGraph(raw, false, -1))
	if !strings.Contains(buf.String(), "(cycle)") {
		t.Errorf("a cyclic graph should mark the back-edge with (cycle); got:\n%s", buf.String())
	}
}

func TestCollectDepGraph_SkipsListDepsForFilteredIssues(t *testing.T) {
	// A closed issue with dependencies must NOT trigger a `bd dep
	// list` on the default (omit-closed) path — its edges would be
	// discarded in finalize anyway. The open issue still gets resolved.
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "wyk", Path: "/tmp/wyk"}}}
	stub := &stubDepGraphClient{
		issues: []beads.Issue{
			{ID: "a-open", Status: "open", DependencyCount: 1},
			{ID: "a-closed", Status: "closed", DependencyCount: 1},
		},
		deps: map[string][]beads.Issue{
			"a-open":   {{ID: "a-dep", Status: "open"}},
			"a-closed": {{ID: "a-dep", Status: "open"}},
		},
	}
	collectDepGraph(reg, func(_ string) depGraphClient { return stub }, false, -1)
	for _, id := range stub.depCalls {
		if id == "a-closed" {
			t.Error("ListDeps should be skipped for a closed issue when -closed is off")
		}
	}
	if len(stub.depCalls) != 1 || stub.depCalls[0] != "a-open" {
		t.Errorf("expected exactly one dep lookup (a-open); got %v", stub.depCalls)
	}

	// With includeClosed, the closed issue IS resolved.
	stub.depCalls = nil
	collectDepGraph(reg, func(_ string) depGraphClient { return stub }, true, -1)
	if len(stub.depCalls) != 2 {
		t.Errorf("-closed should resolve both issues; got %v", stub.depCalls)
	}
}

func TestEmitDepTree_DeduplicatesSharedSubtree(t *testing.T) {
	// Diamond: a-4 depends on both a-2 and a-3, which both depend on
	// a-1. a-4's subtree must print in full once and as "(see above)"
	// the second time, not duplicate.
	raw := rawDepGraph{
		nodes: map[string]depGraphNode{
			"a-1": {ID: "a-1", Title: "root", Status: "open", Priority: 0},
			"a-2": {ID: "a-2", Title: "mid-a", Status: "open", Priority: 1},
			"a-3": {ID: "a-3", Title: "mid-b", Status: "open", Priority: 1},
			"a-4": {ID: "a-4", Title: "leaf", Status: "open", Priority: 2},
		},
		edges: []depGraphEdge{
			{From: "a-2", To: "a-1"},
			{From: "a-3", To: "a-1"},
			{From: "a-4", To: "a-2"},
			{From: "a-4", To: "a-3"},
		},
	}
	var buf bytes.Buffer
	emitDepTree(&buf, finalizeDepGraph(raw, false, -1))
	out := buf.String()
	if !strings.Contains(out, "(see above)") {
		t.Errorf("a shared subtree should be collapsed with (see above); got:\n%s", out)
	}
	if n := strings.Count(out, "(see above)"); n != 1 {
		t.Errorf("expected exactly one (see above) marker; got %d:\n%s", n, out)
	}
	// a-4 appears twice as a line (once expanded, once see-above) but
	// must not recurse further the second time — total a-4 lines == 2.
	if n := strings.Count(out, "a-4 "); n != 2 {
		t.Errorf("a-4 should appear exactly twice (once full, once see-above); got %d:\n%s", n, out)
	}
}

func TestEmitDepTree_EmptyGraph(t *testing.T) {
	var buf bytes.Buffer
	emitDepTree(&buf, depGraph{})
	if !strings.Contains(buf.String(), "no dependencies") {
		t.Errorf("empty graph should print a friendly placeholder; got %q", buf.String())
	}
}
