package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// gitInit creates a fresh git repo in a tempdir and returns its root.
// Used by the init tests to exercise findGitDir + write paths.
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// runInitIn runs runInit with the process cwd set to dir; mirrors how
// the real binary discovers .git via `git rev-parse --git-dir`.
func runInitIn(t *testing.T, dir string, args ...string) int {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return runInit(args)
}

func TestInit_InstallsExecutableHook(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("runInit exit %d, want 0", code)
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	st, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook not executable; mode = %v", st.Mode())
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("hook body missing the exec line:\n%s", body)
	}
	if !strings.Contains(string(body), hookMarker) {
		t.Errorf("hook body missing the marker; future reinstall detection won't work:\n%s", body)
	}
}

func TestInit_IdempotentReinstallNoForce(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("first install exit %d", code)
	}
	// Second run without -force should succeed (idempotent) since
	// the existing hook carries our marker.
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("idempotent reinstall exit %d, want 0", code)
	}
}

// writeForeignHook plants a non-wyk post-commit hook in dir's default
// hooks dir and returns its path — the roborev-in-a-beads-repo shape
// that motivated would-you-kindly-7kly.
func writeForeignHook(t *testing.T, dir string) string {
	t.Helper()
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# some other tool's hook\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return hookPath
}

func TestInit_LeavesForeignHookAloneWithoutForce(t *testing.T) {
	dir := gitInit(t)
	hookPath := writeForeignHook(t, dir)

	// Without -force: decline, but exit 0 — wyk chose not to clobber, so
	// nothing failed. It used to exit 64, which aborted the run before
	// registration (would-you-kindly-7kly).
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("expected 0 (declined with a warning) on a foreign hook; got %d", code)
	}
	if body, _ := os.ReadFile(hookPath); strings.Contains(string(body), hookMarker) {
		t.Errorf("foreign hook should be untouched; got:\n%s", body)
	}

	// With -force: replace.
	if code := runInitIn(t, dir, "-force", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("expected 0 with -force; got %d", code)
	}
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("after -force, hook should be the wyk one; got:\n%s", body)
	}
}

// TestInit_RegistersDespiteForeignHook is the regression test for
// would-you-kindly-7kly. Registration is what makes a repo visible to
// `wyk inbox` / the dashboard / the TUI; a foreign post-commit hook used
// to abort init at exit 64 before the registry write, so handoffs filed
// in that repo reported success and no human could ever see them.
// Registration must not be gated on the hook step.
func TestInit_RegistersDespiteForeignHook(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := gitInit(t)
	writeForeignHook(t, dir)

	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-claude-md"); code != 0 {
		t.Fatalf("init exit %d, want 0", code)
	}
	regPath, err := registry.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if !reg.Has(dir) {
		t.Fatalf("repo with a foreign hook was not registered; registry = %+v", reg.Repos)
	}
}

// TestInit_DryRunMatchesRealRunOnForeignHook pins the preview/real-run
// agreement the bug report called out: -dry-run promised registration
// while the real run aborted before it. Both must now register.
func TestInit_DryRunMatchesRealRunOnForeignHook(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := gitInit(t)
	writeForeignHook(t, dir)

	out := captureStdout(t, func() {
		if code := runInitIn(t, dir, "-dry-run", "-skip-bd-init", "-skip-claude-md"); code != 0 {
			t.Errorf("dry-run exit %d, want 0", code)
		}
	})
	if !strings.Contains(out, "would register") {
		t.Fatalf("dry-run should promise registration; got:\n%s", out)
	}
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-claude-md"); code != 0 {
		t.Fatalf("real run exit %d, want 0", code)
	}
	regPath, _ := registry.DefaultPath()
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if !reg.Has(dir) {
		t.Error("dry-run promised registration but the real run did not deliver it")
	}
}

