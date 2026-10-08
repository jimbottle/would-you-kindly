package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sahilm/fuzzy"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// Narrowing and ordering the visible set: the / fuzzy filter,
// presets, priority caps, sort keys, and recomputeVisible.

// titleSource exposes the issue list's titles to sahilm/fuzzy for the
// subsequence title match. The description is matched separately as a
// plain substring (see recomputeVisible), so it has no fuzzy source —
// keeping the two fields independent means a query can't match across
// the title→description boundary.
type titleSource []beads.Issue

func (s titleSource) String(i int) string { return s[i].Title }
func (s titleSource) Len() int            { return len(s) }

// issuesMatchingPreset approximates, in memory, what a bd fetch for
// `want` would return, given a snapshot taken under `have`. Only an
// "all" snapshot can seed a different view (it's the superset), and
// only for the presets that are plain field predicates:
//
//	human   -> carries the human label, non-closed
//	blocked -> status == blocked
//	mine    -> assignee == me, non-closed (or just non-closed when
//	           me is empty, matching filter.QueryWithClosed's
//	           degradation)
//
// "ready" is excluded — bd computes dependency-readiness and a field
// predicate would silently show blocked-by-open-deps rows. ok=false
// means "can't approximate; cold-start instead". The predicates
// filter closed rows EXPLICITLY: saveCacheCmd persists every
// successful fetch, including ones taken while showClosed was on,
// and sessions relaunch closed-excluded — so the snapshot cannot be
// assumed closed-free (roborev #2061/#2062).
func issuesMatchingPreset(snapshot []beads.Issue, have, want filter.Preset, me string) ([]beads.Issue, bool) {
	if have != filter.PresetAll {
		return nil, false
	}
	var keep func(beads.Issue) bool
	switch want {
	case filter.PresetHuman:
		keep = func(i beads.Issue) bool { return i.IsHuman() && i.Status != "closed" }
	case filter.PresetBlocked:
		keep = func(i beads.Issue) bool { return i.Status == "blocked" }
	case filter.PresetReview:
		keep = func(i beads.Issue) bool { return i.HasLabel(filter.ReviewLabel) && i.Status != "closed" }
	case filter.PresetMine:
		if me == "" {
			// QueryWithClosed degrades `mine` with no identity to
			// the non-closed set.
			keep = func(i beads.Issue) bool { return i.Status != "closed" }
			break
		}
		keep = func(i beads.Issue) bool { return i.Assignee == me && i.Status != "closed" }
	default:
		return nil, false
	}
	out := make([]beads.Issue, 0, len(snapshot))
	for _, i := range snapshot {
		if keep(i) {
			out = append(out, i)
		}
	}
	return out, true
}

// sortKey identifies the active client-side sort for the visible
// rows. sortNone preserves bd's native order (the default).
type sortKey int

const (
	sortNone sortKey = iota
	sortPriority
	sortUpdated
	sortRepo
	sortID
	// sortDeps orders rows topologically against the CURRENT
	// VISIBLE SET: roots (nothing they depend on is on screen)
	// first, then issues whose deps are all already emitted,
	// deeper levels later. Edges come from the per-issue dep set
	// resolved via DepLister into m.depCache; dependencies pointing
	// at issues NOT in the visible set are ignored (treated as
	// already satisfied). Until every visible row's edges are
	// cached, the sort degrades to the bd-supplied DependencyCount
	// level proxy so the first paint isn't blocked on N `bd dep
	// list` round-trips. See sortByDeps / resolveDepsCmd.
	sortDeps
)

// next returns the next sort key in the cycle so `s` rotates
// through {none, priority, updated, repo, id, deps, none, ...}.
func (k sortKey) next() sortKey {
	if k == sortDeps {
		return sortNone
	}
	return k + 1
}

// label returns the human-readable name used as the chip strip
// text (the header arrow is applied separately by sortDecorate
// against the column's own caption).
func (k sortKey) label() string {
	switch k {
	case sortPriority:
		return "priority"
	case sortUpdated:
		return "updated"
	case sortRepo:
		return "repo"
	case sortID:
		return "id"
	case sortDeps:
		return "deps"
	default:
		return ""
	}
}

// sortKeyFromLabel is the inverse of label: it maps a persisted sort
// label back to its sortKey. The bool is false for an empty or
// unrecognised label so session hydration can leave the default sort
// untouched rather than silently snapping to sortNone. sortNone has
// no label (label returns "") and so is never produced here — a saved
// session with no sort simply doesn't carry the field.
func sortKeyFromLabel(s string) (sortKey, bool) {
	for _, k := range []sortKey{sortPriority, sortUpdated, sortRepo, sortID, sortDeps} {
		if k.label() == s {
			return k, true
		}
	}
	return sortNone, false
}

