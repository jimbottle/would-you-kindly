package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/raylytics/would-you-kindly/internal/beads"
	"github.com/raylytics/would-you-kindly/internal/filters"
	"github.com/raylytics/would-you-kindly/internal/registry"
	"github.com/raylytics/would-you-kindly/internal/skills"
	"github.com/raylytics/would-you-kindly/internal/uiconfig"
)

// checkStatus is the verdict for a single doctor check.
type checkStatus int

const (
	statusPass checkStatus = iota
	statusWarn
	statusFail
)

// doctorPerRepoTimeout bounds the bd-query check inside a single
// repo so a locked / syncing / slow-filesystem workspace can't
// hang the whole doctor run. Matched to beads.NewClient()'s
// default Timeout in internal/beads/client.go — the TUI's
// fetchCmd uses context.Background() and inherits THAT default,
// so a repo doctor passes but the TUI fails on would be a
// confusing false signal. Bump both constants together if the
// user's bd commonly takes longer.
const doctorPerRepoTimeout = 10 * time.Second

func (s checkStatus) String() string {
	switch s {
	case statusPass:
		return "PASS"
	case statusWarn:
		return "WARN"
	case statusFail:
		return "FAIL"
	}
	return "?"
}

// MarshalJSON renders the status as a lowercase string ("pass" /
// "warn" / "fail") for the -json output. Lowercase is the more
// conventional JSON key value and stays clearly distinct from
// the String() text-output form.
func (s checkStatus) MarshalJSON() ([]byte, error) {
	switch s {
	case statusPass:
		return []byte(`"pass"`), nil
	case statusWarn:
		return []byte(`"warn"`), nil
	case statusFail:
		return []byte(`"fail"`), nil
	}
	return []byte(`"unknown"`), nil
}

// check is one diagnostic with its outcome and optional detail line.
// leadingBlank tells the text path to emit a blank line before the
// row; used by the informational stanzas (handoff convention,
// update nudge) to preserve the prior layout where those blocks
// were visually set off from the per-check rows. Unexported and
// excluded from JSON via the custom MarshalJSON below.
type check struct {
	name         string
	status       checkStatus
	detail       string
	leadingBlank bool
}

// MarshalJSON exposes the unexported fields under stable JSON
// keys without forcing a refactor to exported fields. The
// indirection isolates "go struct hygiene" (lowercase locals)
// from "JSON contract" (lowercase keys) — keep both clean.
func (c check) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name   string      `json:"name"`
		Status checkStatus `json:"status"`
		Detail string      `json:"detail,omitempty"`
	}{c.name, c.status, c.detail})
}

// runDoctor implements `wyk doctor`: checks the common friction
// points users hit when wyk doesn't appear to be working. Exits 0
// if all checks PASS or only WARN; exits 1 if any FAIL.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit checks as a structured JSON object for CI / dashboard consumption")
	fix := fs.Bool("fix", false, "install wyk's post-commit hook in every registered repo whose hook is missing (foreign / wyk / chained hooks are left alone)")
	dryRun := fs.Bool("dry-run", false, "with -fix, print the plan without installing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk doctor [-json] [-fix [-dry-run]]")
		return 64
	}
	if *asJSON && *fix {
		fmt.Fprintln(os.Stderr, "wyk doctor: -json and -fix are mutually exclusive")
		return 64
	}
	if *dryRun && !*fix {
		fmt.Fprintln(os.Stderr, "wyk doctor: -dry-run only applies with -fix")
		return 64
	}
	if *fix {
		return runDoctorFix(*dryRun)
	}

	checks := collectDoctorChecks()
	hasFail := false
	for _, c := range checks {
		if c.status == statusFail {
			hasFail = true
			break
		}
	}

	if *asJSON {
		emitDoctorJSON(os.Stdout, checks, hasFail)
		if hasFail {
			return 1
		}
		return 0
	}

	for _, c := range checks {
		if c.leadingBlank {
			fmt.Println()
		}
		fmt.Printf("  [%s] %s\n", c.status, c.name)
		if c.detail != "" {
			for _, line := range strings.Split(c.detail, "\n") {
				fmt.Printf("         %s\n", line)
			}
		}
	}

	fmt.Println()
	switch {
	case hasFail:
		fmt.Println("doctor: FAIL — see the [FAIL] lines above")
		return 1
	default:
		fmt.Println("doctor: OK")
		return 0
	}
}

