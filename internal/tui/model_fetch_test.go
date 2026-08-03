package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// This file holds the fetch lifecycle: ticks, refreshes, fs events, stale results,
// and the error banners.
//
// Split out of a single 6.7k-line model_test.go (would-you-kindly-380g);
// Go compiles the package identically either way, so this is navigation
// only — no test body was changed in the move.

func TestErrorStateShowsFriendlyMessage(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: src.err})
	out := model.(Model).View()
	if !strings.Contains(out, "bd is not installed") {
		t.Errorf("error view missing friendly bd-not-installed copy:\n%s", out)
	}
}

func TestStaleFetchIsDroppedAfterPresetChange(t *testing.T) {
	// A tick fires while the user is on the default preset, then the
	// user switches to PresetHuman before the fetch returns. The late
	// fetched message must not overwrite the model's state.
	//
	// The "no-blank-on-switch" change keeps the OLD preset's rows
	// visible during the switch (so users don't see a wiped table
	// for the duration of bd's round-trip); the dropped-stale
	// invariant is about NEW data not overwriting NEWER state, so
	// we check that the stale fetch leaves m.all == the old rows
	// rather than asserting m.all is cleared.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	wantCount := len(m.all)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)
	if !m.refreshing {
		t.Errorf("preset switch should set refreshing=true; got false")
	}

	// late fetch for the OLD preset arrives — must NOT clobber the
	// current preset's rows even though they're still the OLD data
	// on screen.
	stale := []beads.Issue{{ID: "stale-1", Title: "stale", Labels: []string{}}}
	model, _ = m.Update(fetchedMsg{preset: filter.PresetAll, issues: stale})
	m = model.(Model)
	if len(m.all) != wantCount {
		t.Errorf("stale fetch should have been dropped; m.all changed from %d to %d", wantCount, len(m.all))
	}
}

func TestTickSuspendsOnTerminalError(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)
	_, cmd := m.Update(tickMsg{gen: m.tickGen})
	if cmd != nil {
		t.Error("tick should not re-arm while error state is terminal")
	}
}

// TestTickCoalescesWhileRefreshing proves the in-flight guard: a tick
// that fires while a fetch is already in flight (m.refreshing)
// reschedules the next tick but does NOT pile on a second overlapping
// fetch. This is the guard that stops the poll-interval-vs-timeout
// collision from becoming self-sustaining — without it, a slow
// refresh would be lapped by the next tick and double the cold-start
// load on the embedded-Dolt engines.
func TestTickCoalescesWhileRefreshing(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	// A fetch is in flight: not the first-paint load (that's
	// cleared by applyFetched), but a previous tick / manual refresh
	// whose fetchedMsg hasn't landed yet.
	m.refreshing = true
	callsBefore := src.calls

	_, cmd := m.Update(tickMsg{gen: m.tickGen})
	// drainCmd runs the returned command (and any batched children)
	// for their side effects; if a fetch had been batched in, the
	// stub's Fetch would bump src.calls.
	drainCmd(cmd)

	if cmd == nil {
		t.Fatal("tick while refreshing should still reschedule the next tick")
	}
	if src.calls != callsBefore {
		t.Errorf("tick dispatched a second fetch while one was already in flight; calls before=%d after=%d", callsBefore, src.calls)
	}
}

// TestTickCoalescesWhileLoading is the same guard for the initial-
// load case: the first-paint fetch is still outstanding (m.loading),
// so a tick must reschedule without launching an overlapping fetch.
func TestTickCoalescesWhileLoading(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := New(src) // loading == true by construction; no fetch has landed
	callsBefore := src.calls

	_, cmd := m.Update(tickMsg{gen: m.tickGen})
	drainCmd(cmd)

	if cmd == nil {
		t.Fatal("tick while loading should still reschedule the next tick")
	}
	if src.calls != callsBefore {
		t.Errorf("tick dispatched a fetch while the first-paint load was in flight; calls before=%d after=%d", callsBefore, src.calls)
	}
}

