package main

import (
	"os"
	"path/filepath"
	"regexp"
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
	{"bugreport", runBugreport, []string{"-h"}},
	{"conventions", runConventions, []string{"-h"}},
	{"version", runVersion, []string{"-h"}},
	{"help", runHelp, []string{"-h"}},
	{"hook post-commit", runHook, []string{"post-commit", "-h"}},
	{"hook install-nudge", runHook, []string{"install-nudge", "-h"}},
	{"registry add", runRegistry, []string{"add", "-h"}},
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
	// Self-enforcement for the sweep above: extract the NAME of every
	// flag.NewFlagSet("...") call site in the package source and
	// require the set to equal the sweep table's names, so a new
	// FlagSet fails here until it's added to sweptFlagSets — the
	// drift mode that previously left the sweep at 11/20 (roborev
	// #2044). Identity matching (not a bare count) so an add+remove
	// in one change can't cancel out, and the string-literal anchor
	// keeps a mention in a comment from miscounting (roborev #2045).
	// Literal names only: a FlagSet named via a variable or
	// expression escapes this guard (roborev #2046) — the package
	// convention is uniformly literal names, so keep it that way.
	// Tests run with the package dir as CWD.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	siteRE := regexp.MustCompile(`flag\.NewFlagSet\("([^"]+)"`)
	inSource := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range siteRE.FindAllStringSubmatch(string(b), -1) {
			inSource[m[1]] = true
		}
	}
	inTable := map[string]bool{}
	for _, c := range sweptFlagSets {
		inTable[c.name] = true
	}
	for name := range inSource {
		if !inTable[name] {
			t.Errorf("FlagSet %q exists in the source but is not swept — add its -h invocation to sweptFlagSets (or document a deliberate exclusion)", name)
		}
	}
	for name := range inTable {
		if !inSource[name] {
			t.Errorf("sweptFlagSets lists %q but no flag.NewFlagSet(%q) exists — remove the stale entry", name, name)
		}
	}
}

func TestSubcommandHelp_LeadsWithSynopsis(t *testing.T) {
	// Every FlagSet-backed subcommand's -h gets the init -h treatment
	// (would-you-kindly-rnjg): a "wyk <name> — <summary>" lead and a
	// Usage block sourced from cliSubcommandDocs (so -h can't drift
	// from the generated cli.md). Commands without a doc entry (the
	// internal hook) fall back to a plain usage line — still no bare
	// "Usage of <name>:" flag dump anywhere.
	for _, c := range sweptFlagSets {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr := captureOutErr(t, func() { c.run(c.args) })
			out := stdout + stderr
			base, _, _ := strings.Cut(c.name, " ")
			if doc := findCLIDoc(c.name); doc != nil {
				if !strings.Contains(out, "wyk "+base+" — ") {
					t.Errorf("%s -h should lead with the summary line; got:\n%s", c.name, out)
				}
				// init keeps its richer hand-written layout (the
				// template this treatment copies) — "Common case:"
				// without a "Usage:" header is fine there.
				if !strings.Contains(out, "Usage:") && !strings.Contains(out, "Common case:") {
					t.Errorf("%s -h should include a Usage or Common case block; got:\n%s", c.name, out)
				}
				// The examples half of the single-source claim: when
				// the doc defines examples, -h must actually render
				// them — dropping the loop in subcommandUsage would
				// otherwise pass unnoticed (roborev #2060). init's
				// custom layout carries its own examples inline.
				if len(doc.Examples) > 0 && base != "init" {
					if !strings.Contains(out, "Common case:") || !strings.Contains(out, doc.Examples[0]) {
						t.Errorf("%s -h should render the doc's examples; got:\n%s", c.name, out)
					}
				}
			} else if !strings.Contains(out, "usage: wyk "+c.name) {
				t.Errorf("%s -h (no doc entry) should print the fallback usage line; got:\n%s", c.name, out)
			}
			if strings.Contains(out, "Usage of ") {
				t.Errorf("%s -h still shows Go's bare flag-dump header; got:\n%s", c.name, out)
			}
		})
	}
}

// flagNamesInHelp extracts the flag names PrintDefaults emitted in a
// subcommand's -h output. PrintDefaults writes one "  -name ..." line
// per registered flag, so the help text is a faithful, refactor-free
// view of the FlagSet's contents.
func flagNamesInHelp(help string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		// Flag lines start with exactly two spaces then a dash; the
		// wrapped description lines below them are indented further.
		if !strings.HasPrefix(line, "  -") || strings.HasPrefix(line, "   ") {
			continue
		}
		field := strings.Fields(strings.TrimSpace(line))[0]
		names[strings.TrimLeft(field, "-")] = true
	}
	return names
}

// TestCLIDocsFlagsMatchFlagSets is the table↔FlagSet parity guard.
//
// CI's docs-check proves the generated cli.md matches cliSubcommandDocs,
// but it is blind in the direction that actually bites: a flag
// registered on a FlagSet and never added to the table is invisible to
// it, which is exactly how `-all` shipped undocumented. This closes the
// loop from the other side, without the refactor the original note
// assumed — PrintDefaults already renders every registered flag, so the
// swept -h output IS the FlagSet's contents.
//
// Nested dispatchers (registry list, skills install, …) resolve to their
// parent's doc entry, whose flag list is the union across subforms, so
// the registered names are unioned per doc entry before comparing.
func TestCLIDocsFlagsMatchFlagSets(t *testing.T) {
	registered := map[string]map[string]bool{} // doc name -> flag names
	for _, c := range sweptFlagSets {
		doc := findCLIDoc(c.name)
		if doc == nil {
			continue // the internal `hook` dispatcher has no doc entry
		}
		stdout, stderr := captureOutErr(t, func() { c.run(c.args) })
		if registered[doc.Name] == nil {
			registered[doc.Name] = map[string]bool{}
		}
		for name := range flagNamesInHelp(stdout + "\n" + stderr) {
			registered[doc.Name][name] = true
		}
	}

	for docName, got := range registered {
		doc := findCLIDoc(docName)
		documented := map[string]bool{}
		for _, f := range doc.Flags {
			// Docs spell some flags with a double dash (--check); the
			// FlagSet prints one. Compare on the bare name.
			documented[strings.TrimLeft(f.Name, "-")] = true
		}
		for name := range got {
			if !documented[name] {
				t.Errorf("wyk %s: -%s is registered on the FlagSet but missing from its cliSubcommandDocs entry (so it never reaches `wyk help` or cli.md)", docName, name)
			}
		}
		for name := range documented {
			if !got[name] {
				t.Errorf("wyk %s: -%s is documented in cliSubcommandDocs but not registered on any of its FlagSets", docName, name)
			}
		}
	}
}