// runDoctorFix walks every registered repo and installs wyk's
// post-commit hook in the ones missing it. Foreign hooks are
// deliberately skipped — silently re-chaining would commit the
// user to a wrapper they may not want; the doctor text output
// already flags those WARN entries and tells the user to run
// `wyk init -chain` or `-force` explicitly. Hooks already wyk's
// (plain or chained) are also skipped — nothing to do.
//
// Exit codes:
//
//	0  all repos in good shape (or, with -dry-run, would be)
//	1  registry / hook-install error for at least one repo (partial work still done)
//	2  registry resolvable but unreadable, or no registered repos
//	64 usage error (handled by caller)
func runDoctorFix(dryRun bool) int {
	// Install any MISSING user skills first — independent of the
	// registry, so this still helps when no repos are registered.
	// Modified skills are left alone (they need `wyk skills install
	// -force`).
	skillsFixed := 0
	if dir, derr := userSkillsDir(); derr != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor: skills:", derr)
	} else if written, werr := installMissingSkills(dir, dryRun); werr != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor: skills:", werr)
	} else if len(written) > 0 {
		verb := "installed"
		if dryRun {
			verb = "would install"
		}
		fmt.Printf("doctor -fix: %s %d skill(s) to %s: %s\n", verb, len(written), dir, strings.Join(written, ", "))
		skillsFixed = len(written)
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor:", err)
		return 2
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor:", err)
		return 2
	}
	if len(reg.Repos) == 0 {
		// No repos to fix, but installing the skills above is itself
		// fixable work this command performs — so report success rather
		// than the "nothing to fix" code 2. In -dry-run skillsFixed
		// counts the skills we WOULD install, so the exit code mirrors
		// what a real run returns (the convention that -dry-run reports
		// the post-fix state, not the current one).
		if skillsFixed > 0 {
			return 0
		}
		fmt.Fprintln(os.Stderr, "wyk doctor: no repos registered — nothing to fix")
		return 2
	}

	hadError := false
	fixed, skipped := 0, 0
	for _, r := range reg.Repos {
		hookPath, herr := resolveGitHookPath(r.Path, "post-commit")
		if herr != nil {
			fmt.Fprintf(os.Stderr, "wyk doctor: %s: resolve hook path: %v\n", r.Name, herr)
			hadError = true
			continue
		}
		body, rerr := os.ReadFile(hookPath)
		switch {
		case errors.Is(rerr, os.ErrNotExist):
			// The fixable case: no hook at all.
			if dryRun {
				fmt.Printf("wyk doctor: would install hook in %s (%s)\n", r.Name, r.Path)
				fixed++
				continue
			}
			if code := installHookIn(r.Path, "-skip-bd-init", "-skip-register"); code != 0 {
				fmt.Fprintf(os.Stderr, "wyk doctor: %s: install failed (exit %d)\n", r.Name, code)
				hadError = true
				continue
			}
			fixed++
		case rerr != nil:
			fmt.Fprintf(os.Stderr, "wyk doctor: %s: read hook: %v\n", r.Name, rerr)
			hadError = true
		case bytes.Contains(body, []byte(hookMarker)):
			// Already wyk's (plain or chained) — nothing to do.
			skipped++
		default:
			// Foreign hook — silently re-chaining is the wrong call;
			// the user should run `(cd <path> && wyk init -chain)` (or
			// -force) themselves. wyk init has no -C flag; it locates the
			// repo from the working directory, so the remediation cd's in.
			fmt.Printf("wyk doctor: %s: foreign hook left alone (run `(cd %q && wyk init -chain)` or `-force` to override)\n", r.Name, r.Path)
			skipped++
		}
	}

	prefix := "doctor -fix"
	verb := "installed"
	if dryRun {
		prefix += " (dry-run)"
		verb = "to install"
	}
	fmt.Printf("%s: %d %s, %d skipped (already-wyk or foreign)\n", prefix, fixed, verb, skipped)
	if hadError {
		return 1
	}
	return 0
}

// installHookIn shells out to runInit in a child directory via a
// process-level cwd switch (restored via defer). os.Chdir is
// process-global, not goroutine-local — fine here because
// runDoctorFix is the only caller and runs single-threaded, but
// callers that introduce concurrency would race. Implemented as a
// package-level var so the fix tests can stub the install side
// effect without spawning real bd.
var installHookIn = func(dir string, extraArgs ...string) int {
	prev, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor: getwd:", err)
		return 1
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, "wyk doctor: chdir:", err)
		return 1
	}
	defer func() { _ = os.Chdir(prev) }()
	return runInit(extraArgs)
}

