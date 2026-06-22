package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/sanitize"
)

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

	// Identity-aware, mirroring `wyk inbox`'s default (collective query,
	// then narrow to this identity's routed + un-routed work). A malformed
	// $WYK_AGENT_IDENTITY just falls back to the collective inbox here —
	// the nudge is advisory, not a place to hard-error.
	ident, _ := resolveIdentity("")

	subs, code := inboxSubs("", "")
	if code != 0 {
		return 0 // no registry / no workspace → nothing to nudge about
	}
	all, _ := fetchInbox(subs, inboxQuery) // partial results are fine; ignore sub-errors
	if ident != "" {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
