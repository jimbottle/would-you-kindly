// Package watch fires a single, debounced "something changed in
// a tracked bd workspace" signal on every filesystem event under
// the registered .beads directories. The TUI subscribes once at
// startup; every tick the channel emits, the model dispatches the
// same refresh it would otherwise run from the 10-second timer —
// just sooner. The polling timer stays in place as a fallback for
// platforms where fsnotify can't watch the path (rare; mostly
// network filesystems).
//
// Debounce period coalesces back-to-back writes (e.g. five repos
// updating from one `git pull`) into a single refresh — without
// it, a bd subprocess fanning out across the registry could
// trigger N sequential refetches in rapid succession.
package watch

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DebouncePeriod is how long the watcher waits for additional
// events before emitting a single signal. 250ms is short enough
// to feel instant against human reaction time, long enough to
// coalesce a five-repo `git pull` fan-out.
const DebouncePeriod = 250 * time.Millisecond

// Watcher coalesces fsnotify events from one or more .beads
// directories into a single "refresh now" channel. Callers read
// from Events; the watcher closes it when Close is called or the
// ctx cancels, so a `for range` loop terminates cleanly.
type Watcher struct {
	fs     *fsnotify.Watcher
	events chan struct{}

	mu     sync.Mutex
	closed bool
}

// New starts a watcher over every supplied repo root (the
// directory containing .beads/, NOT .beads itself — we watch the
// issues.jsonl path inside it). Each path that fails to register
// is silently skipped; a partially-watching watcher is more
// useful than refusing to start when one repo's .beads is
// momentarily missing. The returned watcher takes ownership of
// ctx — when ctx cancels, the internal goroutine exits and
// Events is closed.
func New(ctx context.Context, repoRoots []string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	for _, root := range repoRoots {
		// Watch the .beads directory itself rather than the
		// issues.jsonl file — bd's writes go through a temp-file
		// rename which fsnotify reports as a CREATE on the dir,
		// not a WRITE on the file. Watching the dir catches both
		// edge cases (rename-over and direct edits) without
		// extra plumbing.
		beadsDir := filepath.Join(root, ".beads")
		_ = fsw.Add(beadsDir) // best-effort; missing dirs are common pre-`bd init`
	}
	w := &Watcher{
		fs:     fsw,
		events: make(chan struct{}, 1),
	}
	go w.run(ctx)
	return w, nil
}

// Events returns the channel callers drain to learn about
// debounced bd-workspace changes. Each receive == "at least one
// write hit a watched .beads directory since the last receive."
// The channel buffer is 1 so a slow consumer doesn't lose the
// signal that something is pending, but never queues up unbounded
// notifications.
func (w *Watcher) Events() <-chan struct{} {
	return w.events
}

// Close stops the watcher and closes Events. Safe to call
// multiple times.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	return w.fs.Close()
}

// run reads fsnotify events, debounces them, and emits to
// w.events. Exits when ctx cancels or w.fs.Events closes.
func (w *Watcher) run(ctx context.Context) {
	defer func() {
		close(w.events)
	}()
	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			_ = w.Close()
			return
		case _, ok := <-w.fs.Events:
			if !ok {
				return
			}
			// Coalesce: reset the debounce timer on every new
			// event so a flurry of writes only emits once,
			// DebouncePeriod after the LAST write.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(DebouncePeriod, func() {
				// Non-blocking send: if the previous tick is
				// still in the buffer, dropping the new one is
				// fine — the consumer will read it and trigger
				// a refresh that already covers both events.
				select {
				case w.events <- struct{}{}:
				default:
				}
			})
		case _, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			// Swallow errors: a transient fsnotify error on one
			// path shouldn't kill the whole watch. The next event
			// will retry; if the underlying watcher is dead,
			// w.fs.Events will close and we'll exit above.
		}
	}
}
