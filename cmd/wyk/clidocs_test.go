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
	// (would-you-kindly-k3fb, roborev #2041). Sweep every
	// FlagSet-backed subcommand's -h output and reject any
	// flag-definition line with more than two fields, so the bug
	// class can't quietly return on any flag.
	cmds := map[string]func([]string) int{
		"handoff":   runHandoff,
		"inbox":     runInbox,
		"init":      runInit,
		"depgraph":  runDepgraph,
		"update":    runUpdate,
		"export":    runExport,
		"import":    runImport,
		"activity":  runActivity,
		"stats":     runStats,
		"dashboard": runDashboard,
		"doctor":    runDoctor,
	}
	for name, run := range cmds {
		t.Run(name, func(t *testing.T) {
			_, stderr := captureOutErr(t, func() { run([]string{"-h"}) })
			for _, line := range strings.Split(stderr, "\n") {
				// PrintDefaults flag lines: two spaces, the flag,
				// then at most a one-word value placeholder.
				// Description continuations are tab-indented and
				// don't match the prefix.
				if !strings.HasPrefix(line, "  -") {
					continue
				}
				// One-character flags carry their usage on the SAME
				// line after a tab; only the pre-tab part is the
				// flag + placeholder.
				head, _, _ := strings.Cut(line, "\t")
				if fields := strings.Fields(head); len(fields) > 2 {
					t.Errorf("%s -h renders a multiword value placeholder (backquoted phrase in the usage string?): %q", name, line)
				}
			}
		})
	}
}
