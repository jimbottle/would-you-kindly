package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// Write paths: close/reopen, quick-add, note, label, assign,
// defer, edit, priority/type bumps, marks + bulk dispatch,
// undo, and the shared write plumbing (runWrite*, results).

// writeMsg carries the result of a Mutator call back to the model.
// `action` describes what was attempted (used to compose the status
// banner); `id` identifies the affected issue. `issue` snapshots
// the full row (filled by close so the undo-handler has the Repo
// to route reopen back through MultiBDSource without re-fetching
// the closed list).
type writeMsg struct {
	action string
	id     string
	issue  beads.Issue
	err    error
}

// mutator returns the Mutator interface if the configured Source
// also implements it. nil means we're in read-only mode and write
// keys should show a "read-only" hint instead of acting.
func (m Model) mutator() Mutator {
	mu, _ := m.src.(Mutator)
	return mu
}

// beginClose enters the confirm-close mode so a stray `a` doesn't
// destroy work. Confirmation is just the next keystroke: y proceeds,
// anything else cancels. The full issue (not just its ID) is captured
// so a concurrent refetch can't shift the cursor onto a different
// issue between the prompt opening and the user's confirmation, AND
// so a multi-repo Mutator can route on Repo even if the fetched list
// has moved on.
func (m Model) beginClose() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	// Refuse at the PROMPT, not at the confirm: opening a second
	// close prompt while one is in flight is how the user ends up
	// confirming a close they didn't mean (would-you-kindly-khtw).
	if m.refuseIfClosing() {
		return m, flashClearCmd(m.statusGen)
	}
	m.mode = modeConfirmClose
	// Bulk path: marks are the targets and the confirm prompt
	// counts them. Single path: snapshot the cursor row into
	// pendingTarget as before.
	if len(m.marked) == 0 {
		m.pendingTarget = m.visible[m.cursor]
	}
	// Modal entry adds 2 lines of chrome — re-clamp scroll so the
	// cursor stays in the now-smaller viewport.
	m.ensureCursorVisible()
	return m, nil
}

func (m Model) updateConfirmClose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	bulk := len(m.marked) > 0
	target := m.pendingTarget
	ret := m.promptReturn
	fromDetail := ret == modeDetail
	m.pendingTarget = beads.Issue{}
	m.promptReturn = modeList
	if msg.String() == "y" || msg.String() == "Y" {
		mu := m.mutator()
		// A successful close drops the issue out of the open view, so
		// we land back in the list regardless of where the prompt was
		// opened — there's nothing useful left to show in detail.
		m.mode = modeList
		// Backstop for the beginClose gate: a prompt opened before an
		// earlier close was dispatched could otherwise still confirm.
		if m.refuseIfClosing() {
			return m, flashClearCmd(m.statusGen)
		}
		if bulk {
			targets := m.markedIssues()
			m.marked = nil
			// Mark BEFORE dispatch so the rows read "closing" on the
			// very next paint rather than after bd returns.
			m.markClosing(targets...)
			m.setStatus(fmt.Sprintf("closing %d rows…", len(targets)))
			if bc, ok := mu.(BulkCloser); ok {
				return m, runBulkClose(targets, bc)
			}
			return m, runBulkWrite("close", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.Close(ctx, i)
			})
		}
		// The list path validates against m.all (the prompt may have
		// outlived a refetch that removed the row). The detail path
		// trusts its snapshot: a drilled-in link can legitimately be
		// absent from the filtered list, so issueExists would
		// false-negative — let bd surface a genuinely-gone ID instead.
		if !fromDetail && !m.issueExists(target.ID) {
			m.setStatus("close cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, nil
		}
		m.lastAction = repeatableAction{kind: "close"}
		// Immediate, row-specific feedback: the target renders as
		// "closing" and the banner names it, so the seconds bd spends
		// closing are legible instead of looking like a dropped
		// keypress (would-you-kindly-khtw).
		m.markClosing(target)
		m.setStatus("closing " + target.ID + "…")
		return m, runWriteWithIssue("close", target, func(ctx context.Context) error {
			return mu.Close(ctx, target)
		})
	}
	// any other key cancels — back to wherever the prompt was opened
	m.mode = ret
	m.setStatus("close cancelled")
	return m, nil
}

// closingLabel is what the Status cell reads for a row whose close is
// in flight. Deliberately a status-column word rather than a spinner:
// it names the row being acted on, which is the thing the user needs
// when they're about to press the key again.
const closingLabel = "closing"

// markClosing records the given issues as having an in-flight close.
func (m *Model) markClosing(issues ...beads.Issue) {
	if m.closing == nil {
		m.closing = make(map[string]bool, len(issues))
	}
	for _, i := range issues {
		m.closing[issueKey(i)] = true
	}
}

// isClosing reports whether this row has a close in flight.
func (m Model) isClosing(i beads.Issue) bool {
	return m.closing[issueKey(i)]
}

// closeBlockedBy returns the ID of an in-flight close and true when a
// NEW close must be refused. The user's rule: never let a second close
// start while the first one is still showing in the list, because the
// second one lands on whatever row the cursor has drifted to. Returns
// one representative ID (map order is arbitrary but a bulk close is
// reported by count at the call site, so any member reads correctly).
func (m Model) closeBlockedBy() (string, bool) {
	for key := range m.closing {
		// issueKey is `repo/id` in multi-repo mode; the ID half is
		// what the user sees in the ID column and what bd calls it.
		if _, id, ok := strings.Cut(key, "/"); ok {
			return id, true
		}
		return key, true
	}
	return "", false
}

// refuseIfClosing sets the "wait for it" banner and reports whether the
// caller must abort. Shared by every close dispatch site (the confirm
// prompt, the bulk path, and `.` repeat) so none can grow a hole.
func (m *Model) refuseIfClosing() bool {
	id, blocked := m.closeBlockedBy()
	if !blocked {
		return false
	}
	if n := len(m.closing); n > 1 {
		m.setStatus(fmt.Sprintf("closing %d rows — wait for them to clear before closing anything else", n))
	} else {
		m.setStatus("still closing " + id + " — wait for it to clear before closing anything else")
	}
	return true
}