// collectDoctorChecks gathers every check into a single slice so
// both the text and JSON paths share the same source of truth.
// The conventions and update-nudge stanzas are appended as
// regular check entries — in the text path they render as
// normal rows (which historically were multi-line free-text
// blocks); a multi-line `detail` is how they survive the
// uniformity.
func collectDoctorChecks() []check {
	var checks []check
	checks = append(checks, checkBDOnPath())
	checks = append(checks, checkWykOnPath())
	checks = append(checks, checkEditor())
	checks = append(checks, checkActor())
	checks = append(checks, checkXDGPaths()...)
	regChecks, repos := checkRegistry()
	checks = append(checks, regChecks...)
	for _, r := range repos {
		checks = append(checks, checkRepo(r)...)
	}

	checks = append(checks, checkSkills())

	// Conventions stanza — informational, always pass. Terse on
	// purpose; refers the reader to `wyk conventions` for the
	// full text.
	checks = append(checks, check{
		name:   "handoff convention",
		status: statusPass,
		detail: "human-flagged tasks carry: label=human + label=src:agent\n" +
			"agent inbox: label=src:agent AND NOT label=human AND status!=closed\n" +
			"prefer `wyk handoff <id>` over hand-rolling labels; full text in `wyk conventions`",
		leadingBlank: true,
	})

	// Update nudge from the cached release snapshot. Skipped when
	// the cache is empty (first run, before the background check
	// has populated it).
	if nudge := readUpdateNudge(versionString()); nudge != "" {
		checks = append(checks, check{
			name:         "wyk update available",
			status:       statusWarn,
			detail:       nudge + "\nRun `wyk update` to install (or `wyk update -dry-run` to see the install command first).",
			leadingBlank: true,
		})
	}
	return checks
}

// checkSkills reports whether wyk's agent skills are installed and
// current at the user skills dir (~/.claude/skills). PASS when every
// skill is byte-current; WARN (with the install hint) when any is
// missing or locally modified — never FAIL, since the skills are an
// optional convenience, not required for wyk to work. `wyk doctor
// -fix` installs the missing ones.
func checkSkills() check {
	const name = "wyk agent skills"
	dir, err := userSkillsDir()
	if err != nil {
		return check{name: name, status: statusWarn, detail: err.Error(), leadingBlank: true}
	}
	all, err := skills.All()
	if err != nil {
		return check{name: name, status: statusWarn, detail: "could not read embedded skills: " + err.Error(), leadingBlank: true}
	}
	var missing, stale, modified, current []string
	for _, s := range all {
		st, err := skillStateAt(s, dir)
		if err != nil {
			return check{name: name, status: statusWarn, detail: fmt.Sprintf("%s: %v", s.Name, err), leadingBlank: true}
		}
		switch st {
		case skillMissing:
			missing = append(missing, s.Name)
		case skillStale:
			stale = append(stale, s.Name)
		case skillModified:
			modified = append(modified, s.Name)
		default:
			current = append(current, s.Name)
		}
	}
	if len(missing) == 0 && len(stale) == 0 && len(modified) == 0 {
		return check{name: name, status: statusPass, detail: fmt.Sprintf("%d skill(s) current in %s", len(current), dir)}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		parts = append(parts, "out of date: "+strings.Join(stale, ", "))
	}
	if len(modified) > 0 {
		parts = append(parts, "locally modified: "+strings.Join(modified, ", "))
	}
	return check{
		name:         name,
		status:       statusWarn,
		detail:       strings.Join(parts, "; ") + "\nRun `wyk skills install` to install missing and refresh out-of-date skills (`-force` overwrites a modified skill); `wyk doctor -fix` installs the missing ones.",
		leadingBlank: true,
	}
}

// doctorJSONOut is the top-level shape emitted by -json. The
// overall verdict mirrors the exit code so a consumer can drive
// CI gating from JSON output alone without re-reading the worst
// status itself.
type doctorJSONOut struct {
	Verdict string  `json:"verdict"` // "ok" or "fail"
	Checks  []check `json:"checks"`
}

