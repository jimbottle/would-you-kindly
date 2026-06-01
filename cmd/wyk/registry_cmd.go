package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// runRegistry dispatches `wyk registry <sub>` to the matching
// handler. Centralises the registry-mutation CLI so users don't
// have to hand-edit ~/.config/wyk/repos.json to clean up dead
// entries — surfaced as a gap during the v0.2.3 cleanup pass.
//
// Subcommands:
//
//	list             dump every entry; -json for structured output.
//	remove <name>    drop a single entry by its display name.
//	prune            drop every entry whose path is gone or no
//	                 longer holds a .git (i.e. the repo was deleted,
//	                 moved, or had its git history removed). Asks
//	                 [y/N] before writing unless -y given.
func runRegistry(args []string) int {
	if len(args) == 0 {
		registryUsage(os.Stderr)
		return 64
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runRegistryList(rest)
	case "remove", "rm":
		return runRegistryRemove(rest)
	case "prune":
		return runRegistryPrune(rest, os.Stdin)
	case "-h", "--help", "help":
		registryUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "wyk registry: unknown subcommand %q\n", sub)
		registryUsage(os.Stderr)
		return 64
	}
}

func registryUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wyk registry <subcommand>

Subcommands:
  list             print registered repos (-json for structured output)
  remove <name>    remove the entry with the given display name
  prune            remove entries whose path / .git is missing
                   (-broken also drops present-but-no-bd-workspace entries; -y to skip confirm)