// clearClosing drops the in-flight marks for the given issues (or all
// of them when none are named), lifting the close block.
func (m *Model) clearClosing(issues ...beads.Issue) {
	if len(issues) == 0 {
		m.closing = nil
		return
	}
	for _, i := range issues {
		delete(m.closing, issueKey(i))
	}
	if len(m.closing) == 0 {
		m.closing = nil
	}
}

// issueExists reports whether the given ID is still present in the
// model's last fetched set (m.all, not the post-filter m.visible).
// A fuzzy filter that hides an issue does NOT count as "gone" — the
// user already confirmed the action against a known ID. Used by the
// prompt handlers to detect a refetch that genuinely removed the
// originally-targeted issue.
func (m Model) issueExists(id string) bool {
	for _, i := range m.all {
		if i.ID == id {
			return true
		}
	}
	return false
}

// toggleHuman flips the `human` label on the cursor issue. No
// confirmation — the operation is reversible by toggling again.
// Bulk path: when marks are present, ADDS the human label to every
// marked row that doesn't already have it (the most common triage
// flow — "flag these five for review"). Toggle-per-row would be
// inconsistent across mixed-state selections.
func (m Model) toggleHuman() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	mu := m.mutator()
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("flag", targets, func(ctx context.Context, i beads.Issue) error {
			if i.IsHuman() {
				return nil // already flagged; bulk is add-only
			}
			return mu.AddLabel(ctx, i, "human")
		})
	}
	i := m.visible[m.cursor]
	if i.IsHuman() {
		m.lastAction = repeatableAction{kind: "unflag"}
		return m, runWrite("unflag", i.ID, func(ctx context.Context) error {
			return mu.RemoveLabel(ctx, i, "human")
		})
	}
	m.lastAction = repeatableAction{kind: "flag"}
	return m, runWrite("flag", i.ID, func(ctx context.Context) error {
		return mu.AddLabel(ctx, i, "human")
	})
}

// beginQuickAdd opens a title prompt and on enter files a new issue
// in the repo of the cursor's current row (or the first registered
// workspace if no row is selected). The issue is labeled src:human.
func (m Model) beginQuickAdd() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, nil
	}
	m.mode = modeQuickAdd
	// Capture the cursor's repo so the new issue lands in the same
	// workspace the user is currently looking at. Empty means
	// "first registered repo" in multi-repo mode, or "the one and
	// only client" in single-repo.
	if len(m.visible) > 0 && m.cursor < len(m.visible) {
		m.pendingTarget = beads.Issue{Repo: m.visible[m.cursor].Repo}
	}
	m.input.SetValue("")
	m.input.Prompt = "new ▸ "
	m.input.Placeholder = "title for the new issue"
	m.input.Focus()
	m.ensureCursorVisible()
	return m, textinput.Blink
}

func (m Model) updateQuickAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		m.pendingTarget = beads.Issue{}
		return m, nil
	case "enter":
		title := strings.TrimSpace(m.input.Value())
		repo := m.pendingTarget.Repo
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if title == "" {
			m.setStatus("quick-add cancelled (empty title)")
			return m, nil
		}
		// Refuse to file an orphan task. wyk's working assumption is
		// every issue has an owner; the way to enforce that without
		// putting up another prompt is to require the launcher pass
		// -me (or have a defaultMe() result). The status banner
		// names the fix so a user surprised by the refusal knows
		// what to do.
		if m.me == "" {
			m.setStatus("quick-add cancelled: no assignee. Re-launch with -me=you@example.com (or set git user.email / $USER)")
			return m, nil
		}
		assignee := m.me
		return m, runQuickAdd(func(ctx context.Context) (string, error) {
			return mu.Create(ctx, repo, title, assignee)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// runQuickAdd wraps Mutator.Create in a tea.Cmd that emits a writeMsg
// with the new ID populated as id. handleWriteResult then displays
// the "created <id>" banner and refetches.
func runQuickAdd(fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		id, err := fn(context.Background())
		return writeMsg{action: "create", id: id, err: err}
	}
}

// beginNote opens the textinput prompt for a new note. The full
// target issue is captured here for the same reasons as beginClose —
// see Model.pendingTarget.
func (m Model) beginNote() (tea.Model, tea.Cmd) {
	if m.mutator() == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, nil
	}
	if len(m.visible) == 0 {
		return m, nil
	}
	m.mode = modeNote
	m.pendingTarget = m.visible[m.cursor]
	m.noteArea.Reset()
	m.noteArea.Focus()
	m.ensureCursorVisible()
	return m, textarea.Blink
}

