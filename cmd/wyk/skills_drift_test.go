package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/skills"
)

// wykCmdRef matches a "wyk <subcommand>" invocation in a skill body:
// the literal "wyk", a single space, then a lowercase command token.
//
// The shape is deliberately strict so it catches commands without
// snagging prose:
//   - requiring a SPACE (not a hyphen) means the skill *names*
//     ("wyk-handoff", "wyk-project-review") don't match;
//   - requiring a LOWERCASE first letter means capitalised prose
//     ("wyk TUI", "wyk CLI", "the wyk binary") is left alone — only an
//     actual lowercase command token matches.
//
// The contract this enforces: any "wyk <lowercase-word>" written in a
// skill must be a real shipped subcommand. That's the right rule for
// agent-facing instructions — they should never tell an agent to run a
// command that doesn't exist. (It does mean a skill author must avoid
// bare lowercase prose right after "wyk " — write "the wyk CLI", not
// "wyk and bd" — but the skills are short, command-dense, and already
// follow that.)
var wykCmdRef = regexp.MustCompile(`\bwyk ([a-z][a-z-]*)`)

// TestSkills_ReferenceOnlyRealWykSubcommands is the drift guard: it
// fails if any embedded skill names a `wyk` subcommand that isn't in
// the shipped dispatch table. Without it a skill could rot silently —
// e.g. a renamed/removed command would keep being advertised to agents
// until someone noticed at runtime.
func TestSkills_ReferenceOnlyRealWykSubcommands(t *testing.T) {
	valid := make(map[string]bool, len(wykSubcommands))
	for _, c := range wykSubcommands {
		valid[c] = true
	}
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no skills embedded")
	}
	// A skill needn't reference any command; we only police the ones it
	// does, so there's no "references at least one command" assertion.
	for _, s := range all {
		for _, m := range wykCmdRef.FindAllStringSubmatch(s.Content, -1) {
			cmd := m[1]
			if !valid[cmd] {
				t.Errorf("skill %q references `wyk %s`, which is not a shipped wyk subcommand "+
					"(see wykSubcommands in completion.go) — fix the skill or add the command", s.Name, cmd)
			}
		}
	}
}

// TestWykCmdRef_MatchesCommandsNotProse proves the matcher underlying
// the drift guard extracts real command tokens and a fake one — and
// leaves skill names and capitalised prose alone — so a passing
// ReferenceOnlyRealWykSubcommands means something.
func TestWykCmdRef_MatchesCommandsNotProse(t *testing.T) {
	body := "Run `wyk inbox -json` then `wyk frobnicate`. The wyk-handoff " +
		"skill drives the wyk TUI."
	got := map[string]int{}
	for _, m := range wykCmdRef.FindAllStringSubmatch(body, -1) {
		got[m[1]]++
	}
	// "inbox" and "frobnicate" match exactly once each; "wyk-handoff"
	// (hyphen), "wyk TUI" (uppercase), and bare "wyk" (no following
	// lowercase word) don't. Compare counts, not just the set, so a
	// regression that double-matched a token (e.g. {"inbox":2}) fails.
	want := map[string]int{"inbox": 1, "frobnicate": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matched %v, want exactly %v", got, want)
	}
}

// TestPluginSkillsMatchEmbedded guards the Claude Code plugin under
// plugin/ against drift. The plugin ships real SKILL.md files (a
// marketplace install can't read them out of the wyk binary), so they're
// committed copies of the embedded source — `make plugin-skills`
// regenerates them. This test fails if a copy falls out of sync, so the
// plugin can never quietly ship a stale skill.
func TestPluginSkillsMatchEmbedded(t *testing.T) {
	// Test CWD is the package dir (cmd/wyk); the plugin is two levels up.
	const pluginSkills = "../../plugin/skills"
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		got, err := os.ReadFile(filepath.Join(pluginSkills, s.Name, "SKILL.md"))
		if err != nil {
			t.Errorf("plugin is missing skill %q (run `make plugin-skills`): %v", s.Name, err)
			continue
		}
		if string(got) != s.Content {
			t.Errorf("plugin skill %q has drifted from the embedded copy — run `make plugin-skills` and commit", s.Name)
		}
	}
	// And no stray skills the binary doesn't know about.
	embedded := make(map[string]bool, len(all))
	for _, s := range all {
		embedded[s.Name] = true
	}
	entries, err := os.ReadDir(pluginSkills)
	if err != nil {
		t.Fatalf("reading %s: %v", pluginSkills, err)
	}
	for _, e := range entries {
		if e.IsDir() && !embedded[e.Name()] {
			t.Errorf("plugin bundles skill %q that isn't embedded in the binary — remove it or add the source", e.Name())
		}
	}
}

// TestSkills_FrontmatterNameMatchesDir guards the other half of skill
// integrity: skills.All() takes a skill's Name from its embed directory
// but the frontmatter carries its own `name:` (the identifier Claude
// Code keys on). If the two drift apart, the skill installs under one
// name and self-identifies as another — confusing at best. Keep them
// equal.
func TestSkills_FrontmatterNameMatchesDir(t *testing.T) {
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		fmName, err := skills.FrontmatterField(s.Content, "name")
		if err != nil {
			t.Errorf("skill %q: reading frontmatter name: %v", s.Name, err)
			continue
		}
		if fmName != s.Name {
			t.Errorf("skill in dir %q has frontmatter name %q — they must match", s.Name, fmName)
		}
	}
}