// TestInit_SkipHook registers and enriches without touching git hooks —
// the escape hatch for a repo whose post-commit belongs to another tool.
func TestInit_SkipHook(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := gitInit(t)

	if code := runInitIn(t, dir, "-skip-hook", "-skip-bd-init", "-skip-claude-md"); code != 0 {
		t.Fatalf("init -skip-hook exit %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Errorf("-skip-hook wrote a hook; stat err = %v", err)
	}
	regPath, _ := registry.DefaultPath()
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if !reg.Has(dir) {
		t.Error("-skip-hook should still register the repo")
	}
}

// footerWant is the expected shape of a hook-decline footer. Every field
// is a literal supplied by the caller, never re-derived from the code
// under test — an expectation computed by calling the same helper the
// implementation calls (beadsWorkspaceExists) agrees with it by
// construction and can't fail, which is how the bd-workspace clause went
// unpinned while looking covered.
type footerWant struct {
	// claim is which of the footer's mutually exclusive sentences must
	// appear.
	claim footerClaim
	// bdWorkspace / enrichment: whether the "Set up:" / "Would set up:"
	// enumeration names each component. Only consulted for the two
	// enumerating claims.
	bdWorkspace bool
	enrichment  bool
}

// footerClaim identifies the hook-decline footer's mutually exclusive
// closing sentences. It replaced a bool because that bool couldn't
// express the preview branch at all — assertDeclineFooter fatal-ed on
// "neither claim appeared", which is exactly what a dry run prints, so
// the whole `Would set up:` path was unreachable by any test.
type footerClaim int

const (
	claimVisible       footerClaim = iota // "this repo IS visible to …"
	claimWouldBe                          // "this repo WOULD BE visible to …"
	claimNotRegistered                    // "This repo is NOT registered …"
)

// marker is the substring that identifies the claim in the footer. The
// three are mutually exclusive as substrings, so "exactly one appeared"
// is a meaningful check.
func (c footerClaim) marker() string {
	switch c {
	case claimWouldBe:
		return "WOULD BE visible to"
	case claimNotRegistered:
		return "NOT registered"
	default:
		return "IS visible to"
	}
}

// assertDeclineFooter runs init in dir and checks the hook-decline
// notice against want: which closing sentence it makes (exactly one may
// appear) and, for the two that enumerate, which components they name.
// The enumeration is what stops the footer quietly re-acquiring the
// "everything else is set up" over-claim.
func assertDeclineFooter(t *testing.T, dir string, want footerWant, args ...string) {
	t.Helper()
	_, stderr := captureStdouterr(t, func() {
		if code := runInitIn(t, dir, args...); code != 0 {
			t.Errorf("init %v exit %d, want 0", args, code)
		}
	})
	var seen []footerClaim
	for _, c := range []footerClaim{claimVisible, claimWouldBe, claimNotRegistered} {
		if strings.Contains(stderr, c.marker()) {
			seen = append(seen, c)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("footer must make exactly one claim, matched %d (args %v); got:\n%s",
			len(seen), args, stderr)
	}
	if seen[0] != want.claim {
		t.Errorf("footer claim = %q, want %q (args %v); got:\n%s",
			seen[0].marker(), want.claim.marker(), args, stderr)
	}
	if want.claim == claimNotRegistered {
		return // the absent/unknown branches enumerate nothing
	}
	if got := strings.Contains(stderr, "bd workspace"); got != want.bdWorkspace {
		t.Errorf("footer names bd workspace = %v, want %v (args %v); got:\n%s",
			got, want.bdWorkspace, args, stderr)
	}
	if got := strings.Contains(stderr, "agent enrichment"); got != want.enrichment {
		t.Errorf("footer names agent enrichment = %v, want %v (args %v); got:\n%s",
			got, want.enrichment, args, stderr)
	}
	if !strings.Contains(stderr, "registry entry") {
		t.Errorf("footer should name the registry entry it established; got:\n%s", stderr)
	}
}

// TestInit_ForeignHookWarningTracksRegistration pins the banner's
// honesty in both directions. The decline notice tells the reader "this
// repo IS visible to `wyk inbox`" — the reassurance that makes exiting 0
// safe — so it must never say that about an unregistered repo (an agent
// reads it, files a handoff, no human receives it) and must never deny
// it for a registered one.
//
// Both lies were live on the same path: `wyk doctor -fix` invokes init
// with -skip-register for repos it read straight OUT of the registry, so
// keying the claim off "did this run register" got it backwards there.
func TestInit_ForeignHookWarningTracksRegistration(t *testing.T) {
	t.Run("registered by this run", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := gitInit(t)
		writeForeignHook(t, dir)
		assertDeclineFooter(t, dir, footerWant{claim: claimVisible},
			"-skip-bd-init", "-skip-claude-md")
	})

	t.Run("skip-register, never registered", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := gitInit(t)
		writeForeignHook(t, dir)
		assertDeclineFooter(t, dir, footerWant{claim: claimNotRegistered},
			"-skip-bd-init", "-skip-claude-md", "-skip-register")
	})

	t.Run("full bootstrap names every component", func(t *testing.T) {
		// The positive side of the enumeration: with the enrichment run
		// and .beads on disk, all three components are genuinely
		// established and all three must appear. Without this the other
		// subtests only ever assert the omissions, so a footer that named
		// nothing would pass them all.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		withTempHome(t)
		dir := gitInit(t)
		writeForeignHook(t, dir)
		// -skip-bd-init keeps real `bd` out of the test; the .beads dir
		// makes the stat fallback report the workspace as present anyway.
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		assertDeclineFooter(t, dir,
			footerWant{claim: claimVisible, bdWorkspace: true, enrichment: true},
			"-skip-bd-init")
	})

	t.Run("dry-run preview names what a real run would establish", func(t *testing.T) {
		// The `Would set up:` branch, and the ONLY place the flag halves of
		// bdWorkspace/enrichment are load-bearing: everywhere else the disk
		// checks run after steps 1 and 1.6 have already written, so they
		// subsume the flags. Without this, collapsing either OR to the disk
		// check alone — a plausible-looking simplification, and a no-op on
		// every other covered path — leaves the suite green while
		// `wyk init -dry-run` on a fresh repo silently stops previewing the
		// two components a real run would create.
		//
		// Nothing is on disk here (fresh repo, no .beads, no enrichment) and
		// -dry-run writes nothing, so every named component comes from a flag.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		withTempHome(t)
		dir := gitInit(t)
		writeForeignHook(t, dir)
		assertDeclineFooter(t, dir,
			footerWant{claim: claimWouldBe, bdWorkspace: true, enrichment: true},
			"-dry-run")
	})

	t.Run("skip-claude-md, enrichment already on disk", func(t *testing.T) {
		// The skip flags say what this RUN did, not what the repo HAS.
		// -skip-bd-init already fell back to a .beads stat; -skip-claude-md
		// must fall back the same way, or a repo carrying the current
		// conventions block and the bd-create-guard is told it has neither
		// while its workspace is credited from disk. Every other
		// -skip-claude-md case sets up a repo with no enrichment, so false
		// is correct there and the asymmetry stays invisible without this.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		withTempHome(t)
		dir := gitInit(t)
		writeForeignHook(t, dir)
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A real run first, so both halves of the enrichment land on disk.
		if code := runInitIn(t, dir, "-skip-bd-init"); code != 0 {
			t.Fatalf("setup init exit %d", code)
		}
		assertDeclineFooter(t, dir,
			footerWant{claim: claimVisible, bdWorkspace: true, enrichment: true},
			"-skip-bd-init", "-skip-claude-md")
	})

	t.Run("skip-register, already registered", func(t *testing.T) {
		// The doctor -fix shape: the repo is in the registry and init is
		// told not to touch it. Reporting "NOT registered ... run `wyk
		// registry add`" here is a no-op instruction about a repo that is
		// already fine.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := gitInit(t)
		writeForeignHook(t, dir)
		if code := runInitIn(t, dir, "-skip-bd-init", "-skip-claude-md"); code != 0 {
			t.Fatalf("setup init exit %d", code)
		}
		assertDeclineFooter(t, dir, footerWant{claim: claimVisible},
			"-skip-bd-init", "-skip-claude-md", "-skip-register")
	})

	// -dry-run -skip-register is the one combination that reaches a
	// PAST-tense footer while performing no writes at all: the repo is
	// already registered, so visibility resolves to regVisible rather than
	// the "would register" preview branch. The enumeration must therefore
	// describe what is ON DISK, not what a real run would have created —
	// which needs pinning from both directions. With only the empty case,
	// the on-disk fallbacks are indistinguishable from a hardcoded false
	// and could regress silently.
	t.Run("dry-run + skip-register, nothing on disk", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		withTempHome(t)
		dir := gitInit(t)
		writeForeignHook(t, dir)
		// -skip-claude-md so the setup run seeds no enrichment either.
		if code := runInitIn(t, dir, "-skip-bd-init", "-skip-claude-md"); code != 0 {
			t.Fatalf("setup init exit %d", code)
		}
		assertDeclineFooter(t, dir, footerWant{claim: claimVisible},
			"-dry-run", "-skip-register")
	})

	t.Run("dry-run + skip-register, both already on disk", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		withTempHome(t)
		dir := gitInit(t)
		writeForeignHook(t, dir)
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A real run first, so CLAUDE.md and .claude/settings.json really
		// carry the enrichment the dry run below must recognise.
		if code := runInitIn(t, dir, "-skip-bd-init"); code != 0 {
			t.Fatalf("setup init exit %d", code)
		}
		assertDeclineFooter(t, dir,
			footerWant{claim: claimVisible, bdWorkspace: true, enrichment: true},
			"-dry-run", "-skip-register")
	})
}

