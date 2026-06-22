package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestFreshInboxIssues(t *testing.T) {
	all := []beads.Issue{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	surfaced := map[string]bool{"b": true}
	got := freshInboxIssues(all, surfaced)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("fresh = %+v, want [a c] in order", got)
	}
	// Nothing fresh when all are surfaced.
	if f := freshInboxIssues(all, map[string]bool{"a": true, "b": true, "c": true}); len(f) != 0 {
		t.Errorf("expected no fresh issues; got %+v", f)
	}
}

func TestBuildNudgeReason(t *testing.T) {
	reason := buildNudgeReason([]beads.Issue{
		{ID: "wyk-1", Priority: 1, Title: "Rotate the DB password"},
		{ID: "wyk-2", Priority: 3, Title: "Approve the release"},
	})
	for _, want := range []string{"2 issues", "wyk-1", "[P1]", "Rotate the DB password", "wyk-2", "[P3]", "wyk inbox", "wyk handoff"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason missing %q\n--- reason ---\n%s", want, reason)
		}
	}

	// Singular noun for one item.
	if r := buildNudgeReason([]beads.Issue{{ID: "x", Title: "t"}}); !strings.Contains(r, "1 issue just landed") {
		t.Errorf("expected singular phrasing; got %q", r)
	}

	// Untrusted titles are stripped of terminal escapes before they reach
	// the transcript.
	r := buildNudgeReason([]beads.Issue{{ID: "x", Title: "evil\x1b[31mred\x1b[0m\x07"}})
	if strings.ContainsRune(r, '\x1b') || strings.ContainsRune(r, '\x07') {
		t.Errorf("escape sequences not sanitized from title: %q", r)
	}
}

func TestSlugifySession(t *testing.T) {
	cases := map[string]string{
		"f9d260f7-320a-4b04": "f9d260f7-320a-4b04",
		"../../etc/passwd":   "etcpasswd",
		"a/b\\c":             "abc",
		"":                   "session",
		"!!!":                "session",
	}
	for in, want := range cases {
		if got := slugifySession(in); got != want {
			t.Errorf("slugifySession(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSurfacedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	saveSurfaced(path, map[string]bool{"b": true, "a": true})
	got := loadSurfaced(path)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("round-trip = %+v, want {a,b}", got)
	}
	// On-disk form is a sorted JSON array (stable diffs).
	data, _ := os.ReadFile(path)
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("state file is not a JSON array: %v (%s)", err, data)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("persisted ids = %v, want [a b]", ids)
	}
	// A missing file is an empty set, not an error.
	if m := loadSurfaced(filepath.Join(t.TempDir(), "absent.json")); len(m) != 0 {
		t.Errorf("missing file should load empty; got %+v", m)
	}
}

func TestRunHookAgentNudge_EarlyReturnsAllowStop(t *testing.T) {
	// stop_hook_active and unparseable payloads must allow the stop
	// (exit 0, no stdout) WITHOUT touching bd — so these run with no
	// registry/workspace and still must not block.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty registry isolation
	cases := map[string]string{
		"stop_hook_active short-circuits": `{"session_id":"s1","stop_hook_active":true}`,
		"unparseable payload":             `{not json`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if code := runHookAgentNudge(strings.NewReader(payload)); code != 0 {
					t.Errorf("exit %d, want 0", code)
				}
			})
			if strings.TrimSpace(out) != "" {
				t.Errorf("expected no stdout (stop allowed); got %q", out)
			}
		})
	}
}

func TestRunHookInstallNudge(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	path := filepath.Join(cfg, "settings.json")

	// Pre-seed an unrelated PreToolUse hook to prove preservation.
	seed := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"wyk hook bd-create-guard"}]}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	install := func(args ...string) int {
		var code int
		captureStdout(t, func() { code = runHookInstallNudge(args) })
		return code
	}

	// Install: adds the Stop hook, leaves the PreToolUse hook alone.
	if code := install(); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	root := readSettingsFile(t, path)
	if !settingsHasHookForEvent(root, "Stop", agentNudgeHookCmd) {
		t.Error("Stop hook not installed")
	}
	if !settingsHasHookForEvent(root, "PreToolUse", "wyk hook bd-create-guard") {
		t.Error("pre-existing PreToolUse hook was clobbered")
	}

	// Idempotent: a second install must not duplicate the entry.
	if code := install(); code != 0 {
		t.Fatalf("second install exit %d", code)
	}
	if n := countEventHooks(readSettingsFile(t, path), "Stop", agentNudgeHookCmd); n != 1 {
		t.Errorf("install not idempotent: %d Stop entries, want 1", n)
	}

	// Dry-run mutates nothing.
	before, _ := os.ReadFile(path)
	if code := install("-uninstall", "-dry-run"); code != 0 {
		t.Fatalf("dry-run exit %d", code)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("dry-run mutated settings.json")
	}

	// Uninstall removes the Stop hook but preserves the PreToolUse hook.
	if code := install("-uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d", code)
	}
	root = readSettingsFile(t, path)
	if settingsHasHookForEvent(root, "Stop", agentNudgeHookCmd) {
		t.Error("Stop hook not removed")
	}
	if !settingsHasHookForEvent(root, "PreToolUse", "wyk hook bd-create-guard") {
		t.Error("uninstall clobbered the PreToolUse hook")
	}

	// Uninstall again is a no-op success.
	if code := install("-uninstall"); code != 0 {
		t.Errorf("second uninstall exit %d, want 0", code)
	}
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse settings: %v (%s)", err, b)
	}
	return root
}

func countEventHooks(root map[string]any, event, cmd string) int {
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	n := 0
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == cmd {
				n++
			}
		}
	}
	return n
}
