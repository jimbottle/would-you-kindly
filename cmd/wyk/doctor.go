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
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filters"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/skills"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
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
	checks = append(checks, checkBDVersion())
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
			"multi-agent: route with `wyk handoff --identity <name>` (label src:agent:<name>); read with `wyk inbox --identity <name>` / $WYK_AGENT_IDENTITY\n" +
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

// bd version compatibility bounds. wyk shells out to `bd` for every
// read and write (internal/beads), so its JSON shapes, query syntax,
// and flags (e.g. --dolt-auto-commit) are an unversioned contract
// between the two binaries. A bd that's too old can lack flags wyk
// passes; a much newer major could change the JSON wyk parses. These
// bounds turn that skew into an explicit doctor signal instead of a
// silent empty list or opaque parse error downstream.
const (
	// minBDVersion is the oldest bd wyk is known to work against;
	// below it doctor FAILs. Bump together with any wyk change that
	// depends on a newer bd flag/shape.
	minBDVersion = "v1.0.0"
	// testedBDMajor is the highest bd major wyk has been tested
	// against; a newer major WARNs (likely fine, but unverified).
	testedBDMajor = "v1"
)

// bdVersionRE pulls the first X.Y.Z token out of `bd --version` output
// ("bd version 1.0.4 (sha…)").
var bdVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

// checkBDVersion verifies the bd on PATH is a version wyk knows how to
// talk to. FAIL only below the known-good minimum (genuinely
// incompatible); WARN for an unparseable version or a newer-than-tested
// major (probably works, but the bd JSON/flags wyk parses are
// unverified there); PASS in range. Kept distinct from checkBDOnPath so
// "bd present" and "bd compatible" read as separate signals.
func checkBDVersion() check {
	const name = "bd version compatible"
	if _, err := exec.LookPath("bd"); err != nil {
		// checkBDOnPath already FAILs with install instructions; don't
		// double up a hard failure — just note why we can't check.
		return check{name: name, status: statusWarn, detail: "bd not on PATH — can't check version (see the bd-on-PATH check above)"}
	}
	out, err := exec.Command("bd", "--version").Output()
	if err != nil {
		return check{name: name, status: statusWarn, detail: "couldn't run `bd --version`: " + err.Error()}
	}
	st, detail := classifyBDVersion(string(out))
	return check{name: name, status: st, detail: detail}
}