// jumpToHuman moves the cursor to the next (dir=+1) or previous
// (dir=-1) issue in m.visible that carries the human label. Wraps.
// If no human-flagged issues are visible, sets a status banner and
// leaves the cursor put.
func (m Model) jumpToHuman(dir int) (tea.Model, tea.Cmd) {
	n := len(m.visible)
	if n == 0 {
		return m, nil
	}
	for offset := 1; offset <= n; offset++ {
		idx := ((m.cursor+dir*offset)%n + n) % n
		if m.visible[idx].IsHuman() {
			m.cursor = idx
			m.ensureCursorVisible()
			return m, nil
		}
	}
	m.setStatus("no human-flagged issues in this view")
	return m, nil
}

// --- write actions ------------------------------------------------

// switchPreset clears the visible rows before dispatching the new
// fetch so the UI doesn't flash the old preset's data under the new
// header. The previous preset's rows stay visible until the new
// fetch returns — clearing them would blank the screen for the
// duration of the bd round-trip. The refreshing indicator in the
// status bar signals that the on-screen data is stale-for-this-
// preset; the cursor resets to 0 so the user lands at the top of
// the new view as soon as data arrives. Any pending fuzzy filter
// stays — it re-applies once the new data arrives.
func (m Model) switchPreset(p filter.Preset) (tea.Model, tea.Cmd) {
	// Record the h-toggle's return target on ANY transition into the
	// human view — h, tab, the :preset palette — not just the h key,
	// so toggling out always returns to where the user actually came
	// from rather than a target recorded on an older h trip
	// (roborev #2061/#2062). Still only on the way IN, so a stray
	// double-h can't strand the target on "human".
	if p == filter.PresetHuman && m.preset != filter.PresetHuman {
		m.humanReturnPreset = m.preset
	}
	m.preset = p
	m.cursor = 0
	m.scroll = 0
	// Instant paint: if this preset has been fetched before, swap its
	// cached rows in NOW so the list reflects the new view on this
	// frame instead of showing the old preset's rows for the duration
	// of the bd round-trip. The fetch dispatched below still runs and
	// reconciles when it lands (its fetchedMsg refreshes both m.all
	// and the cache). A cache miss leaves the previous rows up — the
	// pre-cache behavior — until the fetch returns.
	if cached, ok := m.presetCache[p]; ok {
		// Clone for the same aliasing reason the store side clones:
		// once painted, m.all is fair game for in-place optimistic
		// mutations that must not reach back into the cache.
		m.all = cloneIssues(cached)
		m.commonPrefix = commonIDPrefix(m.all)
		m.recomputeVisible()
		m.ensureCursorVisible()
	}
	m.refreshing = true
	return m, m.fetchCmd()
}

func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c quits unconditionally; the status bar advertises it and
	// the textinput wouldn't otherwise intercept it.
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	// Cursor movement stays live while the prompt is open — the list
	// narrows as the user types, and picking a row shouldn't require
	// closing the prompt first. Arrows (plus emacs-style ctrl+n/p; j/k
	// are text here) move the selection; the central Update wrapper's
	// followCursor call keeps the split pane tracking it.
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
		m.ensureCursorVisible()
		return m, nil
	}
	// esc and enter close the prompt and KEEP the query. The filter
	// already applies live as the user types, so closing is just
	// "done typing" — no separate apply step, and no way to lose the
	// filter by reaching for the wrong key. The status bar's
	// filter:"…" chip explains the narrowed list once the prompt is
	// gone, and esc in the list (or / + clearing the text) drops it.
	// / closes too, but only on an empty input: once there's text it
	// types a literal slash, since branches (feat/x) and paths in
	// descriptions are matched fields that need one.
	if msg.Type == tea.KeyEsc || msg.Type == tea.KeyEnter ||
		(keyHit(msg, m.keys.Filter) && m.input.Value() == "") {
		// Trim once so the lookup key, the applied query, and the
		// status banner all agree — a stray trailing space on
		// "@nope " used to keep the raw value as the literal
		// query while the banner reported the trimmed form.
		raw := strings.TrimSpace(m.input.Value())
		// Alias expansion: a value of `@name` swaps the query
		// for the saved alias before applying. A miss keeps the
		// raw `@name` as a literal fuzzy query (so a row with
		// `@name` in its title still matches) and surfaces a
		// status banner so the user knows the alias didn't
		// resolve. Multi-word values starting with `@` (e.g.
		// "@blocked something") are not expanded — keeps the
		// rule narrow and predictable.
		if q, ok := m.filterAliases.Lookup(raw); ok {
			m.query = q
		} else {
			m.query = raw
			if strings.HasPrefix(raw, "@") {
				m.setStatus("no filter alias for " + raw)
			}
		}
		m.mode = modeList
		m.input.Blur()
		m.recomputeVisible()
		m.ensureCursorVisible()
		// A widened filter can pull in rows whose deps aren't cached
		// under an active deps sort; resolve them (nil Cmd otherwise).
		depCmd := m.maybeResolveDeps()
		if m.status != "" {
			return m, tea.Batch(flashClearCmd(m.statusGen), depCmd)
		}
		return m, depCmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.query = m.input.Value()
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, tea.Batch(cmd, m.maybeResolveDeps())
}

