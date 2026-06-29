package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/jimbottle/would-you-kindly/internal/registry"
	"github.com/jimbottle/would-you-kindly/internal/wykconfig"
)

// scopeEnvVar overrides the configured default_scope for a single run.
// Mirrors WYK_AGENT_IDENTITY's relationship to the identity flag: an
// ambient env value the per-run flag (-all) still beats.
const scopeEnvVar = "WYK_DEFAULT_SCOPE"

// errScopeConflict is returned by reposToQuery when more than one of
// -C / -repo / -all is supplied. A distinct sentinel so the caller can
// map it to a usage exit (64) while a registry / config load failure
// maps to the generic error exit (1).
var errScopeConflict = errors.New("-C, -repo, and -all are mutually exclusive")

// scopeErrExit maps a reposToQuery error to the right process exit
// code: 64 (usage) for a scope conflict or an invalid scope value the
// user supplied, 1 (generic) for a registry / config load or I/O
// failure. Centralised so all six commands classify identically.
func scopeErrExit(err error) int {
	if errors.Is(err, errScopeConflict) || errors.Is(err, wykconfig.ErrInvalidScope) {
		return 64
	}
	return 1
}

// resolveDefaultScope picks the effective repo scope for the multi-repo
// commands when no per-run scope flag forces the choice. Precedence:
// $WYK_DEFAULT_SCOPE (highest), then config.json's default_scope, then
// the built-in default "all". A set-but-invalid value (env or config)
// is a hard error rather than a silent fallthrough — mirroring
// resolveIdentity — so a typo can't quietly change which repos a
// command queries. An unsupported / corrupt config.json surfaces as an
// error too: the command refuses rather than guessing.
func resolveDefaultScope() (string, error) {
	if v := os.Getenv(scopeEnvVar); v != "" {
		if err := wykconfig.ValidateScope(v); err != nil {
			return "", fmt.Errorf("$%s: %w", scopeEnvVar, err)
		}
		return v, nil
	}
	path, err := wykconfig.DefaultPath()
	if err != nil {
		return "", err
	}
	cfg, err := wykconfig.Load(path)
	if err != nil {
		return "", err
	}
	if cfg.DefaultScope != "" {
		if err := wykconfig.ValidateScope(cfg.DefaultScope); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		return cfg.DefaultScope, nil
	}
	return wykconfig.ScopeAll, nil
}

// reposToQuery resolves the set of repos a multi-repo command should
// query, returning one registry.Repo per workspace so each caller can
// keep building its own beads.Client list. It is the single scope
// resolver shared by inbox / stats / activity / dashboard / depgraph /
// export. Precedence (highest first):
//
//  1. -C <dir>   → a single synthetic source at dir (bypasses the
//     registry entirely; Name is empty so the caller renders it
//     un-prefixed, matching the historical single-repo behavior).
//  2. -repo name → that one registry entry (by display name).
//  3. -all       → every registered repo, ignoring the configured scope.
//     4/5/6. otherwise the effective default scope (env > config > "all"):
//     "all" → every registered repo; "cwd" → the repo containing the
//     current directory.
//
// -C / -repo / -all are mutually exclusive (errScopeConflict). An empty
// registry collapses to a synthetic cwd source regardless of scope — bd
// auto-discovers .beads upward, so an unregistered-but-valid workspace
// still reports its own inbox. Under "cwd" scope a registry hit scopes
// to that single (correctly-labelled) entry; a miss falls back to the
// same synthetic cwd source.
func reposToQuery(dir, repoName string, allFlag bool) ([]registry.Repo, error) {
	n := 0
	if dir != "" {
		n++
	}
	if repoName != "" {
		n++
	}
	if allFlag {
		n++
	}
	if n > 1 {
		return nil, errScopeConflict
	}

	// -C bypasses the registry: a single synthetic source at the dir.
	if dir != "" {
		return []registry.Repo{{Path: dir}}, nil
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return nil, err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, err
	}

	// -repo selects one named entry.
	if repoName != "" {
		filtered, err := filterRegistryByName(reg, repoName)
		if err != nil {
			return nil, err
		}
		return filtered.Repos, nil
	}

	// Resolve the effective scope BEFORE the empty-registry shortcut so a
	// set-but-invalid scope (env or config) is a hard error even when no
	// repos are registered — the brief's "invalid scope is never silently
	// tolerated" guarantee. -all forces "all" and skips the lookup.
	scope := wykconfig.ScopeAll
	if !allFlag {
		s, err := resolveDefaultScope()
		if err != nil {
			return nil, err
		}
		scope = s
	}

	// Nothing registered: behave like the pre-registry fallback and run
	// against cwd, whichever scope is configured.
	if len(reg.Repos) == 0 {
		return []registry.Repo{cwdSyntheticRepo()}, nil
	}

	if scope == wykconfig.ScopeCwd {
		if cwd, err := os.Getwd(); err == nil {
			if repo, ok := reg.RepoForDir(cwd); ok {
				return []registry.Repo{repo}, nil
			}
		}
		// cwd is in no registered repo (or unreadable): a synthetic
		// source at cwd so an unregistered-but-valid bd workspace still
		// reports for itself rather than silently returning the whole
		// registry.
		return []registry.Repo{cwdSyntheticRepo()}, nil
	}

	return reg.Repos, nil
}

// cwdSyntheticRepo is the un-named, cwd-rooted Repo used for the -C
// fallback paths (empty registry, cwd-scope miss). An empty Path on a
// getwd failure leaves the client's Dir unset, so bd inherits the
// process working directory — the same effect, just without an
// explicit path to label.
func cwdSyntheticRepo() registry.Repo {
	if cwd, err := os.Getwd(); err == nil {
		return registry.Repo{Path: cwd}
	}
	return registry.Repo{}
}
