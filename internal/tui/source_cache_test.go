package tui

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filter"
)

// TestMultiBDSource_FetchCache exercises the per-repo fetch cache
// (would-you-kindly-jipr): unchanged repos are served from cache (no bd
// call) and invalidated by mtime change, preset change, InvalidateCache,
// SetIncludeClosed, and the TTL.
func TestMultiBDSource_FetchCache(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRepoSource{issues: []beads.Issue{{ID: "alpha-1", Title: "t"}}}
	m := &MultiBDSource{subs: []subRepo{{
		name:     "alpha",
		src:      fake,
		path:     dir,
		branchFn: func(_ context.Context) string { return "main" },
	}}}
	ctx := context.Background()
	count := func() int32 { return atomic.LoadInt32(&fake.fetchCount) }
	fetch := func(p filter.Preset) { _, _, _ = m.FetchWithSubErrors(ctx, p) }

	fetch(filter.PresetAll)
	if count() != 1 {
		t.Fatalf("first fetch count=%d, want 1", count())
	}
	fetch(filter.PresetAll) // unchanged → cache hit
	if count() != 1 {
		t.Errorf("unchanged repo should hit cache; count=%d, want 1", count())
	}

	// mtime bump → invalidate.
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(beadsDir, future, future); err != nil {
		t.Fatal(err)
	}
	fetch(filter.PresetAll)
	if count() != 2 {
		t.Errorf("mtime change should re-fetch; count=%d, want 2", count())
	}
	fetch(filter.PresetAll) // same mtime → hit again
	if count() != 2 {
		t.Errorf("second fetch at same mtime should hit; count=%d, want 2", count())
	}

	// Manual `r` → InvalidateCache.
	m.InvalidateCache()
	fetch(filter.PresetAll)
	if count() != 3 {
		t.Errorf("InvalidateCache should force re-fetch; count=%d, want 3", count())
	}

	// Different preset is a different cache key.
	fetch(filter.PresetReady)
	if count() != 4 {
		t.Errorf("preset change should re-fetch; count=%d, want 4", count())
	}

	// SetIncludeClosed invalidates the whole cache.
	m.SetIncludeClosed(true)
	fetch(filter.PresetReady)
	if count() != 5 {
		t.Errorf("SetIncludeClosed should invalidate; count=%d, want 5", count())
	}

	// TTL backstop: backdate the entry → next fetch misses.
	m.cacheMu.Lock()
	e := m.cache["alpha"]
	e.at = time.Now().Add(-2 * fetchCacheTTL)
	m.cache["alpha"] = e
	m.cacheMu.Unlock()
	fetch(filter.PresetReady)
	if count() != 6 {
		t.Errorf("TTL expiry should re-fetch; count=%d, want 6", count())
	}
}

// TestMultiBDSource_NoCacheWithoutPath confirms a sub with no path (the
// stub/test shape) is never cached — every fetch runs live.
func TestMultiBDSource_NoCacheWithoutPath(t *testing.T) {
	fake := &fakeRepoSource{issues: []beads.Issue{{ID: "alpha-1"}}}
	m := &MultiBDSource{subs: []subRepo{{name: "alpha", src: fake, branchFn: func(_ context.Context) string { return "" }}}}
	for i := 0; i < 3; i++ {
		_, _, _ = m.FetchWithSubErrors(context.Background(), filter.PresetAll)
	}
	if got := atomic.LoadInt32(&fake.fetchCount); got != 3 {
		t.Errorf("no-path sub should fetch live every time; count=%d, want 3", got)
	}
}