// recomputeVisible applies the text filter to m.all. The TITLE is matched
// with sahilm/fuzzy (subsequence, rank-based: best-first, ties fall back
// to position in m.all so the cursor doesn't jump as the user types) so
// abbreviations like "rpw" → "rotate password" work. Every other field —
// repo, branch, ID, description — is matched as a case-insensitive
// contiguous substring (repo being the most common target). Fields are
// matched independently so a query can't span a field boundary, and
// title matches rank above the rest.
func (m *Model) recomputeVisible() {
	// Apply the priority cap first so the fuzzy ranking only ever
	// runs over rows the user actually wants to see. -1 means "no
	// cap"; the test below short-circuits.
	pool := m.all
	if m.priorityCap >= 0 {
		filtered := make([]beads.Issue, 0, len(m.all))
		for _, i := range m.all {
			if i.Priority <= m.priorityCap {
				filtered = append(filtered, i)
			}
		}
		pool = filtered
	}

	if m.query == "" {
		// No fuzzy filter: clone so the sort below doesn't reorder
		// m.all, which the rest of the model expects to be in
		// bd's native order.
		out := make([]beads.Issue, len(pool))
		copy(out, pool)
		applySort(out, m.sortBy, m.sortDesc, m.depCache)
		m.visible = out
		m.titleMatches = nil // no filter → no highlight
		if m.cursor >= len(m.visible) {
			m.cursor = max(0, len(m.visible)-1)
		}
		return
	}

	// The title is matched with sahilm/fuzzy (subsequence), which makes
	// short abbreviations like "rpw" → "rotate password" work — the title
	// is short, so a subsequence is a meaningful signal. Every OTHER field
	// (repo, branch, ID, description) is matched as a case-insensitive
	// CONTIGUOUS SUBSTRING. Repo is the most common target ("show me the
	// android repo's rows"), and substring keeps it predictable: a fuzzy
	// subsequence over the long description matched almost anything (a
	// 7-char query like "android" lands a scattered a·n·d·r·o·i·d in nearly
	// every body), flooding the filter. Title matches outrank the rest.
	titleScore := make(map[int]int, len(pool))
	metaMatch := make(map[int]bool, len(pool))
	m.titleMatches = make(map[string][]int, len(pool))
	for _, mt := range fuzzy.FindFrom(m.query, titleSource(pool)) {
		titleScore[mt.Index] = mt.Score
		// Capture rune-index positions so renderRow can style
		// each matched rune. fuzzy.MatchedIndexes are byte
		// offsets into the source string; convert here once so
		// renderRow stays a fast formatter. Key by issueKey (not
		// bare ID) so two cross-repo issues with colliding IDs
		// don't overwrite each other's match indices.
		m.titleMatches[issueKey(pool[mt.Index])] = byteToRuneIdxs(pool[mt.Index].Title, mt.MatchedIndexes)
	}
	lowerQuery := strings.ToLower(m.query)
	contains := func(s string) bool {
		return s != "" && strings.Contains(strings.ToLower(s), lowerQuery)
	}
	for idx := range pool {
		if _, ok := titleScore[idx]; ok {
			continue // already a (stronger) title match
		}
		i := pool[idx]
		if contains(i.Repo) || contains(i.Branch) || contains(i.ID) || contains(i.Description) {
			metaMatch[idx] = true
		}
	}

	type scored struct {
		idx, score int
		title      bool
	}
	list := make([]scored, 0, len(titleScore)+len(metaMatch))
	for idx, score := range titleScore {
		list = append(list, scored{idx, score, true})
	}
	for idx := range metaMatch {
		list = append(list, scored{idx: idx, title: false})
	}
	sort.Slice(list, func(i, j int) bool {
		// Title matches first; within title matches, by fuzzy score.
		if list[i].title != list[j].title {
			return list[i].title
		}
		if list[i].title && list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].idx < list[j].idx
	})
	out := make([]beads.Issue, 0, len(list))
	for _, s := range list {
		out = append(out, pool[s.idx])
	}
	// Sort overrides the fuzzy-score ordering when set — the user
	// asked for a specific axis, honour it.
	applySort(out, m.sortBy, m.sortDesc, m.depCache)
	m.visible = out

	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

