package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sweptFlagSets drives TestSubcommandHelp_NoMultiwordFlagPlaceholders
// and is itself guarded by TestSweepCoversEveryFlagSet: the table must
// account for every flag.NewFlagSet call site in this package, so a
// new subcommand cannot ship unswept.
//
// Surfaces with NO flag.NewFlagSet of their own (and therefore not
// listed): `create` forwards -h verbatim to a real bd binary, the
// top-level TUI flags live on flag.CommandLine (whose -h path
// os.Exits via printTopLevelUsage), and `completion` parses args by
// hand.
var sweptFlagSets = []struct {
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

func TestSubcommandHelp_NoMultiwordFlagPlaceholders(t *testing.T) {
	// Go's flag package takes the FIRST backquoted token in a usage
	// string as the flag's value placeholder — for every flag kind,
	// bools included. A multiword backquoted phrase ('wyk inbox',
	// 'dot -Tsvg') therefore renders as a nonsense value name:
	// "-identity wyk inbox", "-template wyk handoff <id> < filled.md"
	// (would-you-kindly-k3fb, roborev #2041/#2043). Sweep every
	// FlagSet-backed subcommand's -h output — including the nested
	// registry/skills/hook dispatchers — and reject any
	// flag-definition line with a multiword placeholder.
	for _, c := range sweptFlagSets {
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

func TestSweepCoversEveryFlagSet(t *testing.T) {
	// Self-enforcement for the sweep above: count flag.NewFlagSet call
	// sites in the package source and require the table to match, so a
	// 21st FlagSet fails here until it's added to sweptFlagSets (or
	// deliberately documented) instead of rotting unswept — the same
	// drift mode that previously left the sweep at 11/20
	// (roborev #2044). Tests run with the package dir as CWD.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sites += strings.Count(string(b), "flag.NewFlagSet(")
	}
	if sites != len(sweptFlagSets) {
		t.Errorf("package has %d flag.NewFlagSet call sites but sweptFlagSets lists %d — add the new FlagSet's -h invocation to the sweep (or document a deliberate exclusion here)",
			sites, len(sweptFlagSets))
	}
}