// TestTickDispatchesFetchWhenIdle is the positive case: a tick that
// fires with no fetch in flight DOES start one (and reschedules),
// confirming the coalesce guard doesn't wedge the poll loop shut.
func TestTickDispatchesFetchWhenIdle(t *testing.T) {
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src) // loading + refreshing both cleared
	callsBefore := src.calls

	_, cmd := m.Update(tickMsg{gen: m.tickGen})
	drainCmd(cmd)

	if src.calls <= callsBefore {
		t.Errorf("idle tick should dispatch a fetch; calls before=%d after=%d", callsBefore, src.calls)
	}
}

func TestTerminalErrorBannerAppendsRetryHint(t *testing.T) {
	// Terminal errors (bd missing, no workspace) also suspend the
	// auto-refresh tick, so the small banner can't rely on the next
	// tick recovering. The recovery path is `r`, and the user needs
	// an explicit cue in the banner.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)

	out := m.View()
	if !strings.Contains(out, "press r to retry") {
		t.Errorf("terminal-error banner should append the retry hint; got:\n%s", out)
	}
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("terminal error should still leave the table visible; got:\n%s", out)
	}
}

func TestTransientErrorBannerOmitsRetryHint(t *testing.T) {
	// Transient errors recover on the next 10s tick on their own —
	// the explicit "press r to retry" hint is only needed for
	// terminal errors that suspend auto-refresh. Keep the banner
	// terse for the common flaky-bd case.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(fetchedMsg{preset: m.preset, err: errors.New("bd: transient flake")})
	m = model.(Model)

	out := m.View()
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("transient error should still surface as a banner; got:\n%s", out)
	}
	if strings.Contains(out, "press r to retry") {
		t.Errorf("transient banner should NOT include the retry hint (next tick recovers); got:\n%s", out)
	}
}

func TestTransientFetchErrorKeepsTableVisible(t *testing.T) {
	// The "no-blank-on-refresh" invariant: once we have data on
	// screen, a transient bd error during an auto-refresh tick
	// surfaces as a small banner — the table stays put. Pre-fix,
	// any non-nil m.lastErr caused viewList to replace the whole
	// table with a full-screen "press r to retry" stand-in, which
	// is the "TUI blanks on refresh" symptom the user reported.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	out := m.View()
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Fatalf("setup: initial view should show issue rows; got:\n%s", out)
	}

	// Simulate a flaky bd query: tick → fetch returns error.
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: errors.New("bd: transient flake")})
	m = model.(Model)

	out = m.View()
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("transient fetch error should leave the table visible; got:\n%s", out)
	}
	if strings.Contains(out, "press r to retry") {
		t.Errorf("transient fetch error should not show the full-screen retry hint; got:\n%s", out)
	}
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("transient fetch error should surface as a 'refresh failed' banner; got:\n%s", out)
	}
}

func TestRefreshKeyKeepsTableVisible(t *testing.T) {
	// Pressing `r` no longer blanks the screen: the table stays
	// up, a small ↻ refreshing hint appears in the status bar.
	// Replaces the previous "loading…" full-screen blank that
	// fired on every keypress of r.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(Model)

	if m.loading {
		t.Error("manual refresh should not set loading=true (blanks the view)")
	}
	if !m.refreshing {
		t.Error("manual refresh should set refreshing=true (status-bar indicator)")
	}
	out := m.View()
	if strings.Contains(out, "loading…") {
		t.Errorf("manual refresh view should not show full-screen loading…; got:\n%s", out)
	}
	if !strings.Contains(out, sampleIssues()[0].Title) {
		t.Errorf("manual refresh view should still show issue rows; got:\n%s", out)
	}
	if !strings.Contains(out, "refreshing") {
		t.Errorf("manual refresh view should show the ↻ refreshing indicator; got:\n%s", out)
	}
}