// updateNote drives the multi-line textarea. enter inserts a
// newline (so a long-form note can span multiple lines); ctrl+s
// submits; esc cancels. Empty submission is treated as a cancel
// with a status banner. The pendingTarget snapshot guards against
// a concurrent refetch shifting the cursor — issueExists() check
// matches the close/defer/assign flows.
func (m Model) updateNote(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	if msg.Type == tea.KeyEsc {
		m.mode = m.promptReturn
		m.promptReturn = modeList
		m.noteArea.Blur()
		m.pendingTarget = beads.Issue{}
		return m, nil
	}
	// ctrl+s submits. (Most other terminals send the literal
	// 0x13 byte; bubbletea normalizes to "ctrl+s".)
	if msg.String() == "ctrl+s" {
		text := strings.TrimSpace(m.noteArea.Value())
		target := m.pendingTarget
		fromDetail := m.promptReturn == modeDetail
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = m.promptReturn
		m.promptReturn = modeList
		m.noteArea.Blur()
		if text == "" {
			m.setStatus("note cancelled (empty)")
			return m, nil
		}
		// Detail path trusts its snapshot (see updateConfirmClose);
		// the list path guards against a refetch removing the row.
		if !fromDetail && !m.issueExists(target.ID) {
			m.setStatus("note cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, nil
		}
		// Optimistically append the note to the detail body so it
		// shows immediately — the list refetch won't re-enrich the
		// detail issue, so without this the new note wouldn't appear
		// until the user re-opened the row.
		if fromDetail && m.detailIssue.ID == target.ID {
			if strings.TrimSpace(m.detailIssue.Notes) == "" {
				m.detailIssue.Notes = text
			} else {
				m.detailIssue.Notes += "\n\n" + text
			}
			m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
		}
		// runWriteWithIssue (not runWrite) so the error path in
		// handleWriteResult has the pre-append snapshot to roll the
		// detail body back to on failure. target is m.pendingTarget,
		// captured before the optimistic append above.
		return m, runWriteWithIssue("note", target, func(ctx context.Context) error {
			return mu.Note(ctx, target, text)
		})
	}
	var cmd tea.Cmd
	m.noteArea, cmd = m.noteArea.Update(msg)
	return m, cmd
}

// restoreFilterPrompt resets the shared textinput so the next `/`
// shows the filter UI instead of the note UI.
func (m *Model) restoreFilterPrompt() {
	m.input.Prompt = "/ "
	m.input.Placeholder = "fuzzy filter… (↑↓ select)"
}

// handleBulkWriteResult formats a status banner from a bulk
// dispatch. Total success → "closed/flagged/deferred N rows";
// partial failure → "<verb> K of N (M failed: <first failure>)";
// total failure → "<action> failed for all N rows (<first
// failure>)". On any failure, marks are restored for the failed
// rows so the user can retry without re-marking (dispatch sites
// optimistically clear m.marked; this is the rollback). Refetches
// after a non-total-failure outcome so the new state is visible.
func (m Model) handleBulkWriteResult(msg bulkWriteMsg) (tea.Model, tea.Cmd) {
	// Mirror the single-target path: the bulk close's in-flight marks
	// lift here, on every outcome (failed rows keep their marks
	// restored below so the user can retry them).
	if msg.action == "close" {
		m.clearClosing()
	}
	succeeded := msg.total - len(msg.failed)
	verb := bulkVerbs[msg.action]
	if verb == "" {
		verb = msg.action
	}
	if len(msg.failed) > 0 {
		if m.marked == nil {
			m.marked = make(map[string]bool, len(msg.failed))
		}
		for _, t := range msg.failed {
			m.marked[issueKey(t)] = true
		}
	}
	switch {
	case len(msg.failed) == 0:
		m.setStatus(fmt.Sprintf("%s %d rows", verb, succeeded))
	case succeeded == 0:
		m.setStatus(fmt.Sprintf("%s failed for all %d rows (%s)", msg.action, msg.total, msg.errs[0]))
		return m, nil // sticky banner on total failure
	default:
		m.setStatus(fmt.Sprintf("%s %d of %d (%d failed: %s)", verb, succeeded, msg.total, len(msg.failed), msg.errs[0]))
	}
	// Mirror the single-target path: a bulk status change (close/defer)
	// pushes the succeeded issues out of the open-only list, so the
	// post-write refetch's refreshDepCachesFromList can't correct their
	// cached (status). Patch each one directly so a multi-select close
	// doesn't leave stale dep rows (would-you-kindly-1ym).
	if st, ok := statusForAction(msg.action); ok {
		for _, t := range msg.succeeded {
			m.patchDepCacheStatus(t.ID, st)
		}
	}
	// Optimistically drop the just-closed rows so a multi-select close
	// updates the list immediately, mirroring the single-target path.
	// (Only close is handled; optimisticListUpdate no-ops other actions.)
	if msg.action == "close" {
		for _, t := range msg.succeeded {
			m.optimisticListUpdate("close", t)
		}
	}
	return m, tea.Batch(m.fetchCmd(), flashClearCmd(m.statusGen))
}

// repeatableAction captures the minimum a `.` re-dispatch needs:
// the kind tag (close/defer/assign/label/unlabel/priority/flag/
// unflag) and a stringified arg (empty for kinds that take no
// arg, like close/flag). Zero value means "nothing to repeat".
type repeatableAction struct {
	kind string
	arg  string
}

// handleRepeat re-dispatches lastAction against the cursor row.
// Re-prompts are skipped — the captured arg fires verbatim. Empty
// lastAction surfaces a status banner so the user knows the key
// was understood but had nothing to act on. Bulk-mode repeats are
// not supported: `.` is a single-row tool by design.
func (m Model) handleRepeat() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if m.lastAction.kind == "" {
		m.setStatus("nothing to repeat (do a close / defer / label / etc. first)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to repeat against")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	arg := m.lastAction.arg
	switch m.lastAction.kind {
	case "close":
		// `.` re-dispatches without a confirm prompt, so it's the
		// easiest way to fire a second close into the lag window.
		if m.refuseIfClosing() {
			return m, flashClearCmd(m.statusGen)
		}
		m.markClosing(target)
		m.setStatus("closing " + target.ID + "…")
		return m, runWriteWithIssue("close", target, func(ctx context.Context) error {
			return mu.Close(ctx, target)
		})
	case "defer":
		return m, runWriteWithIssue("defer", target, func(ctx context.Context) error {
			return mu.SetDefer(ctx, target, arg)
		})
	case "assign":
		return m, runWriteWithIssue("assign", target, func(ctx context.Context) error {
			return mu.SetAssignee(ctx, target, arg)
		})
	case "label":
		return m, runWrite("label:"+arg, target.ID, func(ctx context.Context) error {
			return mu.AddLabel(ctx, target, arg)
		})
	case "unlabel":
		return m, runWrite("unlabel:"+arg, target.ID, func(ctx context.Context) error {
			return mu.RemoveLabel(ctx, target, arg)
		})
	case "priority":
		n, err := strconv.Atoi(arg)
		if err != nil {
			m.setStatus(fmt.Sprintf("repeat: stored priority %q is not a number", arg))
			return m, flashClearCmd(m.statusGen)
		}
		return m, runWrite(fmt.Sprintf("set P%d", n), target.ID, func(ctx context.Context) error {
			return mu.SetPriority(ctx, target, n)
		})
	case "type":
		// Replay the stored type verbatim rather than re-cycling
		// from the target's current value — matches how priority
		// replay re-applies the stored value, so `.` is "redo the
		// last write" not "do the same kind of action."
		return m, runWrite(fmt.Sprintf("set type=%s", arg), target.ID, func(ctx context.Context) error {
			return mu.SetIssueType(ctx, target, arg)
		})
	case "flag":
		return m, runWrite("flag", target.ID, func(ctx context.Context) error {
			return mu.AddLabel(ctx, target, "human")
		})
	case "unflag":
		return m, runWrite("unflag", target.ID, func(ctx context.Context) error {
			return mu.RemoveLabel(ctx, target, "human")
		})
	default:
		m.setStatus("repeat: don't know how to redo " + m.lastAction.kind)
		return m, flashClearCmd(m.statusGen)
	}
}

// runWrite wraps a Mutator call in a tea.Cmd that emits a writeMsg.
// All mutators in the Client carry their own per-call timeout, so a
// fresh background context is fine here.
func runWrite(action, id string, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		err := fn(context.Background())
		return writeMsg{action: action, id: id, err: err}
	}
}

