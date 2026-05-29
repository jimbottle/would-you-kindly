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

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/filters"
	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/uiconfig"
)

// checkStatus is the verdict for a single doctor check.
type checkStatus int

const (
	statusPass checkStatus = iota
	statusWarn
	statusFail
)

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
type check struct {
	name   string
	status checkStatus
	detail string
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
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk doctor [-json]")
		return 64
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

	// Conventions stanza — informational, always pass. Terse on
	// purpose; refers the reader to `wyk conventions` for the
	// full text.
	checks = append(checks, check{
		name:   "handoff convention",
		status: statusPass,
		detail: "human-flagged tasks carry: label=human + label=src:agent\n" +
			"agent inbox: label=src:agent AND NOT label=human AND status!=closed\n" +
			"prefer `wyk handoff <id>` over hand-rolling labels; full text in `wyk conventions`",
	})

	// Update nudge from the cached release snapshot. Skipped when
	// the cache is empty (first run, before the background check
	// has populated it).
	if nudge := readUpdateNudge(versionString()); nudge != "" {
		checks = append(checks, check{
			name:   "wyk update available",
			status: statusWarn,
			detail: nudge + "\nRun `wyk update` to install (or `wyk update -dry-run` to see the install command first).",
		})
	}
	return checks
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
		// doctor run.
		//
		// Detect timeouts via ctx.Err() rather than errors.Is on the
		// returned error: exec.CommandContext kills the process when
		// the context expires, and cmd.Run() returns an *exec.ExitError
		// like "signal: killed" — which does NOT wrap
		// context.DeadlineExceeded. The context itself does, so check
		// the ctx state BEFORE calling cancel().
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
				detail: "bd didn't respond within 5s — workspace may be locked, syncing, or on a slow filesystem",
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
			detail: "no post-commit hook in this repo — commits won't auto-close referenced issues. Run `wyk init -C " + r.Path + "` to install it.",
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
				detail: "an unfamiliar post-commit hook is installed. wyk's auto-close won't run. Re-run `wyk init -C " + r.Path + " -chain` to keep both, or `-force` to replace.",
			})
		}
	}
	return out
}