func TestSwitchPresetKeepsRowsAndShowsRefreshIndicator(t *testing.T) {
	// Switching presets no longer blanks the table — the previous
	// rows stay on screen until the new fetch returns, with a
	// subtle "↻ refreshing" hint in the status bar. The cursor
	// still resets to 0 so the user lands at the top of the new
	// view as soon as data arrives.
	src := &stubSource{issues: sampleIssues()}
	m := applyFetched(New(src), src)
	preCount := len(m.all)
	if preCount == 0 {
		t.Fatal("setup: sampleIssues should yield at least one row")
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = model.(Model)

	if len(m.all) != preCount {
		t.Errorf("switchPreset must NOT clear all (blanks the screen); got all=%d, want %d", len(m.all), preCount)
	}
	if m.cursor != 0 {
		t.Errorf("switchPreset should reset cursor; got %d", m.cursor)
	}
	if m.loading {
		t.Error("switchPreset should NOT set loading=true (that's the full-screen blank path)")
	}
	if !m.refreshing {
		t.Error("switchPreset should set refreshing=true")
	}
	out := m.View()
	if strings.Contains(out, "loading…") {
		t.Errorf("view should not render the full-screen loading indicator on a preset switch:\n%s", out)
	}
	if !strings.Contains(out, "refreshing") {
		t.Errorf("view should render the refresh indicator in the status bar:\n%s", out)
	}
}

func TestInitialPaintShowsLoading(t *testing.T) {
	// Before the first fetchedMsg arrives, the view must say "loading…"
	// rather than render the "no issues — bd returned an empty list"
	// empty state, which is indistinguishable from a slow first fetch.
	src := &stubSource{issues: sampleIssues()}
	m := New(src)
	if !m.loading {
		t.Fatal("New(...) should construct a Model in loading state")
	}
	out := m.View()
	if !strings.Contains(out, "loading…") {
		t.Errorf("initial paint should render loading indicator; got:\n%s", out)
	}
	if strings.Contains(out, "no issues") {
		t.Error("initial paint should NOT render the empty-list state before the first fetch")
	}
}

func TestRefreshAfterTerminalErrorRestartsTickAndRetiresOldChain(t *testing.T) {
	src := &stubSource{err: beads.ErrBDNotFound}
	m := New(src)
	// land a terminal error and let the tick handler retire the current chain.
	model, _ := m.Update(fetchedMsg{preset: m.preset, err: beads.ErrBDNotFound})
	m = model.(Model)
	model, _ = m.Update(tickMsg{gen: m.tickGen})
	m = model.(Model)
	preGen := m.tickGen

	// manual refresh from the error state: tickGen bumps and a new tick is scheduled.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(Model)
	if m.tickGen <= preGen {
		t.Errorf("refresh from terminal error should bump tickGen (was %d, now %d)",
			preGen, m.tickGen)
	}
	if cmd == nil {
		t.Error("refresh from terminal error should produce a batched command")
	}

	// a tick from the OLD generation must be dropped — it would otherwise
	// re-arm and yield duplicate tick chains forever.
	_, staleCmd := m.Update(tickMsg{gen: preGen})
	if staleCmd != nil {
		t.Error("stale-gen tick should be dropped, not re-arm a chain")
	}
}

func TestFSEventMsg_DispatchesNonNilCmd(t *testing.T) {
	// A watcher tick should batch a refetch + wait re-arm. We
	// can't easily inspect a tea.Batch's contents from a unit
	// test (it returns a BatchMsg the runtime then re-dispatches),
	// so the contract test is just: a non-nil cmd came back.
	// The fetchCmd path itself is exhaustively covered by every
	// other write-handler test.
	src := &stubSource{issues: sampleIssues()}
	events := make(chan struct{}, 1)
	m := applyFetched(New(src).WithFSEvents(events), src)

	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Errorf("fsEventMsg should produce a batched refetch+rearm cmd; got nil")
	}
}

func TestFSEventMsg_SuspendedOnTerminalError(t *testing.T) {
	// Terminal-error suspension (no bd / no workspace) still
	// applies — refetching when there's no source to query just
	// wastes work. The wait still re-arms so a later recovery
	// (user fixes PATH) can come through. We distinguish the
	// suspended branch from the active branch by the side effect
	// the inner fetchCmd would produce: src.calls ticks if and
	// only if a refetch was batched. The wait callback reads from
	// a pre-loaded channel so neither branch blocks the test.
	src := &stubSource{err: beads.ErrBDNotFound}
	events := make(chan struct{}, 1)
	events <- struct{}{} // pre-load so waitFSEvent returns immediately
	m := applyFetched(New(src).WithFSEvents(events), src)
	m.lastErr = beads.ErrBDNotFound // isTerminalErr definitely matches this

	callsBefore := src.calls
	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Fatalf("even in terminal-error state, the wait should re-arm")
	}
	drainCmd(cmd)
	if src.calls != callsBefore {
		t.Errorf("terminal-error suspension should NOT refetch; calls=%d before=%d", src.calls, callsBefore)
	}
}