// runWriteWithIssue is runWrite that also threads the full Issue
// through to the result. Used by close so handleWriteResult can
// snapshot the row into m.lastClosed for `u` undo without forcing
// every other write path to plumb the issue through.
func runWriteWithIssue(action string, issue beads.Issue, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		err := fn(context.Background())
		return writeMsg{action: action, id: issue.ID, issue: issue, err: err}
	}
}

// runBulkWrite fires fn against each target sequentially and
// reports a single bulkWriteMsg with success/failure detail. We
// run sequentially (not in parallel) so the bd subprocess load
// stays the same as the single-target path — the multi-repo
// HUMAN-BLOCK semaphore already caps fanout, but parallel bulk
// closes would still spike subprocess count and risk reordering
// audit events. The dispatch is O(N) requests but N is the size
// of a user's triage selection (rarely >20), so the latency is
// acceptable.
func runBulkWrite(action string, targets []beads.Issue, fn func(ctx context.Context, i beads.Issue) error) tea.Cmd {
	return func() tea.Msg {
		var failed, succeeded []beads.Issue
		var errs []string
		for _, t := range targets {
			if err := fn(context.Background(), t); err != nil {
				failed = append(failed, t)
				errs = append(errs, fmt.Sprintf("%s: %v", t.ID, err))
			} else {
				succeeded = append(succeeded, t)
			}
		}
		return bulkWriteMsg{action: action, total: len(targets), failed: failed, succeeded: succeeded, errs: errs}
	}
}

// runBulkClose is runBulkWrite's batched sibling for close: one
// BulkCloser call for the whole selection, folded into the same
// bulkWriteMsg shape so handleBulkWriteResult's banner / mark-restore
// logic is shared. Targets keep their marked order in the result.
func runBulkClose(targets []beads.Issue, bc BulkCloser) tea.Cmd {
	return func() tea.Msg {
		failures := map[string]error{}
		for _, f := range bc.CloseMany(context.Background(), targets) {
			failures[issueKey(f.Issue)] = f.Err
		}
		msg := bulkWriteMsg{action: "close", total: len(targets)}
		for _, t := range targets {
			if err, ok := failures[issueKey(t)]; ok {
				msg.failed = append(msg.failed, t)
				msg.errs = append(msg.errs, fmt.Sprintf("%s: %v", t.ID, err))
			} else {
				msg.succeeded = append(msg.succeeded, t)
			}
		}
		return msg
	}
}

// bulkWriteMsg carries the result of a runBulkWrite back to the
// model. action is what was attempted (close/flag/defer); total is
// the batch size; failed lists the issues that errored (parallel
// to errs which holds per-target error strings). Carrying the full
// issues — not just IDs — lets handleBulkWriteResult restore marks
// for failed rows so the user can retry without re-marking.
type bulkWriteMsg struct {
	action    string
	total     int
	failed    []beads.Issue
	succeeded []beads.Issue
	errs      []string
}

// bulkVerbs maps each bulk-capable action to its past-tense form
// for the status banner. A naive `action + "ed"` produced
// "closeed" and "defered"; this explicit map matches what
// handleWriteResult uses for the single-target path.
// Every action passed to runBulkWrite needs an entry: a miss falls
// back to the raw action name, so the type bulk-write's banner read
// "type 3 rows" (would-you-kindly-6gjb).
var bulkVerbs = map[string]string{
	"close":    "closed",
	"flag":     "flagged",
	"defer":    "deferred",
	"priority": "reprioritized",
	"assign":   "reassigned",
	"label":    "labeled",
	"type":     "retyped",
}

// usesTextInput reports whether the current mode renders the shared
// single-line textinput prompt (m.input) — every prompt mode except
// modeNote, which uses the multi-line noteArea.
//
// Three behaviours must agree on this set: viewList renders the prompt,
// bodyHeight reserves its two lines of chrome, and the key-handler
// fallthrough forwards cursor-blink ticks to it. They were three
// hand-copied lists, and the blink one had only modeFilter — so the
// cursor was frozen in every other prompt (would-you-kindly-6gjb).
func (m Model) usesTextInput() bool {
	switch m.mode {
	case modeFilter, modeQuickAdd, modeDefer, modeCommand, modeAssign, modeLabel:
		return true
	}
	return false
}

// rowIndexByKey returns the index of the issue in m.all matching key
// (issueKey — repo-qualified so cross-repo ID collisions don't alias),
// or -1 if absent.
func (m *Model) rowIndexByKey(key string) int {
	for i := range m.all {
		if issueKey(m.all[i]) == key {
			return i
		}
	}
	return -1
}

