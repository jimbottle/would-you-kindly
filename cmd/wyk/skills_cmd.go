package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jimbottle/would-you-kindly/internal/skills"
)

// runSkills dispatches `wyk skills <sub>` — manage the Claude Code
// skills wyk ships for agents (authored in internal/skills/data,
// embedded in the binary). Install writes them to the user's
// ~/.claude/skills (default) or the project's ./.claude/skills so an
// agent's harness loads them on demand.
//
// Subcommands:
//
//	list                       provided skills + install state at the target
//	install [-user|-project]   write the skills (idempotent; -force overwrites
//	                           a locally-modified copy; -dry-run shows the plan)
//	uninstall [-user|-project] remove wyk's skills from the target
//	print <name>               emit one skill's SKILL.md to stdout
func runSkills(args []string) int {
	if len(args) == 0 {
		skillsUsage(os.Stderr)
		return 64
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runSkillsList(rest)
	case "install":
		return runSkillsInstall(rest, os.Stdin)
	case "uninstall", "remove":
		return runSkillsUninstall(rest, os.Stdin)
	case "print":
		return runSkillsPrint(rest)
	case "-h", "--help", "help":
		skillsUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "wyk skills: unknown subcommand %q\n", sub)
		skillsUsage(os.Stderr)
		return 64
	}
}

func skillsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wyk skills <subcommand>

Subcommands:
  list                     show the skills wyk provides + their install state
  install [-user|-project] write the skills to ~/.claude/skills (default) or
                           ./.claude/skills; idempotent. -force overwrites a
                           locally-modified copy; -dry-run prints the plan.
  uninstall [-user|-project] remove wyk's skills from the target
  print <name>             emit one skill's SKILL.md to stdout