// TestInit_StaleHooksPathWarningTracksRegistration: the out-of-repo
// decline is the sibling of the foreign-hook decline and must give the
// same (accurate) footer rather than a different one.
func TestInit_StaleHooksPathWarningTracksRegistration(t *testing.T) {
	t.Run("registered by this run", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := gitInit(t)
		gitConfigSet(t, dir, "core.hooksPath", t.TempDir())
		assertDeclineFooter(t, dir, footerWant{claim: claimVisible},
			"-skip-bd-init", "-skip-claude-md")
	})

	t.Run("skip-register, never registered", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := gitInit(t)
		gitConfigSet(t, dir, "core.hooksPath", t.TempDir())
		assertDeclineFooter(t, dir, footerWant{claim: claimNotRegistered},
			"-skip-bd-init", "-skip-claude-md", "-skip-register")
	})
}

// TestInit_SkipHookRejectsHookFlags: -skip-hook says "don't touch hooks"
// and -chain/-force say "touch them this way"; honouring one silently
// would leave the user guessing which won.
func TestInit_SkipHookRejectsHookFlags(t *testing.T) {
	dir := gitInit(t)
	for _, flag := range []string{"-chain", "-force"} {
		if code := runInitIn(t, dir, "-skip-hook", flag, "-skip-bd-init", "-skip-register"); code != 64 {
			t.Errorf("-skip-hook %s exit %d, want 64", flag, code)
		}
	}
}

