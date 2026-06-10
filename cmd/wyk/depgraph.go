package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
)

// runDepgraph walks the registered bd workspaces and emits the
// dependency edges between issues in one of three shapes: a human-
// readable text tree (default), Graphviz DOT (-dot), or a
// {nodes,edges} JSON object (-json). It's the first-class surface for
// publishing the cross-repo dependency graph — easier to scan than a
// raw `wyk export` dump and easy to feed into `dot -Tsvg` or a docs
// pipeline.
//
// Edge semantics throughout: an edge from→to means "from depends on
// to" (to blocks from). A root is an issue that depends on nothing in
// the included set; its dependents nest under it in the tree.
// Isolated issues (no dependency relationships) are omitted — this is
// a graph of dependencies, not a full issue listing.
//
// Exit codes:
//
//	0  graph emitted
//	1  registry / per-repo I/O error (partial output still emitted)
//	64 usage error
func runDepgraph(args []string) int {
	fs := flag.NewFlagSet("depgraph", flag.ContinueOnError)
	asDOT := fs.Bool("dot", false, "emit Graphviz DOT (pipe into 'dot -Tsvg')")
	asJSON := fs.Bool("json", false, "emit {nodes, edges} JSON for tooling consumers")
	compact := fs.Bool("compact", false, "with -json, emit non-indented JSON (smaller; indentation is overhead for an LLM)")
	repoName := fs.String("repo", "", "restrict to the registered repo with this name (empty = full registry)")
	priorityCap := fs.Int("priority", -1, "only include issues at this priority or higher (0=critical; -1=all); the cap is per-node, so an edge to a lower-priority neighbor is pruned and a high-priority issue with only lower-priority links can drop out")
	includeClosed := fs.Bool("closed", false, "include closed issues (default omits them)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk depgraph [-dot | -json] [-repo name] [-priority N] [-closed]")
		return 64
	}
	if *asDOT && *asJSON {
		fmt.Fprintln(os.Stderr, "wyk depgraph: -dot and -json are mutually exclusive")
		return 64
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk depgraph:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk depgraph: load registry:", err)
		return 1
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk depgraph: no repos registered. Run `wyk init` in a bd workspace first.")
		return 1
	}
	if *repoName != "" {
		filtered, err := filterRegistryByName(reg, *repoName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk depgraph:", err)
			return 1
		}
		reg = filtered
	}

	raw, hadError := collectDepGraph(reg, defaultDepGraphClient, *includeClosed, *priorityCap)
	graph := finalizeDepGraph(raw, *includeClosed, *priorityCap)

	switch {
	case *asJSON:
		emitDepJSON(os.Stdout, graph, *compact)
	case *asDOT:
		emitDepDOT(os.Stdout, graph)
	default:
		emitDepTree(os.Stdout, graph)
	}
	if hadError {
		return 1
	}
	return 0
}

// depGraphClient is the slice of beads.Client runDepgraph needs.
// ListAll supplies the node set (open + closed, filtered later in the
// pure layer); ListDeps supplies the edges per issue. Tests inject a
// stub so the walk + assembly can run without a real bd binary.
type depGraphClient interface {
	ListAll(ctx context.Context) ([]beads.Issue, error)
	ListDeps(ctx context.Context, id string) ([]beads.Issue, error)
}

// defaultDepGraphClient is runDepgraph's production factory: a real
// beads.Client pointed at the repo's path. Package-level var so a
// future probe/debug flag can swap it without touching collectDepGraph.
var defaultDepGraphClient = func(dir string) depGraphClient {
	c := beads.NewClient()
	c.Dir = dir
	return c
}

// depGraphNode is one issue in the emitted graph. Repo is the
// registered workspace name it was walked from, or empty for a
// cross-repo dependency target only seen through another issue's
// ListDeps (so a downstream renderer can still label it).
type depGraphNode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Repo     string `json:"repo,omitempty"`
}

// depGraphEdge is a directed dependency: From depends on To (To
// blocks From).
type depGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// depGraph is the emitted shape: a node list and an edge list.
type depGraph struct {
	Nodes []depGraphNode `json:"nodes"`
	Edges []depGraphEdge `json:"edges"`
}

// rawDepGraph is the pre-filter accumulation: nodes keyed by ID for
// dedup across repos, and the full edge list. finalizeDepGraph turns
// it into a sorted, filtered, connected depGraph.
type rawDepGraph struct {
	nodes map[string]depGraphNode
	edges []depGraphEdge
}

