// Package skills holds the canonical, binary-embedded Claude Code
// skills wyk ships for agents working in a bd/wyk project. The source
// of truth is internal/skills/data/<name>/SKILL.md; embedding it means
// `wyk update` carries new skill versions and `wyk skills install`
// (the CLI surface) always writes the version that matches the running
// binary — no separate file to keep in sync.
//
// The skills are deliberately THIN: their bodies call the wyk/bd CLI
// (`wyk inbox`, `wyk conventions`, `wyk handoff`, `wyk depgraph`)
// rather than restating conventions, so the binary stays the single
// source of truth and the skill text can't drift from it.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed data
var data embed.FS

// Skill is one wyk-provided skill: its name (which matches both the
// data/<name> directory and the SKILL.md frontmatter `name:`), the
// frontmatter description (the trigger Claude Code matches on), and the
// full SKILL.md content to write on install.
type Skill struct {
	Name        string
	Description string
	Content     string
}

// All returns every embedded skill, ordered by name. Errors only on a
// malformed embed (a SKILL.md with broken frontmatter), which a test
// guards against — so production callers can treat a non-nil error as
// a build-time bug.
func All() ([]Skill, error) {
	entries, err := fs.ReadDir(data, "data")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		b, err := data.ReadFile("data/" + name + "/SKILL.md")
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", name, err)
		}
		content := string(b)
		desc, err := FrontmatterField(content, "description")
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", name, err)
		}
		out = append(out, Skill{Name: name, Description: desc, Content: content})
	}
	return out, nil // fs.ReadDir already returns entries sorted by name
}

// FrontmatterField extracts a top-level `key: value` scalar from a
// SKILL.md's leading `---` YAML frontmatter. The skill frontmatter is
// intentionally simple (single-line name/description), so a tiny
// hand-parser avoids a YAML dependency. Returns an error when the
// frontmatter is missing/unterminated or the key isn't present.
func FrontmatterField(content, key string) (string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return "", fmt.Errorf("missing `---` frontmatter")
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", fmt.Errorf("unterminated frontmatter")
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("frontmatter key %q not found", key)
}