// optimisticListUpdate applies a just-succeeded close/reopen to m.all
// immediately so the list reflects it without waiting for the
// post-write refetch (which on a multi-repo workspace can take several
// seconds). The refetch still runs and remains the source of truth —
// this only closes the latency gap (would-you-kindly-6dis).
//
// issue is the snapshot the write carried. For reopen it is m.lastClosed,
// captured BEFORE the close, so it holds the row's real pre-close status
// (which bd reopen doesn't guarantee is "open" — it can land in
// in_progress); restoring it needs no guess. Only close/reopen are
// handled: the two flips whose effect on an open/ready view's membership
// is unambiguous. Anything the refetch disagrees with self-corrects.
func (m *Model) optimisticListUpdate(action string, issue beads.Issue) {
	if issue.ID == "" {
		return
	}
	idx := m.rowIndexByKey(issueKey(issue))
	switch action {
	case "close":
		switch {
		case idx < 0:
			return // not in the current view — nothing to do locally
		case m.showClosed:
			// Closed rows stay on screen when closed are shown; flip the
			// status so the closed styling (strikethrough) appears now.
			m.all[idx].Status = "closed"
		default:
			m.all = append(m.all[:idx], m.all[idx+1:]...)
		}
	case "reopen":
		if idx >= 0 {
			// Still in view (reopened from a closed/showClosed view):
			// restore the pre-close status from the snapshot.
			m.all[idx].Status = issue.Status
		} else {
			// Dropped from view when it was closed (the common undo
			// case) — re-add the snapshot so it reappears at once. It
			// already carries the correct pre-close status.
			m.all = append(m.all, issue)
		}
	default:
		return
	}
	m.recomputeVisible()
}

// handleWriteResult sets the status banner and triggers a refetch so
// the list reflects the new state. On error, the banner shows the
// failure message; the existing data stays so the user can retry.
func (m Model) handleWriteResult(msg writeMsg) (tea.Model, tea.Cmd) {
	// Lift the close block first, on BOTH outcomes and before any
	// early return: a close that failed leaves its row on screen with
	// an error banner, and the user must be able to retry it.
	if msg.action == "close" {
		m.clearClosing(msg.issue)
	}
	if msg.err != nil {
		// Roll back an optimistic detail-view mutation when the write
		// failed, so the detail body can't contradict the error
		// banner — a failed reopen would otherwise keep showing
		// Status="open" and an "a: close" footer, and a failed note
		// would leave a phantom entry in the body. The list path never
		// touches detailIssue, so gating on modeDetail + a matching ID
		// confines this to detail-initiated reopen/note. msg.issue is
		// the pre-mutation snapshot threaded by runWriteWithIssue.
		if (msg.action == "reopen" || msg.action == "note") &&
			m.mode == modeDetail && msg.issue.ID != "" && msg.issue.ID == m.detailIssue.ID {
			m.detailIssue = msg.issue
			m.detailVP.SetContent(m.renderDetailBody(m.detailIssue))
		}
		// Create failure has no ID yet — render without the empty
		// "id" slot to keep the message clean (no double-space).
		if msg.id == "" {
			m.setStatus(fmt.Sprintf("%s failed: %s", msg.action, msg.err.Error()))
		} else {
			m.setStatus(fmt.Sprintf("%s %s failed: %s", msg.action, msg.id, msg.err.Error()))
		}
		// Errors stay until the next user action (any keystroke in
		// updateList clears m.status). A 4s auto-wipe is too short
		// for a user who glances away to read the full bd
		// complaint.
		return m, nil
	}
	switch msg.action {
	case "close":
		m.setStatus("closed " + msg.id)
		// Snapshot the row so `u` can reopen it without re-fetching
		// the closed list. Cleared on reopen success (or on a
		// second close, which overwrites this one).
		m.lastClosed = msg.issue
	case "reopen":
		m.setStatus("reopened " + msg.id)
		m.lastClosed = beads.Issue{} // consumed
	case "defer":
		m.setStatus("deferred " + msg.id)
	case "assign":
		m.setStatus("reassigned " + msg.id)
	case "edit":
		m.setStatus("edited " + msg.id)
	case "flag":
		m.setStatus("flagged " + msg.id + " for human")
	case "unflag":
		m.setStatus("unflagged " + msg.id)
	case "note":
		m.setStatus("noted " + msg.id)
	case "create":
		m.setStatus("created " + msg.id)
	default:
		// Compound actions like "label:foo" / "unlabel:foo" carry
		// the label name in the action string itself so the
		// status banner can read "labeled foo a-1" instead of a
		// generic "label a-1". Plain actions (no `:`) fall
		// through to the action-then-id format.
		if name, label, ok := strings.Cut(msg.action, ":"); ok {
			switch name {
			case "label":
				m.setStatus("labeled " + msg.id + " " + label)
			case "unlabel":
				m.setStatus("removed " + label + " from " + msg.id)
			default:
				m.setStatus(msg.action + " " + msg.id)
			}
		} else {
			m.setStatus(msg.action + " " + msg.id)
		}
	}
	// A status-changing mutation (close/reopen/defer) leaves cached
	// detail-view dependency rows showing the OLD status; patch them
	// immediately so re-opening a related issue's detail is accurate
	// even before the refetch lands — and so a just-closed issue
	// (which drops out of the open-only list) gets corrected at all,
	// since refreshDepCachesFromList can't see it there.
	if st, ok := statusForAction(msg.action); ok && msg.id != "" {
		m.patchDepCacheStatus(msg.id, st)
	}
	// Reflect close/reopen in the list right away so the row updates
	// near-instantly instead of after the multi-second refetch.
	m.optimisticListUpdate(msg.action, msg.issue)
	// Refetch so the list reflects the write. Loading flag isn't set
	// here because the existing data is still valid until the new
	// fetch arrives — flashing "loading…" would just be noise.
	return m, tea.Batch(m.fetchCmd(), flashClearCmd(m.statusGen))
}