func emitDoctorJSON(w *os.File, checks []check, hasFail bool) {
	out := doctorJSONOut{Verdict: "ok", Checks: checks}
	if hasFail {
		out.Verdict = "fail"
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// --- individual checks ---

func checkBDOnPath() check {
	path, err := exec.LookPath("bd")
	if err != nil {
		return check{
			name:   "bd binary on PATH",
			status: statusFail,
			detail: "install bd from https://github.com/gastownhall/beads — wyk shells out to it for every read and write",
		}
	}
	// Try to read the version so we know we can actually invoke it.
	out, vErr := exec.Command("bd", "--version").Output()
	version := "(unknown version)"
	if vErr == nil {
		version = strings.TrimSpace(string(out))
	}
	return check{
		name:   "bd binary on PATH",
		status: statusPass,
		detail: path + " — " + version,
	}
}

func checkWykOnPath() check {
	path, err := exec.LookPath("wyk")
	if err != nil {
		// If we're running, we DID start somehow — probably via a
		// full path or a build tree. The hook needs `wyk` on PATH
		// though, so this is worth flagging as a WARN rather than a
		// hard FAIL.
		return check{
			name:   "wyk binary on PATH",
			status: statusWarn,
			detail: "wyk isn't on PATH; the post-commit hook (which execs `wyk hook post-commit`) won't work at commit time. Install wyk via `go install` or move the binary into your PATH.",
		}
	}
	return check{name: "wyk binary on PATH", status: statusPass, detail: path}
}

// checkEditor reports the resolved $EDITOR and whether the binary
// actually exists on PATH. WARN (not FAIL) when EDITOR is unset
// because the TUI's `e` key falls back to `vi` — it still works,
// just maybe not in the user's preferred editor. FAIL only when
// the chosen binary doesn't resolve.
func checkEditor() check {
	editor := os.Getenv("EDITOR")
	fallback := false
	if editor == "" {
		editor = "vi"
		fallback = true
	}
	path, err := exec.LookPath(editor)
	if err != nil {
		return check{
			name:   "$EDITOR resolves",
			status: statusFail,
			detail: fmt.Sprintf("the TUI's `e` key opens %q on the description; not on PATH. Set EDITOR to a binary you have installed (e.g. vim, nvim, nano, code -w).", editor),
		}
	}
	st := statusPass
	detail := fmt.Sprintf("%s — %s", editor, path)
	if fallback {
		st = statusWarn
		detail = fmt.Sprintf("%s — %s (fallback; $EDITOR is unset)", editor, path)
	}
	return check{name: "$EDITOR resolves", status: st, detail: detail}
}

// checkActor reports the audit-trail identity bd uses when wyk
// writes (close / note / etc.). Resolution order matches bd's:
// $BEADS_ACTOR, then git user.name, then $USER. WARN when none
// is set so a future `bd audit` walk won't show empty actors.
func checkActor() check {
	if v := os.Getenv("BEADS_ACTOR"); v != "" {
		return check{name: "audit-trail actor", status: statusPass, detail: "$BEADS_ACTOR = " + v}
	}
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return check{name: "audit-trail actor", status: statusPass, detail: "git user.name = " + name}
		}
	}
	if v := os.Getenv("USER"); v != "" {
		return check{name: "audit-trail actor", status: statusPass, detail: "$USER = " + v}
	}
	return check{
		name:   "audit-trail actor",
		status: statusWarn,
		detail: "set $BEADS_ACTOR (or git config user.name) so the bd audit trail records who acted",
	}
}

// checkXDGPaths reports the resolved config-file locations for
// wyk's three per-user state files (registry, ui prefs, filter
// aliases). Each path gets its own PASS/WARN line so a user can
// tell at a glance where wyk would read from. WARN when the file
// is missing (not FAIL — first-run state is fine; the user just
// hasn't seeded that file yet).
func checkXDGPaths() []check {
	var out []check
	for _, e := range []struct {
		name string
		path func() (string, error)
	}{
		{"~/.config/wyk/repos.json", registry.DefaultPath},
		{"~/.config/wyk/ui.json", uiconfig.DefaultPath},
		{"~/.config/wyk/filters.json", filters.DefaultPath},
	} {
		p, err := e.path()
		if err != nil {
			out = append(out, check{
				name:   e.name,
				status: statusWarn,
				detail: "could not resolve path: " + err.Error(),
			})
			continue
		}
		if _, err := os.Stat(p); err != nil {
			out = append(out, check{
				name:   e.name,
				status: statusWarn,
				detail: p + " (not yet created — wyk seeds it on first write)",
			})
			continue
		}
		out = append(out, check{
			name:   e.name,
			status: statusPass,
			detail: p,
		})
	}
	return out
}