func TestInit_ChainPreservesForeignHook(t *testing.T) {
	// -chain is the safer alternative to -force: it preserves the
	// existing hook at .pre-wyk and writes a wrapper that runs both.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# roborev-style hook\necho roborev ran\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("expected 0 with -chain; got %d", code)
	}
	// Original was moved to .pre-wyk
	preserved, err := os.ReadFile(preWykPath)
	if err != nil {
		t.Fatalf("preserved hook missing: %v", err)
	}
	if string(preserved) != string(foreign) {
		t.Errorf(".pre-wyk content mismatch; got:\n%s", preserved)
	}
	// New hook is the chained wrapper
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "post-commit.pre-wyk") {
		t.Errorf("hook should reference the preserved .pre-wyk; got:\n%s", body)
	}
	if !strings.Contains(string(body), "wyk hook post-commit") {
		t.Errorf("hook should still exec wyk; got:\n%s", body)
	}
}

func TestInit_ChainRefusesWhenPreWykAlreadyExists(t *testing.T) {
	// Guard against silently clobbering a previously-preserved hook.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# fresh foreign hook\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preWykPath, []byte("#!/bin/sh\n# previously preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("expected 64 (refuse) when .pre-wyk already exists; got %d", code)
	}
}

func TestHookScripts_BothContainMarker(t *testing.T) {
	// Re-install detection looks for hookMarker in the existing
	// hook's body to decide "this is a wyk hook, skip" vs "this is
	// foreign, refuse/-chain/-force". If a future hook variant
	// (e.g. one that adds a pre-commit dimension) forgets the
	// marker, detection silently breaks — a foreign-hook refusal
	// could fire against wyk's own previously-installed hook. Lock
	// the invariant.
	if !strings.Contains(postCommitHook, hookMarker) {
		t.Errorf("postCommitHook is missing %q — re-install detection will break for this variant", hookMarker)
	}
	if !strings.Contains(chainedPostCommitHook, hookMarker) {
		t.Errorf("chainedPostCommitHook is missing %q — re-install detection will break for this variant", hookMarker)
	}
}