func TestFSEventMsg_CoalescesWhileFetchInFlight(t *testing.T) {
	// A fs event arriving while a fetch is already in flight must NOT
	// dispatch an overlapping fetch — it only re-arms the wait.
	// Otherwise a chatty .beads/ watcher storms the cold Dolt engines
	// (the "constant rapid refresh" bug). The pre-loaded channel lets
	// the re-armed waitFSEvent return without blocking the drain.
	src := &stubSource{issues: sampleIssues()}
	events := make(chan struct{}, 1)
	events <- struct{}{}
	m := applyFetched(New(src).WithFSEvents(events), src)
	m.refreshing = true // simulate an in-flight fetch

	callsBefore := src.calls
	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Fatal("fsEvent should still re-arm the wait even when coalescing")
	}
	drainCmd(cmd)
	if src.calls != callsBefore {
		t.Errorf("fsEvent while refreshing must NOT dispatch an overlapping fetch; calls=%d before=%d", src.calls, callsBefore)
	}
}

func TestFSEventMsg_IdleDispatchesAndMarksRefreshing(t *testing.T) {
	// The positive case: an idle fs event DOES refetch and sets
	// m.refreshing so the tick path coalesces against it (the two
	// refresh sources can't otherwise see each other's in-flight fetch).
	src := &stubSource{issues: sampleIssues()}
	events := make(chan struct{}, 1)
	events <- struct{}{}
	m := applyFetched(New(src).WithFSEvents(events), src) // clears loading+refreshing
	// Push lastSync past the rate-limit floor so this models a quiet
	// period (the common single-write case), not a burst.
	m.lastSync = time.Now().Add(-2 * minFSRefreshGap)

	callsBefore := src.calls
	model, cmd := m.Update(fsEventMsg{})
	m = model.(Model)
	if !m.refreshing {
		t.Error("an idle fsEvent fetch should set m.refreshing so ticks coalesce")
	}
	drainCmd(cmd)
	if src.calls != callsBefore+1 {
		t.Errorf("idle fsEvent should dispatch exactly one fetch; calls=%d before=%d", src.calls, callsBefore)
	}
}

func TestFSEventMsg_RateLimitsBurstRefreshes(t *testing.T) {
	// A fs event arriving within minFSRefreshGap of the last fetch
	// completion must NOT dispatch a refetch — only re-arm the wait.
	// This is what stops a chatty writer (a single bd write emits
	// ~130 fsnotify events; agents/git-pulls stream many writes) from
	// churning the multi-repo fetch back-to-back.
	src := &stubSource{issues: sampleIssues()}
	events := make(chan struct{}, 1)
	events <- struct{}{}
	m := applyFetched(New(src).WithFSEvents(events), src)
	m.lastSync = time.Now() // just synced → inside the cooldown

	callsBefore := src.calls
	_, cmd := m.Update(fsEventMsg{})
	if cmd == nil {
		t.Fatal("fsEvent should still re-arm the wait while rate-limited")
	}
	drainCmd(cmd)
	if src.calls != callsBefore {
		t.Errorf("fsEvent within the refresh gap must NOT refetch; calls=%d before=%d", src.calls, callsBefore)
	}
}

func TestRefreshDepCachesFromList_FreshensOnFetch(t *testing.T) {
	// An external status change arrives via a list refresh: the cached
	// dep row for that ID must pick up the new status (and title) from
	// the fetched list, so the detail view doesn't render stale data.
	src := &stubSource{issues: []beads.Issue{{ID: "a-1"}, {ID: "b-2", Title: "new title", Status: "in_progress"}}}
	m := applyFetched(New(src), src)
	m.depCache["a-1"] = []beads.Issue{{ID: "b-2", Title: "old title", Status: "open"}}

	// Re-deliver the fetch (simulating a refresh that carries b-2's
	// updated status).
	model, _ := m.Update(fetchedMsg{preset: m.preset, issues: src.issues})
	m = model.(Model)

	row := m.depCache["a-1"][0]
	if row.Status != "in_progress" || row.Title != "new title" {
		t.Errorf("cached dep row not freshened from list: got status=%q title=%q", row.Status, row.Title)
	}
}