// setPriorityCap updates the priority filter and re-runs the
// visible-row pipeline. Cursor resets to 0 since the previous
// position is meaningless against a different filter; scroll
// re-clamps so the (now smaller or larger) list doesn't leave the
// cursor offscreen. Param named capLevel to avoid shadowing Go's
// builtin cap() — protects future edits that might add a
// slice-capacity check inside the function.
func (m Model) setPriorityCap(capLevel int) (tea.Model, tea.Cmd) {
	m.priorityCap = capLevel
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
}

// setSortKey rotates the active sort and re-runs the visible-row
// pipeline. Cursor resets to 0 because the user's previous
// position has no meaning against a re-ordered list. Direction
// resets to natural (sortDesc=false) on axis change — preserving
// the reverse across an axis switch would carry an "unexpected
// direction" surprise into the next sort.
func (m Model) setSortKey(k sortKey) (tea.Model, tea.Cmd) {
	m.sortBy = k
	m.sortDesc = false
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	// Entering the deps sort kicks off async resolution of any
	// visible row's edges we don't have cached yet; the first paint
	// uses the count proxy and re-sorts when depsResolvedMsg lands.
	return m, m.maybeResolveDeps()
}

// reverseSort flips m.sortDesc and re-runs the visible-row
// pipeline. No-op when no axis is active — sortNone has no
// direction to reverse, and a status banner is more useful than
// a silent no-press.
func (m Model) reverseSort() (tea.Model, tea.Cmd) {
	if m.sortBy == sortNone {
		m.setStatus("S: pick a sort first (press s)")
		return m, flashClearCmd(m.statusGen)
	}
	m.sortDesc = !m.sortDesc
	m.cursor = 0
	m.recomputeVisible()
	m.ensureCursorVisible()
	return m, nil
}

// parseSortKey maps a string axis name to its sortKey constant.
// Used by `:sort` so the command palette can drive the same
// rotation the `s` key cycles through. Empty input is rejected
// here — `:sort` with no args is a usage error, handled in the
// caller before reaching this helper.
func parseSortKey(s string) (sortKey, bool) {
	switch strings.ToLower(s) {
	case "none":
		return sortNone, true
	case "priority", "p":
		return sortPriority, true
	case "updated":
		return sortUpdated, true
	case "repo":
		return sortRepo, true
	case "id":
		return sortID, true
	case "deps", "dep":
		return sortDeps, true
	}
	return sortNone, false
}

// toggleShowClosed flips the include-closed flag on both the model
// (for chip rendering) and the underlying Source (for the next
// fetch query / bd subcommand choice), then triggers a refetch so
// the rows reflect the new scope.
func (m Model) toggleShowClosed() (tea.Model, tea.Cmd) {
	m.showClosed = !m.showClosed
	if tog, ok := m.src.(ClosedToggler); ok {
		tog.SetIncludeClosed(m.showClosed)
	}
	// The cached per-preset rows were fetched under the OLD
	// showClosed scope, so they'd paint the wrong set (closed rows
	// present/absent) on the next switch. Drop them; they repopulate
	// from fresh fetches.
	m.presetCache = nil
	m.cursor = 0
	m.refreshing = true
	return m, m.fetchCmd()
}

// byteToRuneIdxs converts the byte-offset slice fuzzy returns into
// rune-index positions inside the source string. sahilm/fuzzy
// works on bytes, but renderer logic for highlighting wants
// rune-aligned positions so a multi-byte glyph isn't half-styled.
// Assumes byteIdxs is sorted ascending (which fuzzy guarantees).
func byteToRuneIdxs(s string, byteIdxs []int) []int {
	if len(byteIdxs) == 0 {
		return nil
	}
	out := make([]int, 0, len(byteIdxs))
	runeIdx := 0
	next := 0
	for byteOff := range s {
		if next < len(byteIdxs) && byteOff == byteIdxs[next] {
			out = append(out, runeIdx)
			next++
			if next == len(byteIdxs) {
				break
			}
		}
		runeIdx++
	}
	return out
}
