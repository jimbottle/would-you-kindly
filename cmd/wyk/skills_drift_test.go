package main

import (
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
	for _, s := range all {
		seen := false
		for _, m := range wykCmdRef.FindAllStringSubmatch(s.Content, -1) {
			cmd := m[1]
			seen = true
			if !valid[cmd] {
				t.Errorf("skill %q references `wyk %s`, which is not a shipped wyk subcommand "+
					"(see wykSubcommands in completion.go) — fix the skill or add the command", s.Name, cmd)
			}
		}
		_ = seen // a skill needn't reference any command; we only police the ones it does
	}
}

// TestWykCmdRef_MatchesCommandsNotProse proves the matcher underlying
// the drift guard extracts real command tokens and a fake one — and
// leaves skill names and capitalised prose alone — so a passing
// ReferenceOnlyRealWykSubcommands means something.
func TestWykCmdRef_MatchesCommandsNotProse(t *testing.T) {
	body := "Run `wyk inbox -json` then `wyk frobnicate`. The wyk-handoff " +
		"skill drives the wyk TUI."
	var got []string
	for _, m := range wykCmdRef.FindAllStringSubmatch(body, -1) {
		got = append(got, m[1])
	}
	// "inbox" and "frobnicate" match; "wyk-handoff" (hyphen), "wyk TUI"
	// (uppercase), and bare "wyk" (no following lowercase word) don't.
	want := map[string]bool{"inbox": true, "frobnicate": true}
	if len(got) != len(want) {
		t.Fatalf("matched %v, want exactly %v", got, []string{"inbox", "frobnicate"})
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected match %q", c)
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