// nodeIncluded is the single inclusion predicate shared by the
// collect-side ListDeps skip and the finalize-side filter so the two
// can't drift. A node is in scope when it isn't closed (unless
// includeClosed) and its priority is within the cap (priorityCap < 0
// disables the cap; lower number = higher priority, so "within cap"
// means priority <= cap).
func nodeIncluded(status string, priority int, includeClosed bool, priorityCap int) bool {
	if !includeClosed && status == "closed" {
		return false
	}
	if priorityCap >= 0 && priority > priorityCap {
		return false
	}
	return true
}

// collectDepGraph walks the registry sequentially (matching the
// export/dashboard bd-subprocess policy — no parallel fan-out heating
// the CPU). For each repo it pulls every issue via ListAll, then
// resolves edges only for issues that actually have dependencies
// (DependencyCount > 0) AND would survive the active filters — an
// out-of-scope issue's edges are all dropped in finalize anyway, so
// shelling `bd dep list` for it would be pure waste. That keeps the
// "few bd calls" win real on the default (omit-closed) path even in a
// repo full of closed-but-once-blocked issues. A dependency target
// not walked directly (a cross-repo edge) is added as a node from the
// ListDeps payload so the edge isn't dangling. Per-repo / per-issue
// errors are folded into the hadError flag rather than aborting — a
// partial graph still emits. The same filters are re-applied in
// finalize, which stays the correctness source of truth; this skip is
// purely an optimization.
func collectDepGraph(reg *registry.Registry, mk func(dir string) depGraphClient, includeClosed bool, priorityCap int) (rawDepGraph, bool) {
	raw := rawDepGraph{nodes: map[string]depGraphNode{}}
	hadError := false
	for _, r := range reg.Repos {
		c := mk(r.Path)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		issues, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "wyk depgraph: %s: list-all: %v\n", r.Name, err)
			hadError = true
			continue
		}
		for _, iss := range issues {
			// A walked repo's data is authoritative (it carries the
			// Repo label); overwrite any stub a prior repo's ListDeps
			// added for the same ID.
			raw.nodes[iss.ID] = depGraphNode{
				ID:       iss.ID,
				Title:    iss.Title,
				Status:   iss.Status,
				Priority: iss.Priority,
				Repo:     r.Name,
			}
		}
		for _, iss := range issues {
			if iss.DependencyCount == 0 {
				continue
			}
			// Skip the dep lookup for an issue the filters will drop:
			// every edge it sources gets pruned in finalize, so the
			// call's results would be thrown away.
			if !nodeIncluded(iss.Status, iss.Priority, includeClosed, priorityCap) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			deps, err := c.ListDeps(ctx, iss.ID)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "wyk depgraph: %s: dep list %s: %v\n", r.Name, iss.ID, err)
				hadError = true
				continue
			}
			for _, d := range deps {
				raw.edges = append(raw.edges, depGraphEdge{From: iss.ID, To: d.ID})
				// Fill in a node for a cross-repo / external target we
				// haven't walked, but don't clobber a richer walked entry.
				if _, ok := raw.nodes[d.ID]; !ok {
					raw.nodes[d.ID] = depGraphNode{
						ID:       d.ID,
						Title:    d.Title,
						Status:   d.Status,
						Priority: d.Priority,
					}
				}
			}
		}
	}
	return raw, hadError
}

// finalizeDepGraph applies the -closed / -priority filters and
// returns a deterministic, connected depGraph. A node is included
// when it isn't closed (unless includeClosed) and its priority is
// within the cap (priorityCap < 0 disables the cap; lower number =
// higher priority, so "within cap" means priority <= cap). An edge
// survives only when BOTH endpoints are included; the final node set
// is the endpoints of the surviving edges (isolated nodes are
// dropped — they carry no dependency information). Nodes sort by
// (priority, id); edges by (from, to).
func finalizeDepGraph(raw rawDepGraph, includeClosed bool, priorityCap int) depGraph {
	included := func(id string) bool {
		n, ok := raw.nodes[id]
		if !ok {
			return false
		}
		return nodeIncluded(n.Status, n.Priority, includeClosed, priorityCap)
	}
	var edges []depGraphEdge
	connected := map[string]bool{}
	for _, e := range raw.edges {
		if included(e.From) && included(e.To) {
			edges = append(edges, e)
			connected[e.From] = true
			connected[e.To] = true
		}
	}
	var nodes []depGraphNode
	for id := range connected {
		nodes = append(nodes, raw.nodes[id])
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Priority != nodes[j].Priority {
			return nodes[i].Priority < nodes[j].Priority
		}
		return nodes[i].ID < nodes[j].ID
	})
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return depGraph{Nodes: nodes, Edges: edges}
}

