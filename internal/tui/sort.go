package tui

import (
	"sort"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// This file holds the list-sorting logic: applySort (the per-axis
// in-place sort) and the dependency topological sort (sortByDeps /
// depsFullyResolved). Extracted from model.go to isolate the ordering
// algorithm from the Bubble Tea model wiring.

// applySort orders the issue slice in place per the chosen sort
// key. sortNone is a no-op so callers can pass through without
// branching. Priority ASC (P0 first); updated DESC (newest first);
// repo / id ASC (alphabetical). deps is a topological order built
// from depCache (see sortByDeps); it's handled separately because
// a topo order isn't expressible as a pairwise less-func.
//
// depCache supplies the resolved dependency edges the deps sort
// needs; it's ignored for every other axis. A nil cache (no
// DepLister, or nothing resolved yet) makes the deps sort fall
// back to the DependencyCount level proxy.
func applySort(issues []beads.Issue, k sortKey, reverse bool, depCache map[string][]beads.Issue) {
	if k == sortDeps {
		sortByDeps(issues, depCache)
		if reverse {
			for i, j := 0, len(issues)-1; i < j; i, j = i+1, j-1 {
				issues[i], issues[j] = issues[j], issues[i]
			}
		}
		return
	}
	// Each axis declares its NATURAL direction (priority asc =
	// P0 first; updated desc = newest first). Reverse flips the
	// less-func so a single bool drives every axis the same way.
	var less func(i, j int) bool
	switch k {
	case sortPriority:
		less = func(i, j int) bool { return issues[i].Priority < issues[j].Priority }
	case sortUpdated:
		less = func(i, j int) bool { return issues[i].UpdatedAt.After(issues[j].UpdatedAt) }
	case sortRepo:
		less = func(i, j int) bool { return issues[i].Repo < issues[j].Repo }
	case sortID:
		less = func(i, j int) bool { return issues[i].ID < issues[j].ID }
	default:
		return
	}
	if reverse {
		base := less
		less = func(i, j int) bool { return base(j, i) }
	}
	sort.SliceStable(issues, less)
}

// sortByDeps reorders issues in place into a topological order
// (Kahn's algorithm) against the CURRENT visible set:
//
//   - Edges are the direct dependencies in depCache. A dependency
//     pointing at an issue NOT in `issues` is ignored — treated as
//     already satisfied — so an issue blocked only by off-screen
//     work sorts as a root.
//   - In-degree 0 nodes (roots) come first; an issue is emitted
//     once every visible dep it has is already emitted; deeper
//     levels follow.
//   - Within a batch of newly-ready nodes the tiebreak is Priority
//     ASC then ID ASC, matching the priority sort's reading order.
//   - Cycles can't crash: when no zero-in-degree node remains but
//     nodes are left, the remaining node with lowest Priority then
//     lowest ID is force-emitted to break the cycle, and the loop
//     continues.
//
// Fallback: when depCache doesn't cover every visible row (no
// DepLister, or async resolution hasn't finished), the true edge
// set is unknown, so a partial topo order would be misleading.
// Degrade to the DependencyCount level proxy (count ASC, then
// Priority ASC, then ID ASC) — the same ordering the old proxy
// produced — so the first paint stays sensible until the cache
// fills in.
func sortByDeps(issues []beads.Issue, depCache map[string][]beads.Issue) {
	if !depsFullyResolved(issues, depCache) {
		sort.SliceStable(issues, func(i, j int) bool {
			if issues[i].DependencyCount != issues[j].DependencyCount {
				return issues[i].DependencyCount < issues[j].DependencyCount
			}
			if issues[i].Priority != issues[j].Priority {
				return issues[i].Priority < issues[j].Priority
			}
			return issues[i].ID < issues[j].ID
		})
		return
	}

	// Index the visible set so off-screen deps can be skipped and
	// each node located by ID. Two issues sharing an ID (cross-repo
	// collision) is possible in theory; the visible set is keyed by
	// bare ID here because depCache and DependencyCount are too —
	// the deps sort is a best-effort ordering aid, not a routing
	// decision, so a collision degrades to "treated as one node"
	// rather than mis-routing a write.
	idx := make(map[string]int, len(issues))
	for i := range issues {
		idx[issues[i].ID] = i
	}

	// Build in-degree and the reverse adjacency (dep -> dependents)
	// counting only edges whose target is in the visible set.
	inDeg := make(map[string]int, len(issues))
	dependents := make(map[string][]string, len(issues))
	for i := range issues {
		id := issues[i].ID
		if _, ok := inDeg[id]; !ok {
			inDeg[id] = 0
		}
		for _, d := range depCache[id] {
			if _, onScreen := idx[d.ID]; !onScreen {
				continue // off-screen dep: already satisfied
			}
			if d.ID == id {
				continue // self-edge: ignore so it doesn't deadlock
			}
			inDeg[id]++
			dependents[d.ID] = append(dependents[d.ID], id)
		}
	}

	// less ranks ready nodes by Priority ASC then ID ASC.
	less := func(a, b string) bool {
		ia, ib := issues[idx[a]], issues[idx[b]]
		if ia.Priority != ib.Priority {
			return ia.Priority < ib.Priority
		}
		return ia.ID < ib.ID
	}

	emitted := make(map[string]bool, len(issues))
	out := make([]beads.Issue, 0, len(issues))

	// Seed the ready set with every in-degree-0 node.
	var ready []string
	for i := range issues {
		if inDeg[issues[i].ID] == 0 {
			ready = append(ready, issues[i].ID)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })

	usedIdx := make(map[int]bool, len(issues))
	emit := func(id string) {
		emitted[id] = true
		usedIdx[idx[id]] = true
		out = append(out, issues[idx[id]])
		// Decrement dependents; newly-zero ones become ready. Insert
		// in sorted position so the per-level tiebreak holds without
		// re-sorting the whole ready slice each time.
		for _, dep := range dependents[id] {
			if emitted[dep] {
				continue
			}
			inDeg[dep]--
			if inDeg[dep] == 0 {
				pos := sort.Search(len(ready), func(i int) bool { return less(dep, ready[i]) })
				ready = append(ready, "")
				copy(ready[pos+1:], ready[pos:])
				ready[pos] = dep
			}
		}
	}

	for len(out) < len(issues) {
		if len(ready) > 0 {
			id := ready[0]
			ready = ready[1:]
			if emitted[id] {
				continue
			}
			emit(id)
			continue
		}
		// No zero-in-degree node but nodes remain → a cycle. Break it
		// by force-emitting the lowest-Priority-then-ID survivor so
		// the loop can make progress instead of spinning forever.
		var pick string
		for i := range issues {
			id := issues[i].ID
			if emitted[id] {
				continue
			}
			if pick == "" || less(id, pick) {
				pick = id
			}
		}
		if pick == "" {
			break // defensive: nothing left to emit
		}
		emit(pick)
	}

	// When two visible rows share a bare ID (a cross-repo collision),
	// idx keeps only the last index, so the duplicate's slot is never
	// emitted and out stays short — copy would then leave a stale row
	// in the tail. Append any input row whose index wasn't consumed so
	// every original row survives exactly once (the collided duplicate
	// lands unsorted at the end rather than vanishing).
	if len(out) < len(issues) {
		for i := range issues {
			if !usedIdx[i] {
				out = append(out, issues[i])
			}
		}
	}

	copy(issues, out)
}

// depsFullyResolved reports whether depCache holds an entry for
// every issue in the slice. Only then is a true topological order
// computable; otherwise the deps sort degrades to the count proxy.
func depsFullyResolved(issues []beads.Issue, depCache map[string][]beads.Issue) bool {
	if depCache == nil {
		return false
	}
	for i := range issues {
		if _, ok := depCache[issues[i].ID]; !ok {
			return false
		}
	}
	return true
}