// resolveDetailDeps returns a Cmd that lazily resolves a single
// issue's forward dependencies AND dependents for the detail view's
// dependency sections, merged back via depsResolvedMsg. Returns nil
// — no Cmd — when no DepLister is wired or both directions are
// already cached / known-failed for this ID (forward deps in
// particular may already be warm from the deps-sort path, in which
// case only the reverse edge is fetched). Both lookups run off the
// Bubble Tea event loop in the Cmd goroutine so the `bd dep list`
// shell-outs never block input. Unlike maybeResolveDeps this is a
// single issue's two lookups, not a fan-out over visible rows, so it
// needs no semaphore.
// statusForAction maps a status-changing mutator action to the status
// the issue now carries, so cached dependency rows that mention it can
// be patched in place rather than re-fetched. It covers ONLY actions
// that move an issue OUT of the default open list — close→closed,
// defer→deferred — where the post-write refetch can no longer see the
// issue to correct it. Reopen is deliberately omitted: a reopened
// issue stays in the open list, so refreshDepCachesFromList freshens
// its cached rows on the next refetch with bd's ACTUAL new status
// (which `bd reopen` doesn't guarantee is "open" — it can land in
// in_progress), avoiding a guessed value. Returns ("", false) for
// status-neutral actions (assign, label, note, edit, …) too.
func statusForAction(action string) (string, bool) {
	switch action {
	case "close":
		return "closed", true
	case "defer":
		return "deferred", true
	}
	return "", false
}

// handleUndo reopens the most-recently-closed issue captured by
// handleWriteResult. Empty m.lastClosed.ID means "nothing to undo"
// — a friendly status banner is more useful than silently doing
// nothing. Read-only sources surface the same "read-only" hint
// the rest of the write keys use.
func (m Model) handleUndo() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if m.lastClosed.ID == "" {
		m.setStatus("nothing to undo")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.lastClosed
	return m, runWriteWithIssue("reopen", target, func(ctx context.Context) error {
		return mu.Reopen(ctx, target)
	})
}

// toggleMark flips the multi-select state on the cursor row.
// First mark allocates m.marked lazily; removing the last mark
// drops the map back to nil so len(m.marked)>0 stays the
// single source of truth for "selection active". Status banner
// surfaces the current count so the user always knows what
// bulk-c/H/d would act on.
func (m Model) toggleMark() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	key := issueKey(m.visible[m.cursor])
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	if m.marked[key] {
		delete(m.marked, key)
	} else {
		m.marked[key] = true
	}
	if len(m.marked) == 0 {
		m.marked = nil
		m.setStatus("no marks")
	} else {
		m.setStatus(fmt.Sprintf("%d marked", len(m.marked)))
	}
	return m, flashClearCmd(m.statusGen)
}

// markedIssues returns every row in m.all whose composite key is
// in m.marked. We scan m.all rather than m.visible so a fuzzy
// filter that hides part of the selection doesn't silently drop
// rows from a bulk dispatch — marks survive filter changes by
// design, and "close 5 rows" should mean five even if only three
// are on screen. Stable ordering follows m.all (bd's native
// order, mirrored by the visible list when no sort is active).
func (m Model) markedIssues() []beads.Issue {
	if len(m.marked) == 0 {
		return nil
	}
	out := make([]beads.Issue, 0, len(m.marked))
	for _, i := range m.all {
		if m.marked[issueKey(i)] {
			out = append(out, i)
		}
	}
	return out
}

// pruneStaleMarks drops marks whose issue is no longer present in m.all
// — e.g. a row that vanished on a refetch after a partial-failure bulk
// action. Without this, a dangling mark keyed by a gone ID would have a
// later bulk op target a row that's no longer visible
// (would-you-kindly-g00n).
func (m *Model) pruneStaleMarks() {
	if len(m.marked) == 0 {
		return
	}
	present := make(map[string]bool, len(m.all))
	for _, i := range m.all {
		present[issueKey(i)] = true
	}
	for k := range m.marked {
		if !present[k] {
			delete(m.marked, k)
		}
	}
}

// bumpPriority nudges the cursor row's (or every marked row's)
// priority by `delta` steps and dispatches the writes. delta == -1
// means "more urgent" (priority--), +1 means "less urgent"
// (priority++). bd's range is 0–4; results are clamped, and a
// no-op (already at the edge) silently passes — bd's update
// command is idempotent on a no-change so re-writing the same
// priority is harmless.
func (m Model) bumpPriority(delta int) (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		// "priority" is the bulkVerbs key; "reprioritized N rows"
		// reads correctly in the banner. The prior copy-paste
		// from the close/flag/defer handlers used "flag", which
		// produced "flagged N rows" for a priority change.
		return m, runBulkWrite("priority", targets, func(ctx context.Context, i beads.Issue) error {
			return mu.SetPriority(ctx, i, clampPriority(i.Priority+delta))
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	i := m.visible[m.cursor]
	newP := clampPriority(i.Priority + delta)
	if newP == i.Priority {
		m.setStatus(fmt.Sprintf("%s already at P%d", i.ID, i.Priority))
		return m, flashClearCmd(m.statusGen)
	}
	m.lastAction = repeatableAction{kind: "priority", arg: strconv.Itoa(newP)}
	return m, runWrite(fmt.Sprintf("set P%d", newP), i.ID, func(ctx context.Context) error {
		return mu.SetPriority(ctx, i, newP)
	})
}

// issueTypeCycle is the rotation order T walks through. Sourced
// from bd's accepted --type values (see internal/beads.CreateOptions
// IssueType doc); the empty string is mapped to "task" so a row
// without a recorded type lands on the default starting point.
var issueTypeCycle = []string{
	"task", "bug", "feature", "chore", "epic",
	"decision", "spike", "story", "milestone",
}

// nextIssueType returns the value one position ahead of cur in
// issueTypeCycle, wrapping at the end. Unknown / empty `cur`
// returns the first entry — the safe rotation start.
func nextIssueType(cur string) string {
	for i, t := range issueTypeCycle {
		if t == cur {
			return issueTypeCycle[(i+1)%len(issueTypeCycle)]
		}
	}
	return issueTypeCycle[0]
}

// handleTypeCycle bumps the cursor row's IssueType one step
// through issueTypeCycle. Same shape as bumpPriority: respects
// the mutator gate, supports the bulk-marked path so a multi-
// select can move every marked row in lockstep, and records a
// repeatableAction so '.' replays the most recent type set.
func (m Model) handleTypeCycle() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) > 0 {
		targets := m.markedIssues()
		m.marked = nil
		return m, runBulkWrite("type", targets, func(ctx context.Context, i beads.Issue) error {
			return mu.SetIssueType(ctx, i, nextIssueType(i.IssueType))
		})
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	i := m.visible[m.cursor]
	newType := nextIssueType(i.IssueType)
	m.lastAction = repeatableAction{kind: "type", arg: newType}
	return m, runWrite(fmt.Sprintf("set type=%s", newType), i.ID, func(ctx context.Context) error {
		return mu.SetIssueType(ctx, i, newType)
	})
}

// clampPriority keeps priority inside bd's 0–4 range. Values
// outside that range get rejected by bd; clamping silently turns
// the no-op edge case (press + on a P0 row) into a tolerable
// "stay put" instead of a spurious error banner.
func clampPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 4 {
		return 4
	}
	return p
}

