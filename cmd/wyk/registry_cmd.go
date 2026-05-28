package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
  prune            remove entries whose path / .git is missing (-y to skip confirm)

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
	width := 0
	for _, r := range reg.Repos {
		if len(r.Name) > width {
			width = len(r.Name)
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
	if err := fs.Parse(args); err != nil {
		return 64
	}
	reg, regPath, err := loadRegistryForCmd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk registry prune:", err)
		return 1
	}
	dead := findDeadEntries(reg)
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
	for _, d := range dead {
		reg.RemoveByName(d.Name)
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
// can no longer support bd. Two failure modes covered:
//
//   - path missing entirely (repo was deleted or moved without a
//     re-`wyk init` from the new location)
//   - path exists but has no `.git` (someone removed git history;
//     wyk wouldn't be able to install or run its hook)
//
// We don't probe bd here — `wyk init -scan` already gates on bd,
// and an entry that can't even reach `.git` is unambiguously dead.
// A bd-broken-but-git-present entry is surfaced by `wyk doctor`
// instead, where the warning is more nuanced.
func findDeadEntries(reg *registry.Registry) []deadEntry {
	var dead []deadEntry
	for _, r := range reg.Repos {
		if _, err := os.Stat(r.Path); os.IsNotExist(err) {
			dead = append(dead, deadEntry{Repo: r, reason: "path missing"})
			continue
		}
		if _, err := os.Stat(filepath.Join(r.Path, ".git")); os.IsNotExist(err) {
			dead = append(dead, deadEntry{Repo: r, reason: ".git missing"})
		}
	}
	return dead
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
