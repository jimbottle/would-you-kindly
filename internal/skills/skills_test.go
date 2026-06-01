package skills

import (
	"strings"
	"testing"
)

func TestAll_ShipsTheExpectedFamily(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Skill{}
	for _, s := range all {
		got[s.Name] = s
	}
	for _, name := range []string{"wyk", "wyk-handoff", "wyk-project-review"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing expected skill %q (have %v)", name, keys(got))
		}
	}
}

func TestAll_WellFormed(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no skills embedded")
	}
	for _, s := range all {
		// The frontmatter name must match the directory name so install
		// can't write a skill under a path that disagrees with its id.
		fmName, err := FrontmatterField(s.Content, "name")
		if err != nil {
			t.Errorf("%s: %v", s.Name, err)
			continue
		}
		if fmName != s.Name {
			t.Errorf("%s: frontmatter name %q != directory name", s.Name, fmName)
		}
		// The description is the trigger Claude Code matches on — it
		// must be present and non-trivial.
		if len(strings.TrimSpace(s.Description)) < 20 {
			t.Errorf("%s: description too short to be a useful trigger: %q", s.Name, s.Description)
		}
		// There must be a real body after the frontmatter.
		if i := strings.Index(s.Content, "\n---"); i < 0 || len(strings.TrimSpace(s.Content[i+4:])) < 100 {
			t.Errorf("%s: SKILL.md body is missing or too short", s.Name)
		}
	}
}

func TestFrontmatterField(t *testing.T) {
	const doc = "---\nname: foo\ndescription: a thing\n---\n\n# body\n"
	if v, err := FrontmatterField(doc, "name"); err != nil || v != "foo" {
		t.Errorf("name: got %q, %v", v, err)
	}
	if v, err := FrontmatterField(doc, "description"); err != nil || v != "a thing" {
		t.Errorf("description: got %q, %v", v, err)
	}
	if _, err := FrontmatterField(doc, "missing"); err == nil {
		t.Error("expected error for a missing key")
	}
	if _, err := FrontmatterField("no frontmatter here", "name"); err == nil {
		t.Error("expected error for missing frontmatter")
	}
}

func keys(m map[string]Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
