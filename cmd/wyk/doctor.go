package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
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

// check is one diagnostic with its outcome and optional detail line.
type check struct {
	name   string
	status checkStatus
	detail string
}

// runDoctor implements `wyk doctor`: checks the common friction
// points users hit when wyk doesn't appear to be working. Exits 0
// if all checks PASS or only WARN; exits 1 if any FAIL.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk doctor")
		return 64
	}

	var checks []check
	checks = append(checks, checkBDOnPath())
	checks = append(checks, checkWykOnPath())
	regChecks, repos := checkRegistry()
	checks = append(checks, regChecks...)
	for _, r := range repos {
		checks = append(checks, checkRepo(r)...)
	}

	hasFail := false
	for _, c := range checks {
		fmt.Printf("  [%s] %s\n", c.status, c.name)
		if c.detail != "" {
			for _, line := range strings.Split(c.detail, "\n") {
				fmt.Printf("         %s\n", line)
			}
		}
		if c.status == statusFail {
			hasFail = true
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

	// .git directory present?
	gitDir := filepath.Join(r.Path, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		out = append(out, check{
			name:   prefix + ": .git/ present",
			status: statusFail,
			detail: r.Path + " is registered but its .git directory is missing or unreadable (was the repo moved or deleted? consider `wyk init` from the new location or hand-edit ~/.config/wyk/repos.json)",
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c := beads.NewClient()
		c.Dir = r.Path
		_, qerr := c.Query(ctx, `status!=closed`)
		cancel()
		switch {
		case qerr == nil:
			out = append(out, check{name: prefix + ": bd query responds", status: statusPass})
		case errors.Is(qerr, context.DeadlineExceeded):
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
	hookPath := filepath.Join(gitDir, "hooks", "post-commit")
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
