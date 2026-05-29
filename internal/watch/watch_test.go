package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_EmitsDebouncedSignalOnFileWrite(t *testing.T) {
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed an existing file so the initial inotify watch attaches
	// to a real inode.
	jsonlPath := filepath.Join(beads, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(ctx, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Trigger a write — should produce a single debounced event.
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"a"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-w.Events():
		// good
	case <-time.After(2 * time.Second):
		t.Fatalf("expected an event within 2s; got none")
	}
}

func TestWatcher_CoalescesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beads, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(ctx, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Hammer with five writes inside the debounce window. The
	// watcher should emit at most once before the next debounce
	// period elapses — the unbuffered/buffered-1 channel can't
	// stack more than one pending event.
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(jsonlPath, []byte(`{}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Drain the first event.
	select {
	case <-w.Events():
	case <-time.After(2 * time.Second):
		t.Fatalf("expected at least one event")
	}

	// A second event MIGHT arrive (the channel buffer is 1) — but
	// we should not get a flood. Wait a debounce period; the
	// channel should have at most one queued event.
	time.Sleep(2 * DebouncePeriod)
	extras := 0
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				break
			}
			extras++
			if extras > 1 {
				t.Errorf("expected coalesced events; got %d extras", extras)
				return
			}
			continue
		default:
		}
		break
	}
}

func TestWatcher_MissingBeadsDirSkipped(t *testing.T) {
	// A repo root with no .beads directory yet (pre-`bd init`)
	// should not block New — it should just register zero watches
	// and run quietly until that repo grows a .beads.
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(ctx, []string{dir})
	if err != nil {
		t.Fatalf("New should not error on missing .beads; got %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// No events should arrive — but this isn't a regression test
	// for that; it's just checking that startup didn't fail.
}

func TestWatcher_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := New(context.Background(), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close should be a no-op; got %v", err)
	}
}