// classifyBDVersion is the pure decision half of checkBDVersion: it
// turns raw `bd --version` output into a (status, detail) verdict so
// the version-comparison logic is unit-testable without a real bd.
func classifyBDVersion(versionOutput string) (checkStatus, string) {
	raw := bdVersionRE.FindString(versionOutput)
	if raw == "" {
		return statusWarn, "couldn't parse a version from: " + strings.TrimSpace(versionOutput)
	}
	ver := "v" + raw
	minStr := strings.TrimPrefix(minBDVersion, "v")
	majorStr := strings.TrimPrefix(testedBDMajor, "v")
	switch {
	case semver.Compare(ver, minBDVersion) < 0:
		return statusFail, fmt.Sprintf("bd %s is older than the minimum wyk supports (%s) — upgrade bd: https://github.com/gastownhall/beads", raw, minStr)
	case semver.Compare(semver.Major(ver), testedBDMajor) > 0:
		return statusWarn, fmt.Sprintf("bd %s is newer than the latest tested major (%s.x) — wyk may still work, but the bd JSON/flags it parses are unverified at this version", raw, majorStr)
	default:
		return statusPass, fmt.Sprintf("bd %s is within the supported range (%s – %s.x)", raw, minStr, majorStr)
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

// doltRemoteCheck classifies the output of `bd dolt remote list` for a
// repo into a durability check. ok=false means "skip" (bd dolt errored —
// the broken-workspace case is already covered by the bd-query check, so
// we don't add a noisy duplicate). A URL in the output ("://") means a
// remote is configured (PASS, with a manual-push reminder); otherwise no
// remote is configured (WARN) — covering both empty output and a
// "no remotes" message without depending on bd's exact phrasing.
func doltRemoteCheck(prefix string, out []byte, err error) (check, bool) {
	if err != nil {
		return check{}, false
	}
	if strings.Contains(string(out), "://") {
		first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
		return check{
			name:   prefix + ": Dolt remote",
			status: statusPass,
			detail: first + " — wyk auto-commits locally; replicate cross-machine with `bd dolt push` (wyk never pushes for you).",
		}, true
	}
	return check{
		name:   prefix + ": Dolt remote",
		status: statusWarn,
		detail: "no Dolt remote configured — issues live only in this machine's local Dolt; for cross-machine durability run `bd dolt remote add <name> <url>` then `bd dolt push`.",
	}, true
}

// claudeBlockSalienceNote returns a hint when the wyk conventions block
// sits deep in a LONG CLAUDE.md — it's loaded either way, but low in a
// big file an agent is likelier to under-weight it (would-you-kindly-rp73).
// Empty string = block is well-placed (or the file is short), so the
// PASS row stays clean.
func claudeBlockSalienceNote(body []byte) string {
	idx := bytes.Index(body, []byte(wykConventionsBeginPrefix))
	if idx < 0 {
		return ""
	}
	markerLine := bytes.Count(body[:idx], []byte("\n")) + 1 // 1-based
	total := bytes.Count(body, []byte("\n")) + 1
	// Only nag for a genuinely long file with the block in its bottom third.
	if total >= 150 && markerLine*3 >= total*2 {
		return fmt.Sprintf("present, but the conventions block starts at line %d of %d — near the bottom of a long CLAUDE.md, where an agent may under-weight it. Consider moving it up or trimming the file.", markerLine, total)
	}
	return ""
}

// classifyGuardHook turns the contents of a repo's .claude/settings.json
// into a check for the bd-create-guard PreToolUse hook. data/readErr are
// the os.ReadFile result; repoPath is only for the re-init hint. The PASS
// case carries an explicit caveat: doctor verifies the FILE, but cannot
// confirm Claude Code actually TRUSTS/RUNS the project hooks — that
// approval state is invisible here (would-you-kindly-rp73).
func classifyGuardHook(prefix, repoPath string, data []byte, readErr error) check {
	name := prefix + ": bd-create-guard hook"
	reinit := "Re-run `(cd " + repoPath + " && wyk init)` to install it."
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return check{name: name, status: statusWarn,
			detail: "no .claude/settings.json — the bd-create-guard PreToolUse hook (redirects an agent's `bd create` to `wyk create`) isn't installed, so plans here may be filed with raw `bd create`/TodoWrite. " + reinit}
	case readErr != nil:
		return check{name: name, status: statusWarn, detail: "couldn't read .claude/settings.json: " + readErr.Error()}
	}
	var root map[string]any
	if jerr := json.Unmarshal(data, &root); jerr != nil {
		return check{name: name, status: statusWarn,
			detail: ".claude/settings.json is not valid JSON (" + jerr.Error() + ") — Claude Code won't load its hooks. Fix it, then " + reinit}
	}
	if !claudeSettingsHasHook(root, claudeSettingsHook) {
		return check{name: name, status: statusWarn,
			detail: "the bd-create-guard PreToolUse hook is missing from .claude/settings.json. " + reinit}
	}
	return check{name: name, status: statusPass,
		detail: "bd-create-guard PreToolUse hook present in .claude/settings.json.\nNOTE: doctor verifies the FILE only — Claude Code must TRUST/approve the project's hooks to actually run it. Verify in-session with `/hooks`; an unapproved hook silently never fires."}
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
		_, qerr := c.Query(ctx, `status!=closed`)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case qerr == nil:
			out = append(out, check{name: prefix + ": bd query responds", status: statusPass})
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

		// Dolt durability (would-you-kindly-wcsu): bd auto-commits every
		// wyk write to the LOCAL Dolt, but nothing pushes it — so without
		// a Dolt remote, issues live only on this machine and a teammate
		// never sees them. bd exposes no ahead/behind count, so we surface
		// the next-best signal: whether a remote is configured at all.
		//
		// This execs bd directly rather than through beads.Client's
		// swappable runner (which has no DoltRemoteList method) — a
		// deliberate exception to the CLAUDE.md "use the runner" convention
		// for this ADVISORY-only row: the classification (the part that
		// matters) is extracted into the unit-tested doltRemoteCheck, and a
		// bad read just drops the row (rerr != nil → ok=false). .Output()
		// captures stdout only by design — bd prints the remote list there;
		// if a build emits it elsewhere we under-detect (WARN, the safe
		// direction), never a false PASS (roborev #1844).
		dctx, dcancel := context.WithTimeout(context.Background(), doctorPerRepoTimeout)
		rout, rerr := exec.CommandContext(dctx, "bd", "-C", r.Path, "dolt", "remote", "list").Output()
		dcancel()
		if c, ok := doltRemoteCheck(prefix, rout, rerr); ok {
			out = append(out, c)
		}
	}

	// CLAUDE.md carries wyk's conventions? This is what makes a repo
	// usable BY AN AGENT — without it the agent is bd-aware but not
	// wyk-aware ("build the plan in wyk" is a no-op). Repos init'd
	// before this seed existed won't have the block; nudge them to
	// re-run init. WARN, not FAIL: the block is enrichment, the bd
	// workspace + hook are the load-bearing parts.
	claudeMDPath := filepath.Join(r.Path, "CLAUDE.md")
	switch body, err := os.ReadFile(claudeMDPath); {
	case err == nil && bytes.Contains(body, []byte(wykConventionsBeginPrefix)):
		c := check{name: prefix + ": CLAUDE.md wyk-aware", status: statusPass}
		// Salience: the block is loaded, but buried at the bottom of a long
		// CLAUDE.md is easy for a model to under-weight (would-you-kindly-rp73).
		if note := claudeBlockSalienceNote(body); note != "" {
			c.detail = note
		}
		out = append(out, c)
	case err == nil || errors.Is(err, os.ErrNotExist):
		noun := "CLAUDE.md has no wyk conventions block"
		if errors.Is(err, os.ErrNotExist) {
			noun = "no CLAUDE.md"
		}
		out = append(out, check{
			name:   prefix + ": CLAUDE.md wyk-aware",
			status: statusWarn,
			detail: noun + " — agents here are bd-aware but not wyk-aware (`build the plan in wyk` won't map to `bd create`). Re-run `(cd " + r.Path + " && wyk init)` to seed it.",
		})
	default:
		out = append(out, check{
			name:   prefix + ": CLAUDE.md wyk-aware",
			status: statusWarn,
			detail: "couldn't read CLAUDE.md: " + err.Error(),
		})
	}

	// bd-create-guard PreToolUse hook in .claude/settings.json — the OTHER
	// half of the agent-enrichment (it redirects a Bash `bd create` to
	// `wyk create`). doctor checked the CLAUDE.md block but never this hook,
	// so a missing/malformed guard passed silently (would-you-kindly-rp73).
	settingsPath := filepath.Join(r.Path, ".claude", "settings.json")
	sData, sErr := os.ReadFile(settingsPath)
	out = append(out, classifyGuardHook(prefix, r.Path, sData, sErr))

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
