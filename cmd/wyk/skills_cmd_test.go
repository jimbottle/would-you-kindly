package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jimbottle/would-you-kindly/internal/skills"
)

func firstSkill(t *testing.T) skills.Skill {
	t.Helper()
	all, err := skills.All()
	if err != nil || len(all) == 0 {
		t.Fatalf("skills.All: %v (n=%d)", err, len(all))
	}
	return all[0]
}

func TestSkillStateAt_MissingCurrentModified(t *testing.T) {
	dir := t.TempDir()
	s := firstSkill(t)

	if st, _ := skillStateAt(s, dir); st != skillMissing {
		t.Errorf("fresh dir: state = %v, want missing", st)
	}
	if err := writeSkillFile(s, dir); err != nil {
		t.Fatal(err)
	}
	if st, _ := skillStateAt(s, dir); st != skillCurrent {
		t.Errorf("after write: state = %v, want current", st)
	}
	// Tamper with it.
	if err := os.WriteFile(filepath.Join(dir, s.Name, "SKILL.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := skillStateAt(s, dir); st != skillModified {
		t.Errorf("after edit: state = %v, want modified", st)
	}
}

// withTempHome points the user skills target at a temp dir.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "") // force the HOME fallback
	t.Setenv("HOME", home)
	return filepath.Join(home, ".claude", "skills")
}

func TestRunSkillsInstall_WritesAndIsIdempotent(t *testing.T) {
	dir := withTempHome(t)
	all, _ := skills.All()

	if code := runSkillsInstall([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("install exit = %d", code)
	}
	for _, s := range all {
		if _, err := os.Stat(filepath.Join(dir, s.Name, "SKILL.md")); err != nil {
			t.Errorf("skill %s not written: %v", s.Name, err)
		}
	}
	// Re-install: everything current → still exit 0, files unchanged.
	if code := runSkillsInstall([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Errorf("re-install exit = %d", code)
	}
	for _, s := range all {
		if st, _ := skillStateAt(s, dir); st != skillCurrent {
			t.Errorf("skill %s state after re-install = %v, want current", s.Name, st)
		}
	}
}

func TestRunSkillsInstall_DryRunWritesNothing(t *testing.T) {
	dir := withTempHome(t)
	if code := runSkillsInstall([]string{"-dry-run", "-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry-run should not create %s (err=%v)", dir, err)
	}
}

func TestRunSkillsInstall_ModifiedNeedsForce(t *testing.T) {
	dir := withTempHome(t)
	runSkillsInstall([]string{"-y"}, strings.NewReader(""))
	s := firstSkill(t)
	p := filepath.Join(dir, s.Name, "SKILL.md")
	if err := os.WriteFile(p, []byte("hand edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without -force: the modified skill is left alone.
	runSkillsInstall([]string{"-y"}, strings.NewReader(""))
	if b, _ := os.ReadFile(p); string(b) != "hand edit" {
		t.Errorf("install without -force overwrote a modified skill")
	}
	// With -force: it's restored to the embedded content.
	runSkillsInstall([]string{"-y", "-force"}, strings.NewReader(""))
	if b, _ := os.ReadFile(p); string(b) != s.Content {
		t.Errorf("install -force should restore the embedded content")
	}
}

func TestRunSkillsUninstall_RemovesOurSkills(t *testing.T) {
	dir := withTempHome(t)
	runSkillsInstall([]string{"-y"}, strings.NewReader(""))
	if code := runSkillsUninstall([]string{"-y"}, strings.NewReader("")); code != 0 {
		t.Fatalf("uninstall exit = %d", code)
	}
	all, _ := skills.All()
	for _, s := range all {
		if _, err := os.Stat(filepath.Join(dir, s.Name, "SKILL.md")); !os.IsNotExist(err) {
			t.Errorf("skill %s still present after uninstall (err=%v)", s.Name, err)
		}
	}
}

func TestRunSkillsPrint(t *testing.T) {
	out := captureStdout(t, func() {
		if code := runSkillsPrint([]string{"wyk-handoff"}); code != 0 {
			t.Errorf("print known skill exit = %d", code)
		}
	})
	if !strings.Contains(out, "name: wyk-handoff") {
		t.Errorf("print should emit the SKILL.md; got %q", out[:min(80, len(out))])
	}
	if code := runSkillsPrint([]string{"nonexistent"}); code != 1 {
		t.Errorf("print unknown skill exit = %d, want 1", code)
	}
	if code := runSkillsPrint(nil); code != 64 {
		t.Errorf("print with no arg exit = %d, want 64", code)
	}
}

func TestTruncForList_RuneAware(t *testing.T) {
	// A long multi-byte string truncated at the boundary must stay
	// valid UTF-8 (no split rune).
	s := strings.Repeat("é", 150) // 2 bytes each → byte-slicing would split one
	got := truncForList(s)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected an ellipsis suffix; got %q", got[len(got)-4:])
	}
	// Short strings pass through unchanged.
	if truncForList("hi") != "hi" {
		t.Errorf("short string should be unchanged")
	}
}
