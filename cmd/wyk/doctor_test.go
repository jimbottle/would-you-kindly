package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

func TestCheckStatus_String(t *testing.T) {
	cases := []struct {
		s    checkStatus
		want string
	}{
		{statusPass, "PASS"},
		{statusWarn, "WARN"},
		{statusFail, "FAIL"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("checkStatus(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// docRepo creates a fake registered repo under a tempdir, optionally
// with a git dir, a .beads dir, and a post-commit hook of the given
// body. The Repo.Name is derived from the directory's basename, the
// path is the directory's absolute resolved path.
func docRepo(t *testing.T, withGit, withBeads bool, hookBody string) registry.Repo {
	t.Helper()
	dir := t.TempDir()
	if withGit {
		// git init produces a .git directory + .git/hooks
		cmd := exec.Command("git", "init", "--quiet", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
	if withBeads {
		if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if hookBody != "" {
		hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// EvalSymlinks for the same reason the registry does it — macOS
	// /var → /private/var would otherwise produce two distinct paths.
	resolved, _ := filepath.EvalSymlinks(dir)
	return registry.Repo{Name: filepath.Base(dir), Path: resolved}
}

func TestCheckRepo_MissingGitFails(t *testing.T) {
	r := docRepo(t, false, false, "") // no .git
	checks := checkRepo(r)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check (the .git fail short-circuits); got %d", len(checks))
	}
	if checks[0].status != statusFail {
		t.Errorf("expected FAIL for missing .git; got %s", checks[0].status)
	}
}

func TestCheckRepo_MissingBeadsFailsButContinues(t *testing.T) {
	r := docRepo(t, true, false, "") // .git but no .beads, no hook
	checks := checkRepo(r)
	// Expect: .beads FAIL + post-commit-missing WARN.
	gotFail := false
	gotHookWarn := false
	for _, c := range checks {
		if strings.Contains(c.name, ".beads") && c.status == statusFail {
			gotFail = true
		}
		if strings.Contains(c.name, "post-commit") && c.status == statusWarn {
			gotHookWarn = true
		}
	}
	if !gotFail {
		t.Errorf("expected a .beads FAIL check among %+v", checks)
	}
	if !gotHookWarn {
		t.Errorf("expected a post-commit WARN check among %+v", checks)
	}
}

func TestCheckRepo_WykHookPlainPasses(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init`. line\nexec wyk hook post-commit\n")
	checks := checkRepo(r)
	foundPlain := false
	for _, c := range checks {
		if strings.Contains(c.name, "post-commit hook (wyk)") && c.status == statusPass {
			foundPlain = true
		}
	}
	if !foundPlain {
		t.Errorf("expected a PASS for the plain wyk hook among %+v", checks)
	}
}

func TestCheckRepo_ForeignHookWarns(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# roborev or some other tool\necho ok\n")
	checks := checkRepo(r)
	foundForeign := false
	for _, c := range checks {
		if strings.Contains(c.name, "foreign") && c.status == statusWarn {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Errorf("expected a WARN for the foreign hook among %+v", checks)
	}
}

func TestCheckRepo_ChainedHookMissingPreWykFails(t *testing.T) {
	// Chained marker present in hook body but no .pre-wyk file → FAIL
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init -chain`.\nexec wyk hook post-commit\n")
	checks := checkRepo(r)
	foundFail := false
	for _, c := range checks {
		if strings.Contains(c.name, ".pre-wyk") && c.status == statusFail {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected a FAIL for missing .pre-wyk on chained hook among %+v", checks)
	}
}

func TestCheckRepo_ChainedHookWithPreWykPasses(t *testing.T) {
	r := docRepo(t, true, true, "#!/bin/sh\n# Installed by `wyk init -chain`.\nexec wyk hook post-commit\n")
	// Create the .pre-wyk file.
	preWyk := filepath.Join(r.Path, ".git", "hooks", "post-commit.pre-wyk")
	if err := os.WriteFile(preWyk, []byte("#!/bin/sh\n# preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	checks := checkRepo(r)
	foundChained := false
	for _, c := range checks {
		if strings.Contains(c.name, "chained") && c.status == statusPass {
			foundChained = true
		}
	}
	if !foundChained {
		t.Errorf("expected a PASS for chained hook with .pre-wyk among %+v", checks)
	}
}
