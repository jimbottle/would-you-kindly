package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
)

// agentNudgeHookCmd is the command `wyk hook install-nudge` registers as a
// Claude Code Stop hook (and the runtime handler dispatched by `wyk hook`).
const agentNudgeHookCmd = "wyk hook agent-nudge"

// nudgeStateMaxAge bounds how long a per-session dedup file lingers before
// the next nudge prunes it. Sessions are short-lived, so two weeks is far
// past any live session while keeping the scratch dir from growing forever.
const nudgeStateMaxAge = 14 * 24 * time.Hour

// nudgeFetchInbox returns the current agent inbox across every registered
// workspace — the same set `wyk inbox` shows. It's a swappable seam (like
// the beads runner / probeBDFunc patterns elsewhere) so the
// block→dedup→allow state machine in runHookAgentNudge can be unit-tested
// without a real bd binary. An empty registry / no workspace is an error,
// which the caller treats as "nothing to nudge about" (fail open).
var nudgeFetchInbox = func() ([]beads.Issue, error) {
	subs, code := inboxSubs("", "")
	if code != 0 {
		return nil, fmt.Errorf("no bd workspace")
	}
	all, _ := fetchInbox(subs, inboxQuery) // partial results are fine; ignore sub-errors
	return all, nil
}

// runHookInstallNudge installs (or removes) the agent-nudge Stop hook in a
// Claude Code settings.json. Defaults to the USER settings
// (~/.claude/settings.json, honoring $CLAUDE_CONFIG_DIR) because the wyk
// inbox is registry-wide — one nudge config covers every repo; -project
// targets ./.claude/settings.json instead. Idempotent in both directions.
func runHookInstallNudge(args []string) int {
	fs := flag.NewFlagSet("hook install-nudge", flag.ContinueOnError)
	fs.Usage = subcommandUsage(fs, "hook install-nudge")
	project := fs.Bool("project", false, "install into ./.claude/settings.json instead of the user settings")
	uninstall := fs.Bool("uninstall", false, "remove the agent-nudge Stop hook instead of installing it")
	dryRun := fs.Bool("dry-run", false, "print what would change without writing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}

	path, err := nudgeSettingsPath(*project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk hook install-nudge:", err)
		return 1
	}
	root, err := loadClaudeSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk hook install-nudge:", err)
		return 1
	}
	has := settingsHasHookForEvent(root, "Stop", agentNudgeHookCmd)

	if *uninstall {
		if !has {
			fmt.Println("agent-nudge Stop hook not present in " + path)
			return 0
		}
		if *dryRun {
			fmt.Println("would remove the agent-nudge Stop hook from " + path)
			return 0
		}
		removeHookForEvent(root, "Stop", agentNudgeHookCmd)
		if err := writeClaudeSettings(path, root); err != nil {
			fmt.Fprintln(os.Stderr, "wyk hook install-nudge:", err)
			return 1
		}
		fmt.Println("removed the agent-nudge Stop hook from " + path)
		return 0
	}

	if has {
		fmt.Println("agent-nudge Stop hook already in " + path)
		return 0
	}
	if *dryRun {
		fmt.Println("would add the agent-nudge Stop hook to " + path)
		return 0
	}
	addHookForEvent(root, "Stop", "", agentNudgeHookCmd)
	if err := writeClaudeSettings(path, root); err != nil {
		fmt.Fprintln(os.Stderr, "wyk hook install-nudge:", err)
		return 1
	}
	fmt.Println("added the agent-nudge Stop hook to " + path +
		"\n  → restart the Claude Code session and approve it via /hooks for it to fire")
	return 0
}

// nudgeSettingsPath resolves the settings.json the nudge hook is written
// into: the per-project file under cwd with -project, else the user file.
func nudgeSettingsPath(project bool) (string, error) {
	if project {
		return filepath.Join(".claude", "settings.json"), nil
	}
	return userSettingsPath()
}

// userSettingsPath resolves ~/.claude/settings.json, honoring
// $CLAUDE_CONFIG_DIR (the same override Claude Code and userSkillsDir use).
func userSettingsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// loadClaudeSettings parses a settings.json into a generic map, treating a
// missing file as an empty object so callers can add to it unconditionally.
func loadClaudeSettings(path string) (map[string]any, error) {
	root := map[string]any{}
	switch b, err := os.ReadFile(path); {
	case errors.Is(err, os.ErrNotExist):
		// fresh file
	case err != nil:
		return nil, err
	default:
		if uerr := json.Unmarshal(b, &root); uerr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, uerr)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	return root, nil
}

// writeClaudeSettings marshals root and writes it atomically (creating the
// .claude dir if needed, via writeFileAtomic).
func writeClaudeSettings(path string, root map[string]any) error {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeFileAtomic(path, out, 0o644)
}

// removeHookForEvent drops every hook whose command equals cmd from the
// given event, pruning entries (and the event itself) left empty. Reports
// whether anything was removed.
func removeHookForEvent(root map[string]any, event, cmd string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	entries, _ := hooks[event].([]any)
	var kept []any
	removed := false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		var keptInner []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == cmd {
				removed = true
				continue
			}
			keptInner = append(keptInner, h)
		}
		if len(keptInner) == 0 {
			continue // entry had only our hook; drop it
		}
		entry["hooks"] = keptInner
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	return removed
}