func TestInit_ChainAndForceMutuallyExclusive(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-chain", "-force", "-skip-bd-init", "-skip-register"); code != 64 {
		t.Errorf("expected 64 when -chain and -force are both set; got %d", code)
	}
}

func TestInit_ChainDryRunReflectsRuntimeRefusal(t *testing.T) {
	// Symmetry with TestInit_ChainRefusesWhenPreWykAlreadyExists:
	// when .pre-wyk is already in place, the real -chain run refuses
	// (exit 64). The dry-run shouldn't claim "would chain" — that's
	// false advertising. Should still exit 0 (it's a dry-run) but
	// print the would-refuse message.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	preWykPath := hookPath + ".pre-wyk"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# fresh foreign hook\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preWykPath, []byte("#!/bin/sh\n# previously preserved\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-chain", "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("-chain -dry-run should exit 0 even when .pre-wyk exists; got %d", code)
	}
	// Nothing should have been written.
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "fresh foreign hook") {
		t.Errorf("dry-run modified the foreign hook; got:\n%s", body)
	}
}

func TestInit_UninstallRemovesPlainHook(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook should exist before uninstall: %v", err)
	}

	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d, want 0", code)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("hook should be removed; stat err = %v", err)
	}
}

func TestInit_UninstallRestoresPreWyk(t *testing.T) {
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Foreign hook + -chain install: leaves the foreign script at .pre-wyk.
	foreign := []byte("#!/bin/sh\n# foreign tool\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-chain", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("chain install exit %d", code)
	}

	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d, want 0", code)
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("post-commit missing after uninstall: %v", err)
	}
	if string(body) != string(foreign) {
		t.Errorf("uninstall should restore the foreign hook verbatim; got:\n%s", body)
	}
	preWyk := hookPath + ".pre-wyk"
	if _, err := os.Stat(preWyk); !os.IsNotExist(err) {
		t.Errorf(".pre-wyk should be gone after restore; stat err = %v", err)
	}
}

func TestInit_UninstallRefusesForeignHook(t *testing.T) {
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# foreign\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runInitIn(t, dir, "-uninstall"); code != 64 {
		t.Errorf("uninstall on foreign hook exit %d, want 64", code)
	}
	body, _ := os.ReadFile(hookPath)
	if string(body) != string(foreign) {
		t.Errorf("foreign hook should be left intact; got:\n%s", body)
	}
}

func TestInit_UninstallMissingHookIsNoop(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-uninstall"); code != 0 {
		t.Errorf("uninstall on missing hook should exit 0; got %d", code)
	}
}

func TestInit_UninstallDryRunDoesNotWrite(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	statBefore, _ := os.Stat(hookPath)
	if code := runInitIn(t, dir, "-uninstall", "-dry-run"); code != 0 {
		t.Fatalf("uninstall dry-run exit %d, want 0", code)
	}
	statAfter, err := os.Stat(hookPath)
	if err != nil {
		t.Errorf("dry-run should not remove the hook: %v", err)
	}
	if statBefore.ModTime() != statAfter.ModTime() {
		t.Errorf("dry-run mutated the hook (mod time changed)")
	}
}

func TestInit_DryRunDoesNotWrite(t *testing.T) {
	dir := gitInit(t)
	if code := runInitIn(t, dir, "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("dry-run exit %d, want 0", code)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("dry-run should not write the hook; stat err = %v", err)
	}
}

