package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// beadsDirName is the workspace marker bd creates at a repo root. wyk's
// registry entries are the directory CONTAINING it, never the directory
// itself — see scanForBeadsRepos in init.go, which applies the same rule
// to a whole tree.
const beadsDirName = ".beads"

// findBeadsRoot walks up from dir looking for the nearest ancestor (dir
// itself included) that holds a .beads/ directory, mirroring bd's own
// upward auto-discovery. Returns the workspace root and true on a hit,
// ("", false) when the walk reaches the filesystem root without finding
// one — or when dir can't be made absolute.
//
// An empty dir means "the process working directory", matching the
// convention beads.Client.Dir uses: unset → inherit cwd.
func findBeadsRoot(dir string) (string, bool) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		dir = cwd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for cur := filepath.Clean(abs); ; {
		if fi, err := os.Stat(filepath.Join(cur, beadsDirName)); err == nil && fi.IsDir() {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// maybeAutoRegister is the seam every wyk write goes through to keep the
// repo it just wrote to visible in the multi-repo views. Production wires
// it to realMaybeAutoRegister; tests swap a stub so they can assert the
// call without touching the user's real repos.json.
var maybeAutoRegister = realMaybeAutoRegister

// realMaybeAutoRegister registers the bd workspace containing dir in
// ~/.config/wyk/repos.json when it isn't there already, printing a
// one-line notice to w so the write is never silent.
//
// This exists because an unregistered workspace silently swallowed
// handoffs, including a P0 (would-you-kindly-afo3). An agent could
// follow the handoff convention exactly — wyk create, wyk handoff,
// correct labels — and the work still never reached a human, because
// `wyk inbox` / `wyk dashboard` / the TUI all read the registry and the
// repo simply wasn't in it. Registration used to happen only as a side
// effect of `wyk init`, which a headless agent never runs.
//
// It is deliberately best-effort and NEVER changes the caller's exit
// code: the create/handoff it follows has already succeeded, and failing
// the command over a registry write would turn a bookkeeping problem
// into a lost issue. Registry failures degrade to a stderr warning.
//
// Undo is `wyk registry remove <name>`; a workspace that later moves or
// disappears is swept by `wyk registry prune`.
func realMaybeAutoRegister(cmdName, dir string, w io.Writer) {
	root, ok := findBeadsRoot(dir)
	if !ok {
		// Not a bd workspace (or unreadable). bd itself would have
		// failed the write we're following, so there is nothing to
		// register and nothing worth warning about.
		return
	}
	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintf(w, "%s: could not resolve the repo registry to auto-register %s: %v\n", cmdName, root, err)
		return
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintf(w, "%s: could not read %s to auto-register %s: %v\n", cmdName, regPath, root, err)
		return
	}
	if reg.Has(root) {
		return
	}
	if err := reg.Add(root); err != nil {
		fmt.Fprintf(w, "%s: could not auto-register %s: %v\n", cmdName, root, err)
		return
	}
	if err := reg.Save(regPath); err != nil {
		fmt.Fprintf(w, "%s: could not save %s to auto-register %s: %v\n", cmdName, regPath, root, err)
		return
	}
	fmt.Fprintf(w, "%s: registered %s in %s\n", cmdName, root, regPath)
	fmt.Fprintf(w, "%s:   (it was unregistered, so issues filed here were invisible to `wyk inbox`, `wyk dashboard`, and the TUI)\n", cmdName)
}