The registry lives at ~/.config/wyk/repos.json (XDG-aware).
`)
}

func runRegistryList(args []string) int {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit structured JSON instead of the plain-text layout")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	reg, regPath, err := loadRegistryForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry list:", err)
		return 1
	}
	if *asJSON {
		// Re-marshal so the output mirrors the on-disk schema
		// (version + repos) — scripts can parse the same shape
		// they'd see if they read repos.json directly.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reg); err != nil {
			fmt.Fprintln(os.Stderr, "wyk registry list:", err)
			return 1
		}
		return 0
	}
	if len(reg.Repos) == 0 {
		fmt.Printf("no repos registered (%s)\n", regPath)
		return 0
	}
	// Column-aligned: longest Name sets the gutter so paths align.
	// Width is the rune count, not byte length — a name with
	// multi-byte runes would otherwise over-pad and misalign the
	// path column. (%-*s still pads on bytes, so the alignment is
	// only approximate for truly wide content, but rune-counting
	// at least removes the gross over-pad.)
	width := 0
	for _, r := range reg.Repos {
		if w := utf8.RuneCountInString(r.Name); w > width {
			width = w
		}
	}
	for _, r := range reg.Repos {
		fmt.Printf("  %-*s  %s\n", width, r.Name, r.Path)
	}
	return 0
}

func runRegistryRemove(args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "wyk registry remove: usage: wyk registry remove <name>")
		return 64
	}
	name := args[0]
	reg, regPath, err := loadRegistryForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry remove:", err)
		return 1
	}
	if !reg.RemoveByName(name) {
		fmt.Fprintf(os.Stderr, "wyk registry remove: no entry named %q in %s\n", name, regPath)
		return 1
	}
	if err := reg.Save(regPath); err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry remove:", err)
		return 1
	}
	fmt.Printf("removed %q from %s\n", name, regPath)
	return 0
}

func runRegistryPrune(args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("registry prune", flag.ContinueOnError)
	yes := fs.Bool("y", false, "skip the [y/N] confirmation prompt")
	broken := fs.Bool("broken", false, "also drop entries whose path exists but holds no bd workspace (probes bd; only definitive 'no workspace' results qualify, not timeouts)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	reg, regPath, err := loadRegistryForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry prune:", err)
		return 1
	}
	dead := findDeadEntries(reg)
	if *broken {
		// Probe only the entries that survived the filesystem checks —
		// an already-dead path can't be probed and is covered anyway.
		skip := make(map[string]bool, len(dead))
		for _, d := range dead {
			skip[d.Path] = true
		}
		dead = append(dead, findBrokenWorkspaces(reg, skip, defaultWorkspaceProbe)...)
	}
	if len(dead) == 0 {
		fmt.Println("wyk registry prune: nothing to prune; every registered repo is reachable")
		return 0
	}
	fmt.Println("the following entries are unreachable and will be removed:")
	for _, d := range dead {
		fmt.Printf("  %-20s  %s  (%s)\n", d.Name, d.Path, d.reason)
	}
	if !*yes {
		fmt.Print("proceed? [y/N] ")
		ok, err := readYesNo(stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk registry prune:", err)
			return 1
		}
		if !ok {
			fmt.Println("aborted; no changes written")
			return 0
		}
	}
	// Remove by Path, not Name: Registry.Add derives Name from
	// filepath.Base and dedupes only on Path, so two repos at
	// different paths can share a Name (the nested `android` /
	// parent-`ebay-watchlist-watch` case from the v0.2.3 audit).
	// Removing by name would silently drop the wrong entry when an
	// alive and a dead repo share one.
	for _, d := range dead {
		if _, err := reg.Remove(d.Path); err != nil {
			fmt.Fprintln(os.Stderr, "wyk registry prune: remove:", err)
			return 1
		}
	}
	if err := reg.Save(regPath); err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry prune:", err)
		return 1
	}
	fmt.Printf("removed %d entr%s from %s\n", len(dead), plural(len(dead), "y", "ies"), regPath)
	return 0
}

// deadEntry pairs a registry Repo with a short, user-readable
// reason explaining why prune wants to drop it. Surfaced in the
// confirmation prompt so users can sanity-check before agreeing.
type deadEntry struct {
	registry.Repo
	reason string
}

// findDeadEntries returns every registry entry whose on-disk state
// can no longer support bd. Two classes of failure covered:
//
//   - path is unreachable (missing, permission denied, dangling
//     symlink) — wyk can't read the workspace at all
//   - path exists but has no `.git` (someone removed git history;
//     wyk wouldn't be able to install or run its hook)
//
// We treat ANY non-nil Stat error on the path as "unreachable"
// rather than only os.IsNotExist — a dangling symlink, EACCES, or
// I/O error is just as fatal for prune's purposes, and the
// alternative (treating them as alive and falling through to the
// .git check) would crash on a path that couldn't even be stat'd.
//
// We don't probe bd here — `wyk init -scan` already gates on bd,
// and an entry that can't even reach `.git` is unambiguously dead.
// A bd-broken-but-git-present entry is surfaced by `wyk doctor`
// instead, where the warning is more nuanced.
func findDeadEntries(reg *registry.Registry) []deadEntry {
	var dead []deadEntry
	for _, r := range reg.Repos {
		if _, err := os.Stat(r.Path); err != nil {
			reason := "path missing"
			if !os.IsNotExist(err) {
				reason = "path unreachable: " + err.Error()
			}
			dead = append(dead, deadEntry{Repo: r, reason: reason})
			continue
		}
		if _, err := os.Stat(filepath.Join(r.Path, ".git")); err != nil {
			reason := ".git missing"
			if !os.IsNotExist(err) {
				reason = ".git unreachable: " + err.Error()
			}
			dead = append(dead, deadEntry{Repo: r, reason: reason})
		}
	}
	return dead
}

// pruneProbeTimeout bounds each `-broken` bd probe so a locked or
// slow workspace can't hang the prune. Matches doctor's per-repo
// timeout so a repo that doctor flags as non-responsive and one prune
// would probe behave consistently.
const pruneProbeTimeout = 5 * time.Second

// defaultWorkspaceProbe is the production bd probe used by
// `registry prune -broken`: it runs a trivial bd query in dir and
// returns the resulting error (nil when the workspace responds). A
// package-level var so tests inject a stub without a real bd binary,
// mirroring beads.Client's runner seam.
var defaultWorkspaceProbe = func(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pruneProbeTimeout)
	defer cancel()
	c := beads.NewClient()
	c.Dir = dir
	_, err := c.Query(ctx, `status!=closed`)
	return err
}

// findBrokenWorkspaces returns registry entries whose path is present
// (not in skip — those are already covered by findDeadEntries) but
// which bd reports as having no workspace. It deliberately drops ONLY
// on beads.ErrNoWorkspace: a definitive "this .beads has no database"
// (an aborted `bd init`, the google_workspace_mcp case). A timeout,
// lock, or any other error is left ALONE — those are plausibly
// transient (syncing, slow FS) and pruning them would silently drop a
// repo the user still wants. This is why the bd probe is opt-in
// (`-broken`) rather than folded into the default filesystem prune.
func findBrokenWorkspaces(reg *registry.Registry, skip map[string]bool, probe func(dir string) error) []deadEntry {
	var broken []deadEntry
	for _, r := range reg.Repos {
		if skip[r.Path] {
			continue
		}
		if errors.Is(probe(r.Path), beads.ErrNoWorkspace) {
			broken = append(broken, deadEntry{Repo: r, reason: "no bd workspace"})
		}
	}
	return broken
}

// loadRegistryForCmd centralises the boilerplate around resolving
// the registry path and loading it. Returns the path too so error
// messages can name the file the user can hand-edit if needed.
func loadRegistryForCmd() (*registry.Registry, string, error) {
	regPath, err := registry.DefaultPath()
	if err != nil {
		return nil, "", err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, "", err
	}
	return reg, regPath, nil
}

// readYesNo accepts a single line from r and returns true iff the
// trimmed input is exactly "y" or "Y" or "yes" (case-insensitive).
// Everything else — including empty input (just Enter) — is no.
// "No" by default matches the [y/N] prompt convention.
func readYesNo(r io.Reader) (bool, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes", nil
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
