package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// seedRegistry writes a repos.json with the given repos under the
// current XDG_CONFIG_HOME. Caller must t.Setenv XDG_CONFIG_HOME first.
func seedRegistry(t *testing.T, repos ...registry.Repo) {
	t.Helper()
	path, err := registry.DefaultPath()
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}
	reg := &registry.Registry{Repos: repos}
	if err := reg.Save(path); err != nil {
		t.Fatalf("save registry: %v", err)
	}
}

// seedConfig writes a config.json with the given default_scope.
func seedConfig(t *testing.T, scope string) {
	t.Helper()
	path, err := wykconfig.DefaultPath()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if err := wykconfig.Save(path, wykconfig.Config{DefaultScope: scope}); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func TestResolveDefaultScope_Precedence(t *testing.T) {
	t.Run("nothing set defaults to all", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(scopeEnvVar, "")
		got, err := resolveDefaultScope()
		if err != nil || got != wykconfig.ScopeAll {
			t.Fatalf("got (%q, %v), want (all, nil)", got, err)
		}
	})
	t.Run("config wins over default", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(scopeEnvVar, "")
		seedConfig(t, wykconfig.ScopeCwd)
		got, err := resolveDefaultScope()
		if err != nil || got != wykconfig.ScopeCwd {
			t.Fatalf("got (%q, %v), want (cwd, nil)", got, err)
		}
	})
	t.Run("env wins over config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		seedConfig(t, wykconfig.ScopeCwd)
		t.Setenv(scopeEnvVar, wykconfig.ScopeAll)
		got, err := resolveDefaultScope()
		if err != nil || got != wykconfig.ScopeAll {
			t.Fatalf("got (%q, %v), want (all, nil)", got, err)
		}
	})
	t.Run("invalid env errors", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(scopeEnvVar, "bogus")
		if _, err := resolveDefaultScope(); err == nil {
			t.Fatal("want error for invalid env scope")
		}
	})
	t.Run("invalid config errors", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(scopeEnvVar, "")
		// Bypass ValidateScope on write by hand-editing the file.
		path, _ := wykconfig.DefaultPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"version":1,"default_scope":"bogus"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveDefaultScope(); err == nil {
			t.Fatal("want error for invalid config scope")
		}
	})
}

func TestReposToQuery_MutualExclusion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name      string
		dir, repo string
		all       bool
	}{
		{"C+repo", "/x", "y", false},
		{"C+all", "/x", "", true},
		{"repo+all", "", "y", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reposToQuery(tc.dir, tc.repo, tc.all); err != errScopeConflict {
				t.Fatalf("err = %v, want errScopeConflict", err)
			}
		})
	}
}

func TestReposToQuery_CBypassesRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repos, err := reposToQuery("/some/dir", "", false)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 1 || repos[0].Path != "/some/dir" || repos[0].Name != "" {
		t.Fatalf("got %+v, want one un-named repo at /some/dir", repos)
	}
}

func TestReposToQuery_RepoByName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedRegistry(t,
		registry.Repo{Name: "alpha", Path: "/tmp/a"},
		registry.Repo{Name: "beta", Path: "/tmp/b"},
	)
	repos, err := reposToQuery("", "beta", false)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "beta" {
		t.Fatalf("got %+v, want [beta]", repos)
	}
}

func TestReposToQuery_AllIgnoresCwdScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	seedRegistry(t,
		registry.Repo{Name: "alpha", Path: "/tmp/a"},
		registry.Repo{Name: "beta", Path: "/tmp/b"},
	)
	seedConfig(t, wykconfig.ScopeCwd) // would normally narrow to cwd
	repos, err := reposToQuery("", "", true)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2 (all)", len(repos))
	}
}

func TestReposToQuery_DefaultAllReturnsEveryRepo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	seedRegistry(t,
		registry.Repo{Name: "alpha", Path: "/tmp/a"},
		registry.Repo{Name: "beta", Path: "/tmp/b"},
	)
	repos, err := reposToQuery("", "", false) // no config → default all
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
	}
}

func TestReposToQuery_CwdHit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	repoDir := t.TempDir()
	seedRegistry(t,
		registry.Repo{Name: "here", Path: repoDir},
		registry.Repo{Name: "elsewhere", Path: "/tmp/elsewhere"},
	)
	seedConfig(t, wykconfig.ScopeCwd)
	t.Chdir(repoDir)
	repos, err := reposToQuery("", "", false)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "here" {
		t.Fatalf("got %+v, want [here]", repos)
	}
}

func TestReposToQuery_CwdMissFallsBackToSynthetic(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	seedRegistry(t, registry.Repo{Name: "elsewhere", Path: "/tmp/elsewhere-unrelated"})
	seedConfig(t, wykconfig.ScopeCwd)
	unrelated := t.TempDir()
	t.Chdir(unrelated)
	repos, err := reposToQuery("", "", false)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	// cwd is in no registered repo → a single synthetic, un-named source.
	if len(repos) != 1 || repos[0].Name != "" {
		t.Fatalf("got %+v, want one un-named synthetic repo", repos)
	}
}

// TestReposToQuery_EmptyRegistryFallsBackToCwd pins the unified
// empty-registry behavior that ALL six multi-repo commands now share
// (activity/dashboard/depgraph/export previously hard-errored with a
// "run wyk init" hint; routing them through reposToQuery intentionally
// gives them inbox/stats's long-standing cwd fallback instead). This is
// the resolver-level guarantee behind that change.
func TestReposToQuery_EmptyRegistryFallsBackToCwd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	// No registry written at all.
	repos, err := reposToQuery("", "", false)
	if err != nil {
		t.Fatalf("reposToQuery: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "" {
		t.Fatalf("got %+v, want one un-named synthetic repo", repos)
	}
}

// TestReposToQuery_RepoWithEmptyRegistryErrors pins that -repo is honored
// strictly even when the registry is empty: it errors ("no registered
// repo named …") rather than silently ignoring -repo and falling back to
// cwd. The -repo filter deliberately runs before the empty-registry
// shortcut — an explicit -repo should never be silently dropped.
func TestReposToQuery_RepoWithEmptyRegistryErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(scopeEnvVar, "")
	if _, err := reposToQuery("", "ghost", false); err == nil {
		t.Fatal("want error for -repo against an empty registry, got nil")
	}
}