// runHookAgentNudge is the Claude Code Stop hook `wyk` can install
// (opt-in). When the agent finishes a turn it checks the agent inbox —
// the same `src:agent`-and-bounced-back set `wyk inbox` shows — and, if
// items have appeared that it hasn't surfaced before THIS session, blocks
// the stop and tells the agent to work them. That closes the handoff
// round-trip without the human having to re-prompt: the human flips the
// `human` label off, and the next time the agent settles it's told.
//
// Two guards keep it from nagging:
//   - per-session dedup: an inbox ID is surfaced at most once per session
//     (state in XDG_STATE_HOME/wyk/agent-nudge/<session>.json), so a
//     standing inbox doesn't re-fire every turn — it nudges on CHANGE.
//   - stop_hook_active: Claude sets this when it's already continuing
//     because of a Stop hook; we never block in that state, so the nudge
//     can't spin the session in a loop.
//
// It always fails OPEN (exit 0, no output → stop allowed): a hook that
// wedged the agent on a bd hiccup would be worse than a missed nudge.
func runHookAgentNudge(stdin io.Reader) int {
	var in struct {
		SessionID      string `json:"session_id"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0 // unparseable hook payload → allow stop
	}
	if in.StopHookActive {
		return 0 // already continuing from a stop hook; don't loop
	}

	all, err := nudgeFetchInbox()
	if err != nil {
		return 0 // no registry / no workspace / bd error → nothing to nudge about
	}
	// Identity-aware, mirroring `wyk inbox`'s default (collective query,
	// then narrow to this identity's routed + un-routed work). A malformed
	// $WYK_AGENT_IDENTITY just falls back to the collective inbox here —
	// the nudge is advisory, not a place to hard-error.
	if ident, _ := resolveIdentity(""); ident != "" {
		all = filterToIdentity(all, ident)
	}
	if len(all) == 0 {
		return 0
	}

	statePath := agentNudgeStatePath(in.SessionID)
	surfaced := loadSurfaced(statePath)

	fresh := freshInboxIssues(all, surfaced)
	if len(fresh) == 0 {
		return 0 // everything currently in the inbox was already surfaced
	}

	// Record the whole current inbox (not just the fresh slice) so an item
	// that's still pending next turn isn't surfaced again.
	for _, is := range all {
		surfaced[is.ID] = true
	}
	saveSurfaced(statePath, surfaced)

	out, err := json.Marshal(stopHookDecision{
		Decision: "block",
		Reason:   buildNudgeReason(fresh),
	})
	if err != nil {
		return 0
	}
	fmt.Fprintln(os.Stdout, string(out))
	return 0
}

// stopHookDecision is the Claude Code Stop-hook output that blocks the
// stop and hands `reason` back to the model as its next instruction.
type stopHookDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// freshInboxIssues returns the inbox issues whose IDs aren't in surfaced,
// preserving fetchInbox's order. Pure; the dedup core is unit-tested.
func freshInboxIssues(all []beads.Issue, surfaced map[string]bool) []beads.Issue {
	var out []beads.Issue
	for _, is := range all {
		if !surfaced[is.ID] {
			out = append(out, is)
		}
	}
	return out
}

// buildNudgeReason renders the message handed back to the agent. Titles
// are untrusted bd content, so they're stripped of terminal escapes
// before they reach the transcript.
func buildNudgeReason(issues []beads.Issue) string {
	var b strings.Builder
	noun := "issue"
	if len(issues) != 1 {
		noun = "issues"
	}
	fmt.Fprintf(&b, "%d %s just landed in your wyk inbox — a human bounced work back to you:\n", len(issues), noun)
	for _, is := range issues {
		fmt.Fprintf(&b, "  - %s [P%d] %s\n", is.ID, is.Priority, sanitize.Inline(is.Title))
	}
	b.WriteString("The default move is to WORK them now — run `wyk inbox` for the full runbook on each. ")
	b.WriteString("If you genuinely can't act on one yet, re-flag it for a human with a note (`wyk handoff <id>`) rather than dropping it.")
	return b.String()
}

// agentNudgeStatePath returns the per-session dedup file. It lives under
// XDG_STATE_HOME (falling back to ~/.local/state, then a temp dir) since
// it's regenerable scratch state, not config. The session id is reduced
// to a filesystem-safe slug so a hostile/odd id can't escape the dir.
func agentNudgeStatePath(sessionID string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "wyk", "agent-nudge", slugifySession(sessionID)+".json")
}

// slugifySession keeps only filename-safe runes so the session id can't
// contain a path separator or traversal. Empty/all-stripped ids collapse
// to a shared "session" file (dedup still works within that run).
func slugifySession(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

// loadSurfaced reads the set of inbox IDs already surfaced this session.
// A missing or unreadable file is an empty set — the nudge then treats
// every current inbox item as fresh, which is the safe direction (surface
// rather than silently swallow).
func loadSurfaced(path string) map[string]bool {
	surfaced := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return surfaced
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil {
		return surfaced
	}
	for _, id := range ids {
		surfaced[id] = true
	}
	return surfaced
}

// saveSurfaced persists the surfaced-ID set (sorted, for stable files).
// Best-effort: a write failure just means the next turn re-surfaces the
// same items — annoying, never harmful — so errors are swallowed.
func saveSurfaced(path string, surfaced map[string]bool) {
	ids := make([]string, 0, len(surfaced))
	for id := range surfaced {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data, err := json.Marshal(ids)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
	pruneOldNudgeState(dir)
}

// pruneOldNudgeState removes dedup files in dir older than nudgeStateMaxAge,
// so the scratch dir doesn't accumulate one file per session forever. It's
// opportunistic (run after each write) and best-effort — any error just
// leaves a stale file for a later pass. The just-written current-session
// file has a fresh mtime, so it's never the one pruned.
func pruneOldNudgeState(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-nudgeStateMaxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
