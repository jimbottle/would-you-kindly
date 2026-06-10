package main

import (
	"strings"
	"testing"
)

func TestSubcommandHelp_NoMultiwordFlagPlaceholders(t *testing.T) {
	// Go's flag package takes the FIRST backquoted token in a usage
	// string as the flag's value placeholder — for every flag kind,
	// bools included. A multiword backquoted phrase ('wyk inbox',
	// 'dot -Tsvg') therefore renders as a nonsense value name:
	// "-identity wyk inbox", "-template wyk handoff <id> < filled.md"
	// (would-you-kindly-k3fb, roborev #2041/#2043). Sweep every
	// FlagSet-backed subcommand's -h output — including the nested
	// registry/skills/hook dispatchers — and reject any
	// flag-definition line with a multiword placeholder, so the bug
	// class can't quietly return on any flag.
	//
	// Deliberately NOT swept (the only flag.NewFlagSet sites absent
	// here): `create` forwards -h verbatim to a real bd binary, and
	// the top-level TUI flags live on flag.CommandLine whose -h path
	// os.Exits via printTopLevelUsage. `completion` has no FlagSet
	// at all.
	cmds := []struct {
		name string
		run  func([]string) int
		args []string
	}{
		{"handoff", runHandoff, []string{"-h"}},
		{"inbox", runInbox, []string{"-h"}},
		{"init", runInit, []string{"-h"}},
		{"depgraph", runDepgraph, []string{"-h"}},
		{"update", runUpdate, []string{"-h"}},
		{"export", runExport, []string{"-h"}},
		{"import", runImport, []string{"-h"}},
		{"activity", runActivity, []string{"-h"}},
		{"stats", runStats, []string{"-h"}},
		{"dashboard", runDashboard, []string{"-h"}},
		{"doctor", runDoctor, []string{"-h"}},
		{"conventions", runConventions, []string{"-h"}},
		{"version", runVersion, []string{"-h"}},
		{"help", runHelp, []string{"-h"}},
		{"hook post-commit", runHook, []string{"post-commit", "-h"}},
		{"registry list", runRegistry, []string{"list", "-h"}},
		{"registry prune", runRegistry, []string{"prune", "-h"}},
		{"skills list", runSkills, []string{"list", "-h"}},
		{"skills install", runSkills, []string{"install", "-h"}},
		{"skills uninstall", runSkills, []string{"uninstall", "-h"}},
	}
	for _, c := range cmds {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr := captureOutErr(t, func() { c.run(c.args) })
			for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
				// PrintDefaults flag lines: two spaces, the flag,
				// then at most a one-word value placeholder. For
				// one-character flags the usage text follows on the
				// SAME line after a tab, so only the pre-tab part is
				// the flag+placeholder. Description continuations
				// are tab-indented and don't match the prefix.
				if !strings.HasPrefix(line, "  -") {
					continue
				}
				head, _, _ := strings.Cut(line, "\t")
				if fields := strings.Fields(head); len(fields) > 2 {
					t.Errorf("%s -h renders a multiword value placeholder (backquoted phrase in the usage string?): %q", c.name, line)
				}
			}
		})
	}
}