func TestInit_DryRunAgainstForeignHookReturnsZero(t *testing.T) {
	// -dry-run is observation-only. Even when a foreign hook would
	// cause the real run to refuse (exit 64), the dry-run must
	// preview and exit 0 so scripts like `wyk init -dry-run || …`
	// don't have to special-case the refusal code.
	dir := gitInit(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# some other tool's hook\nexit 0\n")
	if err := os.WriteFile(hookPath, foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runInitIn(t, dir, "-dry-run", "-skip-bd-init", "-skip-register"); code != 0 {
		t.Errorf("-dry-run against foreign hook should exit 0; got %d", code)
	}
	// And: it must not have written.
	body, _ := os.ReadFile(hookPath)
	if string(body) != string(foreign) {
		t.Errorf("-dry-run modified the foreign hook; got:\n%s", body)
	}
}

func TestInit_OutsideRepoFailsCleanly(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if code := runInitIn(t, dir, "-skip-bd-init", "-skip-register"); code != 2 {
		t.Errorf("expected exit 2 outside a repo; got %d", code)
	}
}

func TestRunFixForeignHooks_ChainsForeignSkipsWykAndMissing(t *testing.T) {
	// Three registered repos: one with a foreign hook (chain target),
	// one with wyk's hook (skip), one with no hook (missing — left
	// alone; doctor -fix handles that case). The stubbed chain seam
	// records which repos the fix actually touched so we don't have
	// to invoke real bd.
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	foreign := gitInit(t)
	wykd := gitInit(t)
	missing := gitInit(t)

	forHook := filepath.Join(foreign, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(forHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(forHook, []byte("#!/bin/sh\n# roborev hook\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}
	wykHook := filepath.Join(wykd, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(wykHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(wykHook, []byte("#!/bin/sh\n# "+hookMarker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write wyk hook: %v", err)
	}

	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "foreign", Path: foreign},
		{Name: "wykd", Path: wykd},
		{Name: "missing", Path: missing},
	}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	var chained []string
	prev := chainHookIntoRepo
	chainHookIntoRepo = func(dir string) int {
		chained = append(chained, dir)
		return 0
	}
	defer func() { chainHookIntoRepo = prev }()

	if code := runFixForeignHooks(false); code != 0 {
		t.Errorf("runFixForeignHooks exit %d, want 0", code)
	}
	if len(chained) != 1 || chained[0] != foreign {
		t.Errorf("chainHookIntoRepo called for %v, want [%q] (foreign only)", chained, foreign)
	}
	// wyk hook untouched.
	if body, _ := os.ReadFile(wykHook); !strings.Contains(string(body), hookMarker) {
		t.Errorf("wyk hook was modified; got:\n%s", body)
	}
}

func TestRunFixForeignHooks_DryRunSkipsWrites(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	foreign := gitInit(t)
	forHook := filepath.Join(foreign, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(forHook), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(forHook, []byte("#!/bin/sh\n# foreign\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}
	regPath, _ := registry.DefaultPath()
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "foreign", Path: foreign}}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	called := false
	prev := chainHookIntoRepo
	chainHookIntoRepo = func(_ string) int { called = true; return 0 }
	defer func() { chainHookIntoRepo = prev }()

	if code := runFixForeignHooks(true); code != 0 {
		t.Errorf("dry-run exit %d, want 0", code)
	}
	if called {
		t.Error("dry-run must not call chainHookIntoRepo")
	}
}

func TestRunFixForeignHooks_NoRegistryReturns2(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	stderr := captureStderr(t, func() {
		if code := runFixForeignHooks(false); code != 2 {
			t.Errorf("missing-registry exit %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "no repos registered") && !strings.Contains(stderr, "registry") {
		t.Errorf("stderr should mention the missing registry; got %q", stderr)
	}
}

func TestInit_FixForeignHooksFlagGuards(t *testing.T) {
	// -fix-foreign-hooks is a registry-wide alternate mode; combined
	// with per-repo install flags it must error out with exit 64
	// rather than silently ignore them.
	old := os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devnull
	defer func() {
		os.Stderr = old
		_ = devnull.Close()
	}()
	for _, args := range [][]string{
		{"-fix-foreign-hooks", "-chain"},
		{"-fix-foreign-hooks", "-force"},
		{"-fix-foreign-hooks", "-scan", "/tmp"},
		{"-fix-foreign-hooks", "-uninstall"},
	} {
		if code := runInit(args); code != 64 {
			t.Errorf("runInit %v exit %d, want 64", args, code)
		}
	}
}

func TestInitUsage_LeadsWithHappyPath(t *testing.T) {
	out := captureStderr(t, func() {
		// -h triggers fs.Usage and returns ErrHelp (exit 0).
		if code := runInit([]string{"-h"}); code != 0 {
			t.Errorf("init -h exit %d, want 0", code)
		}
	})
	// The common case must appear BEFORE the alternate modes.
	common := strings.Index(out, "Common case")
	modes := strings.Index(out, "Other modes")
	if common < 0 || modes < 0 || common > modes {
		t.Errorf("usage should lead with the happy path then the modes; got:\n%s", out)
	}
	if !strings.Contains(out, "wyk init ") {
		t.Errorf("usage should show the bare `wyk init` invocation; got:\n%s", out)
	}
}