func checkRegistry() ([]check, []registry.Repo) {
	regPath, err := registry.DefaultPath()
	if err != nil {
		return []check{{
			name:   "wyk registry resolvable",
			status: statusFail,
			detail: err.Error(),
		}}, nil
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return []check{{
			name:   "wyk registry parseable",
			status: statusFail,
			detail: fmt.Sprintf("%s: %v", regPath, err),
		}}, nil
	}
	if len(reg.Repos) == 0 {
		return []check{{
			name:   "wyk registry has at least one repo",
			status: statusWarn,
			detail: fmt.Sprintf("no repos registered in %s — run `wyk init` in any project to start tracking it", regPath),
		}}, nil
	}
	return []check{{
		name:   "wyk registry",
		status: statusPass,
		detail: fmt.Sprintf("%s — %d repo(s) registered", regPath, len(reg.Repos)),
	}}, reg.Repos
}

func checkRepo(r registry.Repo) []check {
	prefix := "repo " + r.Name
	var out []check

	// .git present? Accepts either a directory or a gitlink file
	// (`.git` containing `gitdir: <path>`, as worktrees and
	// submodules produce). os.Stat handles both.
	if _, err := os.Stat(filepath.Join(r.Path, ".git")); err != nil {
		out = append(out, check{
			name:   prefix + ": .git/ present",
			status: statusFail,
			detail: r.Path + " is registered but its .git is missing or unreadable (was the repo moved or deleted? consider `wyk init` from the new location or hand-edit ~/.config/wyk/repos.json)",
		})
		return out
	}

	// .beads directory present? Emitted independently of the bd
	// query check below so the per-repo row inventory is stable —
	// users always see SOMETHING about .beads, even if bd itself
	// is broken / missing / slow.
	beadsDir := filepath.Join(r.Path, ".beads")
	if _, err := os.Stat(beadsDir); err != nil {
		out = append(out, check{
			name:   prefix + ": .beads/ present",
			status: statusFail,
			detail: "no bd workspace; run `bd init` in " + r.Path,
		})
	} else {
		out = append(out, check{name: prefix + ": .beads/ present", status: statusPass})

		// Separate check: does bd actually respond? Bounded by a
		// timeout so a broken/locked workspace doesn't hang the whole
		// doctor run. Matched to beads.NewClient()'s default Timeout
		// (the value the TUI inherits per call) — a repo that
		// responds inside doctor's window but not the TUI's would be
		// a confusing false pass; aligning both means a doctor
		// warning predicts a TUI refresh failure.
		//
		// Detect timeouts via ctx.Err() rather than errors.Is on the
		// returned error: exec.CommandContext kills the process when
		// the context expires, and cmd.Run() returns an *exec.ExitError
		// like "signal: killed" — which does NOT wrap
		// context.DeadlineExceeded. The context itself does, so check
		// the ctx state BEFORE calling cancel().
		ctx, cancel := context.WithTimeout(context.Background(), doctorPerRepoTimeout)
		c := beads.NewClient()
		c.Dir = r.Path
		issues, qerr := c.Query(ctx, `status!=closed`)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case qerr == nil:
			out = append(out, check{name: prefix + ": bd query responds", status: statusPass})
			// Owner convention: every task must show an owner badge in the
			// TUI (HUMAN / AGENT / HUMAN-BLOCK). That badge is blank when an
			// issue carries neither the `human` label nor `src:agent`. Reuse
			// the open-issue list to flag any that would render blank — the
			// mechanical guard for the convention bd can't enforce.
			if nb := unbadgedIssueIDs(issues); len(nb) > 0 {
				out = append(out, check{
					name:   prefix + ": issues with no owner badge",
					status: statusWarn,
					detail: fmt.Sprintf("%d non-closed issue(s) have no owner — the TUI owner column is blank (no `human` or `src:agent` label): %s. Agent-filed: `bd label add <id> src:agent --dolt-auto-commit=on`; human tasks: `wyk handoff <id>`.",
						len(nb), summarizeIDs(nb, 10)),
				})
			}
		case timedOut:
			out = append(out, check{
				name:   prefix + ": bd query responds",
				status: statusWarn,
				detail: fmt.Sprintf("bd didn't respond within %s — workspace may be locked, syncing, or on a slow filesystem", doctorPerRepoTimeout),
			})
		case errors.Is(qerr, beads.ErrBDNotFound):
			// already caught by checkBDOnPath; don't double-up
		default:
			out = append(out, check{
				name:   prefix + ": bd query responds",
				status: statusWarn,
				detail: qerr.Error(),
			})
		}
	}

	// post-commit hook — is wyk's (plain or chained), foreign, or absent?
	// Resolve via git so gitlinks (.git as a file) and worktrees land on
	// the right hook; raw filepath.Join(r.Path, ".git", ...) breaks for
	// subdirectory registrations whose parent owns the actual git dir.
	hookPath, herr := resolveGitHookPath(r.Path, "post-commit")
	if herr != nil {
		out = append(out, check{
			name:   prefix + ": post-commit hook readable",
			status: statusFail,
			detail: herr.Error(),
		})
		return out
	}
	body, err := os.ReadFile(hookPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out = append(out, check{
			name:   prefix + ": post-commit hook installed",
			status: statusWarn,
			detail: "no post-commit hook in this repo — commits won't auto-close referenced issues. Run `(cd " + r.Path + " && wyk init)` to install it.",
		})
	case err != nil:
		out = append(out, check{
			name:   prefix + ": post-commit hook readable",
			status: statusFail,
			detail: err.Error(),
		})
	default:
		// Reuse the same hookMarker / chainedHookMarker constants the
		// install path uses, so doctor and install can't drift.
		switch {
		case bytes.Contains(body, []byte(chainedHookMarker)):
			// Chained variant — verify the .pre-wyk file is still around.
			preWyk := hookPath + ".pre-wyk"
			if _, perr := os.Stat(preWyk); perr != nil {
				out = append(out, check{
					name:   prefix + ": chained hook's .pre-wyk preserved",
					status: statusFail,
					detail: ".pre-wyk file is missing — the chained wrapper will silently skip the preserved hook. Restore the original or re-run `wyk init -force` to drop chaining.",
				})
			} else {
				out = append(out, check{
					name:   prefix + ": post-commit hook (chained)",
					status: statusPass,
					detail: "wyk's wrapper + preserved " + preWyk,
				})
			}
		case bytes.Contains(body, []byte(hookMarker)):
			out = append(out, check{name: prefix + ": post-commit hook (wyk)", status: statusPass})
		default:
			out = append(out, check{
				name:   prefix + ": post-commit hook (foreign)",
				status: statusWarn,
				detail: "an unfamiliar post-commit hook is installed. wyk's auto-close won't run. Re-run `(cd " + r.Path + " && wyk init -chain)` to keep both, or `-force` to replace.",
			})
		}
	}

	// Surface a core.hooksPath that redirects hooks away from where wyk
	// installs its hook. This is the silent cause of "`Closes:` did
	// nothing": git runs hooks from the redirected dir, so wyk's hook in
	// .git/hooks never executes. (resolveGitHookPath above already follows
	// core.hooksPath, so the classification reflects the *active* hook —
	// but it doesn't explain WHY wyk's hook is bypassed; this does.)
	if activeDir, redirected, inside, wykActive := hooksPathRedirect(r.Path); redirected && !wykActive {
		detail := "git's core.hooksPath redirects post-commit hooks to " + activeDir +
			", so wyk's hook in .git/hooks is bypassed and `Closes:`/`Fixes:` auto-close won't run. "
		if inside {
			detail += "Re-run `(cd " + r.Path + " && wyk init)` to install into the active hooks dir, or unset core.hooksPath."
		} else {
			detail += "That path is outside this repo (likely stale) — clear it: `git -C " + r.Path + " config --unset core.hooksPath`."
		}
		out = append(out, check{name: prefix + ": core.hooksPath redirect", status: statusWarn, detail: detail})
	}
	return out
}

// unbadgedIssueIDs returns the IDs of issues that render with NO owner
// badge in the TUI — carrying neither the `human` label (HUMAN) nor
// `src:agent` (AGENT / HUMAN-BLOCK). This mirrors responsibilityBadgeFor's
// blank case, the project's "task has no owner" signal that bd can't
// enforce. Pulled out so the filter is unit-testable without a live bd
// workspace.
func unbadgedIssueIDs(issues []beads.Issue) []string {
	var out []string
	for _, i := range issues {
		if !i.IsHuman() && !i.HasLabel("src:agent") {
			out = append(out, i.ID)
		}
	}
	return out
}

// summarizeIDs joins IDs for a check detail, capping the list at limit
// with a "(+N more)" tail so a repo with dozens of offenders doesn't
// flood the doctor output. (limit, not max — that shadows a builtin.)
func summarizeIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:limit], ", ") + fmt.Sprintf(" (+%d more)", len(ids)-limit)
}