Skills are authored in the binary and load on demand when their
description matches what you're doing. -user is the default.
`)
}

// skillState is the relationship between an embedded skill and the
// copy (if any) on disk at a target.
type skillState int

const (
	skillMissing  skillState = iota // not installed
	skillCurrent                    // installed, byte-identical to the embedded version
	skillModified                   // installed but differs (user edit, or an older wyk version)
)

func (s skillState) String() string {
	switch s {
	case skillCurrent:
		return "current"
	case skillModified:
		return "modified"
	default:
		return "not installed"
	}
}

// userSkillsDir resolves ~/.claude/skills, honoring $CLAUDE_CONFIG_DIR
// (the override Claude Code itself respects) before falling back to the
// home directory.
func userSkillsDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// projectSkillsDir is the repo-local target, relative to the current
// directory (run from the repo root, like the project's .claude/).
func projectSkillsDir() string {
	return filepath.Join(".claude", "skills")
}

// resolveSkillsTarget reads the -user/-project flags and returns the
// destination directory plus a human label. They're mutually exclusive;
// neither set means the user target (the default).
func resolveSkillsTarget(userFlag, projectFlag bool) (dir, label string, code int) {
	if userFlag && projectFlag {
		fmt.Fprintln(os.Stderr, "wyk skills: -user and -project are mutually exclusive")
		return "", "", 64
	}
	if projectFlag {
		return projectSkillsDir(), "project (./.claude/skills)", 0
	}
	d, err := userSkillsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk skills:", err)
		return "", "", 1
	}
	return d, "user (~/.claude/skills)", 0
}

// skillStateAt reports whether s is missing / current / modified at dir.
func skillStateAt(s skills.Skill, dir string) (skillState, error) {
	b, err := os.ReadFile(filepath.Join(dir, s.Name, "SKILL.md"))
	if os.IsNotExist(err) {
		return skillMissing, nil
	}
	if err != nil {
		return 0, err
	}
	if string(b) == s.Content {
		return skillCurrent, nil
	}
	return skillModified, nil
}

func runSkillsList(args []string) int {
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	userFlag := fs.Bool("user", false, "show state at the user target (~/.claude/skills) — the default")
	projectFlag := fs.Bool("project", false, "show state at the project target (./.claude/skills)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	dir, label, code := resolveSkillsTarget(*userFlag, *projectFlag)
	if code != 0 {
		return code
	}
	all, err := skills.All()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk skills:", err)
		return 1
	}
	fmt.Printf("wyk skills — %s state:\n\n", label)
	for _, s := range all {
		st, err := skillStateAt(s, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wyk skills: %s: %v\n", s.Name, err)
			return 1
		}
		fmt.Printf("  %-20s [%s]\n", s.Name, st)
		fmt.Printf("      %s\n", truncForList(s.Description))
	}
	return 0
}

// truncForList trims a description to a single readable line. Counts
// RUNES, not bytes, so a multi-byte character can't be split into
// invalid UTF-8 at the boundary — matching the project's rune-aware
// truncation convention.
func truncForList(s string) string {
	const max = 100
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func runSkillsInstall(args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	userFlag := fs.Bool("user", false, "install to ~/.claude/skills (the default)")
	projectFlag := fs.Bool("project", false, "install to ./.claude/skills instead")
	force := fs.Bool("force", false, "overwrite a locally-modified skill (default leaves modified copies untouched)")
	dryRun := fs.Bool("dry-run", false, "print what would be written without touching disk")
	yes := fs.Bool("y", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	dir, label, code := resolveSkillsTarget(*userFlag, *projectFlag)
	if code != 0 {
		return code
	}
	all, err := skills.All()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk skills:", err)
		return 1
	}

	// Plan first so the user sees exactly what install will do.
	type plan struct {
		skill  skills.Skill
		state  skillState
		action string // installed / updated / unchanged / skipped
		write  bool
	}
	plans := make([]plan, 0, len(all))
	nWrite := 0
	for _, s := range all {
		st, err := skillStateAt(s, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wyk skills: %s: %v\n", s.Name, err)
			return 1
		}
		p := plan{skill: s, state: st}
		switch st {
		case skillCurrent:
			p.action = "unchanged"
		case skillMissing:
			p.action, p.write = "install", true
		case skillModified:
			if *force {
				p.action, p.write = "overwrite (modified)", true
			} else {
				p.action = "skip (modified — pass -force to overwrite)"
			}
		}
		if p.write {
			nWrite++
		}
		plans = append(plans, p)
	}

	fmt.Printf("wyk skills install → %s\n", label)
	for _, p := range plans {
		fmt.Printf("  %-20s %s\n", p.skill.Name, p.action)
	}
	if nWrite == 0 {
		fmt.Println("nothing to write; every skill is already current (or modified — pass -force)")
		return 0
	}
	if *dryRun {
		fmt.Printf("(dry-run) would write %d skill(s)\n", nWrite)
		return 0
	}
	if !*yes {
		fmt.Printf("write %d skill(s) to %s? [y/N] ", nWrite, dir)
		ok, err := readYesNo(stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk skills:", err)
			return 1
		}
		if !ok {
			fmt.Println("aborted; no changes written")
			return 0
		}
	}
	for _, p := range plans {
		if !p.write {
			continue
		}
		if err := writeSkillFile(p.skill, dir); err != nil {
			fmt.Fprintf(os.Stderr, "wyk skills: %s: %v\n", p.skill.Name, err)
			return 1
		}
	}
	fmt.Printf("wrote %d skill(s) to %s\n", nWrite, dir)
	return 0
}

func runSkillsUninstall(args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("skills uninstall", flag.ContinueOnError)
	userFlag := fs.Bool("user", false, "uninstall from ~/.claude/skills (the default)")
	projectFlag := fs.Bool("project", false, "uninstall from ./.claude/skills instead")
	dryRun := fs.Bool("dry-run", false, "print what would be removed without touching disk")
	yes := fs.Bool("y", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	dir, label, code := resolveSkillsTarget(*userFlag, *projectFlag)
	if code != 0 {
		return code
	}
	all, err := skills.All()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk skills:", err)
		return 1
	}
	type victim struct {
		name     string
		modified bool
	}
	var victims []victim
	for _, s := range all {
		st, err := skillStateAt(s, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wyk skills: %s: %v\n", s.Name, err)
			return 1
		}
		if st == skillMissing {
			continue
		}
		victims = append(victims, victim{name: s.Name, modified: st == skillModified})
	}
	if len(victims) == 0 {
		fmt.Printf("wyk skills: none of wyk's skills are installed at %s\n", label)
		return 0
	}
	fmt.Printf("wyk skills uninstall → %s\n", label)
	for _, v := range victims {
		note := ""
		if v.modified {
			note = "  (locally modified — your edits will be lost)"
		}
		fmt.Printf("  %s%s\n", v.name, note)
	}
	if *dryRun {
		fmt.Printf("(dry-run) would remove %d skill(s)\n", len(victims))
		return 0
	}
	if !*yes {
		fmt.Printf("remove %d skill(s) from %s? [y/N] ", len(victims), dir)
		ok, err := readYesNo(stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk skills:", err)
			return 1
		}
		if !ok {
			fmt.Println("aborted; no changes written")
			return 0
		}
	}
	for _, v := range victims {
		if err := os.Remove(filepath.Join(dir, v.name, "SKILL.md")); err != nil {
			fmt.Fprintf(os.Stderr, "wyk skills: %s: %v\n", v.name, err)
			return 1
		}
		// Drop the now-empty skill dir; ignore the error if the user
		// kept other files alongside it (Remove only succeeds on empty).
		_ = os.Remove(filepath.Join(dir, v.name))
	}
	fmt.Printf("removed %d skill(s) from %s\n", len(victims), dir)
	return 0
}

func runSkillsPrint(args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: wyk skills print <name>")
		return 64
	}
	name := args[0]
	all, err := skills.All()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk skills:", err)
		return 1
	}
	for _, s := range all {
		if s.Name == name {
			fmt.Print(s.Content)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "wyk skills: no skill named %q. Available: %s\n", name, skillNames(all))
	return 1
}

func skillNames(all []skills.Skill) string {
	names := make([]string, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return joinComma(names)
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// writeSkillFile writes one skill's SKILL.md under dir/<name>/,
// creating the directory and writing atomically (temp file + rename)
// so a crash mid-write can't leave a half-written skill.
func writeSkillFile(s skills.Skill, dir string) error {
	skillDir := filepath.Join(dir, s.Name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", skillDir, err)
	}
	final := filepath.Join(skillDir, "SKILL.md")
	tmp, err := os.CreateTemp(skillDir, ".SKILL.md.*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.WriteString(s.Content); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, final, err)
	}
	return nil
}
