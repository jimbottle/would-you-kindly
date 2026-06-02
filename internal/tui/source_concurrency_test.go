package tui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raylytics/would-you-kindly/internal/beads"
	"github.com/raylytics/would-you-kindly/internal/filter"
)

// TestFetchWithSubErrors_BoundsConcurrency proves the per-repo
// `bd list` fan-out is throttled by the fetchConcurrency semaphore:
// even with far more registered repos than the cap, the observed peak
// of simultaneous sub-fetches never exceeds fetchConcurrency. This is
// the regression guard for the thundering-herd cold-start that tripped
// the per-repo 10s deadline on every refresh.
//
// It also re-asserts the two invariants the cap must NOT break: every
// repo still contributes its issue, and the union stays in stable
// registry order.
func TestFetchWithSubErrors_BoundsConcurrency(t *testing.T) {
	// More repos than the cap so an unbounded fan-out would visibly
	// exceed fetchConcurrency.
	const nRepos = fetchConcurrency * 3

	// One shared pair of counters across every sub — they measure the
	// peak concurrent fetch count across the whole fan-out.
	var curFetch, maxSeen int32
	subs := make([]subRepo, nRepos)
	for i := 0; i < nRepos; i++ {
		name := "r" + string(rune('a'+i))
		subs[i] = subRepo{
			name: name,
			src: &fakeRepoSource{
				issues: []beads.Issue{{ID: name + "-1", Title: "issue from " + name}},
				// A small but real sleep widens the window where
				// concurrent fetches overlap, so the peak counter
				// actually reaches the cap under -race. Kept tiny so
				// the suite stays snappy.
				fetchDelay: 10 * time.Millisecond,
				curFetch:   &curFetch,
				maxFetch:   &maxSeen,
			},
			branchFn: func(context.Context) string { return "main" },
		}
	}
	ms := &MultiBDSource{subs: subs}

	issues, subErrs, err := ms.FetchWithSubErrors(context.Background(), filter.PresetAll)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subErrs) != 0 {
		t.Fatalf("expected no sub errors, got %v", subErrs)
	}

	// Concurrency stayed within the cap.
	if got := atomic.LoadInt32(&maxSeen); got > int32(fetchConcurrency) {
		t.Fatalf("peak concurrent fetches %d exceeded cap %d", got, fetchConcurrency)
	}
	// Sanity: with the delay + many repos, the throttle should
	// actually have engaged (more than one fetch ran at once). If
	// this were 0/1 the test would pass vacuously even with a broken
	// cap, so guard against that too.
	if got := atomic.LoadInt32(&maxSeen); got < 2 {
		t.Fatalf("expected some overlap (peak >= 2) to make the cap meaningful, got %d", got)
	}

	// Every repo still contributed its single issue.
	if len(issues) != nRepos {
		t.Fatalf("expected %d issues (one per repo), got %d", nRepos, len(issues))
	}
	// Order is still stable registry order regardless of which sub
	// goroutine finished first.
	for i := 0; i < nRepos; i++ {
		want := subs[i].name + "-1"
		if issues[i].ID != want {
			t.Fatalf("issue %d out of registry order: got %q, want %q", i, issues[i].ID, want)
		}
	}
}