// beginEdit suspends the TUI, opens $EDITOR on a temp file
// seeded with the cursor row's description, and on return
// dispatches Mutator.SetDescription if the body changed.
// Multi-line and arbitrary-character editing that the textinput
// modes can't do. Uses Detailer (when available) to pull the
// full description rather than the slim list-row copy.
func (m Model) beginEdit() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		m.setStatus("nothing to edit")
		return m, flashClearCmd(m.statusGen)
	}
	target := m.visible[m.cursor]
	body := target.Description
	if d, ok := m.src.(Detailer); ok {
		// The slim list row usually has no Description (bd list drops it),
		// so we MUST fetch the full body before editing. If that fetch
		// fails, ABORT rather than open an empty buffer: the user would
		// see a blank description, assume the issue had none, and a save
		// would overwrite the real (now lost) body — recoverable only via
		// bd history, which wyk doesn't surface. (would-you-kindly-quep)
		full, err := d.Detail(context.Background(), target)
		if err != nil {
			m.setStatus("edit aborted: couldn't load full description (" + err.Error() + ")")
			return m, flashClearCmd(m.statusGen)
		}
		body = full.Description
	}
	// Split $EDITOR into a command + args so multi-word editors work
	// (e.g. EDITOR="code -w" or "emacsclient -nw"); the filename is
	// appended last. strings.Fields handles the common space-separated
	// case — a path with embedded spaces still isn't supported, but a
	// bare or flag-carrying editor is the realistic shape.
	// (would-you-kindly-tgmk)
	editorFields := strings.Fields(os.Getenv("EDITOR"))
	if len(editorFields) == 0 {
		editorFields = []string{"vi"}
	}
	f, err := os.CreateTemp("", "wyk-edit-*.md")
	if err != nil {
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		m.setStatus("edit failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	cmd := exec.Command(editorFields[0], append(editorFields[1:], f.Name())...)
	path := f.Name()
	originalBody := body
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editFinishedMsg{target: target, path: path, originalBody: originalBody, err: err}
	})
}

// editFinishedMsg lands after $EDITOR exits. The handler reads
// the temp file and dispatches SetDescription if the body
// changed; either way the temp is removed before the message is
// fully consumed.
type editFinishedMsg struct {
	target       beads.Issue
	path         string
	originalBody string
	err          error
}

// handleEditFinished processes the ExecProcess callback: if the
// editor exited cleanly AND the body changed AND the target row
// still exists, dispatch SetDescription. Otherwise surface an
// appropriate status banner. Temp file is removed regardless so
// /tmp doesn't fill up with abandoned drafts.
func (m Model) handleEditFinished(msg editFinishedMsg) (tea.Model, tea.Cmd) {
	defer func() { _ = os.Remove(msg.path) }()
	if msg.err != nil {
		m.setStatus("edit aborted: " + msg.err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.setStatus("edit read failed: " + err.Error())
		return m, flashClearCmd(m.statusGen)
	}
	// Normalize trailing newlines on both sides before comparing:
	// vi/vim and most editors append a final '\n' when saving a
	// file that doesn't have one, so an open-and-quit on a body
	// without a trailing newline would otherwise trip the
	// "changed" branch and dispatch a spurious SetDescription. We
	// also send the trimmed body so the stored description
	// doesn't silently accumulate trailing whitespace over
	// repeated edits.
	newBody := strings.TrimRight(string(b), "\n")
	if newBody == strings.TrimRight(msg.originalBody, "\n") {
		m.setStatus("edit: no change")
		return m, flashClearCmd(m.statusGen)
	}
	if !m.issueExists(msg.target.ID) {
		m.setStatus("edit cancelled: " + msg.target.ID + " was removed by a refresh")
		return m, flashClearCmd(m.statusGen)
	}
	target := msg.target
	mu := m.mutator()
	return m, runWriteWithIssue("edit", target, func(ctx context.Context) error {
		return mu.SetDescription(ctx, target, newBody)
	})
}

// beginLabel opens the arbitrary-label prompt. The cursor row's
// label set is the toggle target: if the user enters a label
// already on the row, it's removed; otherwise it's added.
// Mirrors how H toggles `human` specifically, but for any label.
// Bulk path is add-only (matches H's bulk) so a typo can't bulk-
// remove an unrelated label across the selection.
func (m Model) beginLabel() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to label")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeLabel
	m.input.SetValue("")
	if len(m.marked) > 0 {
		m.input.Prompt = fmt.Sprintf("add label to %d rows ▸ ", len(m.marked))
	} else {
		m.input.Prompt = "label ▸ "
	}
	m.input.Placeholder = "name (toggle on cursor; bulk path is add-only)"
	m.input.Focus()
	return m, textinput.Blink
}