// depNodeLabel renders one issue as `ID — title (status)`, the same
// shape the TUI detail view uses for its dependency sections. The title
// is untrusted bd content printed to a terminal, so strip escapes
// (would-you-kindly-5zlr).
func depNodeLabel(n depGraphNode) string {
	return fmt.Sprintf("%s — %s (%s)", n.ID, sanitize.Inline(n.Title), n.Status)
}

// emitDepJSON writes the graph as indented {nodes, edges} JSON. Nil
// slices render as [] (not null) so a consumer can iterate without a
// nil guard.
func emitDepJSON(w io.Writer, g depGraph, compact bool) {
	if g.Nodes == nil {
		g.Nodes = []depGraphNode{}
	}
	if g.Edges == nil {
		g.Edges = []depGraphEdge{}
	}
	_ = emitJSON(w, g, compact)
}

// emitDepDOT writes the graph as a Graphviz digraph. Each node gets a
// label line and each edge an arrow; from→to reads "from depends on
// to". Quotes in IDs/titles are escaped so a stray character can't
// break the DOT.
func emitDepDOT(w io.Writer, g depGraph) {
	fmt.Fprintln(w, "digraph deps {")
	fmt.Fprintln(w, "  rankdir=LR;")
	for _, n := range g.Nodes {
		fmt.Fprintf(w, "  %q [label=%q];\n", n.ID, depNodeLabel(n))
	}
	for _, e := range g.Edges {
		fmt.Fprintf(w, "  %q -> %q;\n", e.From, e.To)
	}
	fmt.Fprintln(w, "}")
}

// emitDepTree writes the graph as an indented text forest: roots
// (issues that depend on nothing in the set) first, each followed by
// its dependents nested beneath it. A node reachable from multiple
// parents (a diamond/DAG) is printed in full once and rendered as a
// "(see above)" pointer thereafter, without re-descending, so dense
// graphs don't duplicate whole subtrees. A node reached twice on the
// same path (a dependency cycle) is printed once more with a
// "(cycle)" marker and not descended into, so the walk always
// terminates.
func emitDepTree(w io.Writer, g depGraph) {
	if len(g.Nodes) == 0 {
		fmt.Fprintln(w, "(no dependencies)")
		return
	}
	nodeByID := make(map[string]depGraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}
	childrenOf := map[string][]string{}
	isDependent := map[string]bool{} // appears as an edge From → not a root
	for _, e := range g.Edges {
		childrenOf[e.To] = append(childrenOf[e.To], e.From)
		isDependent[e.From] = true
	}
	// Sort each child list by the same (priority, id) order the node
	// list uses, so the tree reads deterministically.
	less := func(a, b string) bool {
		na, nb := nodeByID[a], nodeByID[b]
		if na.Priority != nb.Priority {
			return na.Priority < nb.Priority
		}
		return na.ID < nb.ID
	}
	for k := range childrenOf {
		kids := childrenOf[k]
		sort.Slice(kids, func(i, j int) bool { return less(kids[i], kids[j]) })
	}
	printed := map[string]bool{}  // emitted anywhere (gates fallback-root selection)
	expanded := map[string]bool{} // subtree already printed in full once
	var print func(id string, depth int, onPath map[string]bool)
	print = func(id string, depth int, onPath map[string]bool) {
		printed[id] = true
		indent := strings.Repeat("  ", depth)
		if onPath[id] {
			fmt.Fprintf(w, "%s%s  (cycle)\n", indent, depNodeLabel(nodeByID[id]))
			return
		}
		// A node reachable from multiple parents (a diamond/DAG) would
		// otherwise have its whole subtree re-printed under each. Show
		// it once in full; subsequent hits get a "(see above)" pointer
		// and aren't re-descended, bounding output on dense graphs —
		// mirrors the "(cycle)" marker.
		if expanded[id] {
			fmt.Fprintf(w, "%s%s  (see above)\n", indent, depNodeLabel(nodeByID[id]))
			return
		}
		fmt.Fprintf(w, "%s%s\n", indent, depNodeLabel(nodeByID[id]))
		onPath[id] = true
		for _, child := range childrenOf[id] {
			print(child, depth+1, onPath)
		}
		delete(onPath, id)
		expanded[id] = true
	}
	// g.Nodes is already (priority, id)-sorted, so iterating it yields
	// roots in the right order without a second sort.
	for _, n := range g.Nodes {
		if !isDependent[n.ID] {
			print(n.ID, 0, map[string]bool{})
		}
	}
	// A pure cycle has no root (every node is some edge's From), so the
	// pass above emits nothing for it. Surface any still-unprinted node
	// as a fallback root — the on-path guard keeps the cycle finite —
	// so no node silently vanishes from the tree.
	for _, n := range g.Nodes {
		if !printed[n.ID] {
			print(n.ID, 0, map[string]bool{})
		}
	}
}