// updateLabel drives the label prompt. enter dispatches the
// AddLabel/RemoveLabel pair based on whether the cursor row
// already carries the label; bulk path always adds. Empty
// submission cancels.
func (m Model) updateLabel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		m.pendingTarget = beads.Issue{}
		return m, nil
	case "enter":
		label := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if label == "" {
			m.setStatus("label cancelled (empty)")
			return m, flashClearCmd(m.statusGen)
		}
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("label", targets, func(ctx context.Context, i beads.Issue) error {
				if i.HasLabel(label) {
					return nil // idempotent add
				}
				return mu.AddLabel(ctx, i, label)
			})
		}
		if !m.issueExists(target.ID) {
			m.setStatus("label cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		if target.HasLabel(label) {
			m.lastAction = repeatableAction{kind: "unlabel", arg: label}
			return m, runWrite("unlabel:"+label, target.ID, func(ctx context.Context) error {
				return mu.RemoveLabel(ctx, target, label)
			})
		}
		m.lastAction = repeatableAction{kind: "label", arg: label}
		return m, runWrite("label:"+label, target.ID, func(ctx context.Context) error {
			return mu.AddLabel(ctx, target, label)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// beginAssign opens the assign prompt. Seeded with the current
// assignee (the field the prompt submits via SetAssignee) so the
// common "fix a typo" or "keep me, just confirm" cases are one
// keystroke instead of a re-type.
// Bulk-aware via the marks set; single path snapshots the cursor
// row into pendingTarget so a concurrent refetch can't shift the
// target out from under the prompt.
func (m Model) beginAssign() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to reassign")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeAssign
	if len(m.marked) > 0 {
		m.input.SetValue("")
		m.input.Prompt = fmt.Sprintf("assignee for %d rows ▸ ", len(m.marked))
	} else {
		// Seed with the assignee — the field the prompt submits via
		// SetAssignee. Seeding the Owner (who filed) would make the
		// "just confirm" flow overwrite the assignee with the filer.
		m.input.SetValue(m.pendingTarget.Assignee)
		m.input.Prompt = "assignee ▸ "
	}
	m.input.Placeholder = "ev@example.com (empty = clear)"
	m.input.Focus()
	return m, textinput.Blink
}

// updateAssign drives the owner-change prompt. enter dispatches
// SetAssignee with the typed value; esc cancels. Empty value is
// honored as a deliberate clear (matches bd's behaviour for
// `--assignee ""`).
func (m Model) updateAssign(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		m.pendingTarget = beads.Issue{}
		return m, nil
	case "enter":
		owner := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("assign", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.SetAssignee(ctx, i, owner)
			})
		}
		if !m.issueExists(target.ID) {
			m.setStatus("assignee change cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		m.lastAction = repeatableAction{kind: "assign", arg: owner}
		return m, runWriteWithIssue("assign", target, func(ctx context.Context) error {
			return mu.SetAssignee(ctx, target, owner)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// beginDefer enters modeDefer with a textinput prompt for the
// defer value (+1d, +1w, tomorrow, 2026-06-15 — bd owns parsing).
// Snapshots the cursor row into pendingTarget so a concurrent
// refetch can't shift the target out from under the user mid-
// prompt. Read-only sources show the standard read-only hint.
func (m Model) beginDefer() (tea.Model, tea.Cmd) {
	mu := m.mutator()
	if mu == nil {
		m.setStatus("read-only mode (no Mutator wired up)")
		return m, flashClearCmd(m.statusGen)
	}
	// Bulk path: marks are the targets. Single path: snapshot the
	// cursor row into pendingTarget. Either way we need at least
	// one target to proceed.
	if len(m.marked) == 0 {
		if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
			m.setStatus("nothing to defer")
			return m, flashClearCmd(m.statusGen)
		}
		m.pendingTarget = m.visible[m.cursor]
	}
	m.mode = modeDefer
	m.input.SetValue("")
	if len(m.marked) > 0 {
		m.input.Prompt = fmt.Sprintf("defer %d rows until ▸ ", len(m.marked))
	} else {
		m.input.Prompt = "defer until ▸ "
	}
	m.input.Placeholder = "+1d, +1w, tomorrow, next monday, 2026-06-15…"
	m.input.Focus()
	return m, textinput.Blink
}

// updateDefer drives the defer-until prompt. Submitting an empty
// value cancels; submitting any other value passes through to bd
// via Mutator.SetDefer (which sends it unparsed to bd update
// --defer — bd is the source of truth on what parses).
func (m Model) updateDefer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quitNow()
	}
	switch msg.String() {
	case "esc":
		m.mode = m.promptReturn
		m.promptReturn = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		m.pendingTarget = beads.Issue{}
		return m, nil
	case "enter":
		when := strings.TrimSpace(m.input.Value())
		target := m.pendingTarget
		fromDetail := m.promptReturn == modeDetail
		m.pendingTarget = beads.Issue{}
		mu := m.mutator()
		m.mode = m.promptReturn
		m.promptReturn = modeList
		m.input.Blur()
		m.restoreFilterPrompt()
		if when == "" {
			m.setStatus("defer cancelled (empty value)")
			return m, flashClearCmd(m.statusGen)
		}
		// Bulk path: dispatch SetDefer across every marked row.
		if len(m.marked) > 0 {
			targets := m.markedIssues()
			m.marked = nil
			return m, runBulkWrite("defer", targets, func(ctx context.Context, i beads.Issue) error {
				return mu.SetDefer(ctx, i, when)
			})
		}
		// Mirror the close/note handlers: if a refetch deleted
		// the target while the prompt was open, surface the same
		// friendly cancellation banner instead of shelling out a
		// stale ID to bd and exposing a raw error. The detail path
		// trusts its snapshot (a drilled-in link may be out of the
		// filtered list), so it skips the check.
		if !fromDetail && !m.issueExists(target.ID) {
			m.setStatus("defer cancelled: " + target.ID + " was removed from the workspace by a refresh")
			return m, flashClearCmd(m.statusGen)
		}
		m.lastAction = repeatableAction{kind: "defer", arg: when}
		return m, runWriteWithIssue("defer", target, func(ctx context.Context) error {
			return mu.SetDefer(ctx, target, when)
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
