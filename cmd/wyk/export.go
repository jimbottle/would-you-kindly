package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// runExport walks every registered bd workspace and emits a JSON
// dump suitable for external tooling: each repo's full issue
// list (open + closed, via `bd list --all`) plus the IDs of
// issues `bd ready` would surface today. Useful as a data feed
// for ad-hoc analyses, test-fixture seeding, or piping into jq.
//
// Exit codes:
//
//	0  dump emitted
//	1  registry / per-repo I/O error (partial output still emitted)
//	64 usage error
func runExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	// -since accepts any duration parsable by time.ParseDuration
	// ("24h", "7d" via 168h, "30m"). Empty (the default) emits
	// the full dump, matching the historical behavior.
	since := fs.String("since", "", "filter issues to those updated within this duration (e.g. 24h, 168h)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk export [-since 24h]")
		return 64
	}
	var cutoff time.Time
	if *since != "" {
		d, err := time.ParseDuration(*since)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk export: invalid -since:", err)
			return 64
		}
		cutoff = time.Now().Add(-d)
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk export:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk export: load registry:", err)
		return 1
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk export: no repos registered. Run `wyk init` in a bd workspace first.")
		return 1
	}

	dump, hadError := collectExport(reg, defaultExportClient)
	if !cutoff.IsZero() {
		dump = filterDumpSince(dump, cutoff)
	}
	emitExportJSON(os.Stdout, dump)
	if hadError {
		return 1
	}
	return 0
}

// exportClient is the optional Client capability collectExport
// needs. Production wires the real beads.NewClient via
// defaultExportClient; tests inject a stub so the walk + error-
// folding can be exercised without a real bd binary.
type exportClient interface {
	ListAll(ctx context.Context) ([]beads.Issue, error)
	Ready(ctx context.Context) ([]beads.Issue, error)
}

// defaultExportClient is runExport's production factory: build
// a real beads.Client pointed at the repo's path. Kept as a
// package-level var so a future probe / debug flag can swap it
// without touching collectExport's signature.
var defaultExportClient = func(dir string) exportClient {
	c := beads.NewClient()
	c.Dir = dir
	return c
}

// exportRepo captures one workspace's full bd state plus the
// ready-IDs derived from `bd ready` so a downstream tool can
// reconstruct the agent's actionable view without re-querying.
// Per-repo errors are folded into the Err field rather than
// aborting the walk — a single broken repo shouldn't make the
// whole export disappear.
type exportRepo struct {
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Issues   []beads.Issue `json:"issues"`
	ReadyIDs []string      `json:"ready_ids"`
	Err      string        `json:"error,omitempty"`
}

// exportDump is the top-level object emitted to stdout. Carries
// the export timestamp so a downstream pipeline can stamp its
// data feed alongside the issue rows.
type exportDump struct {
	ExportedAt time.Time    `json:"exported_at"`
	Repos      []exportRepo `json:"repos"`
}

// collectExport walks the registry sequentially (matches the
// dashboard's bd-subprocess concurrency policy — no parallel
// fanout heating the CPU). Issues come from `bd list --all`
// (open + closed); ReadyIDs come from `bd ready` so the
// blocker-aware view is preserved without a downstream tool
// having to reimplement the bd ready rules. Both calls run
// before recording the row so a partial failure (e.g. `bd ready`
// times out but `bd list --all` succeeds) still emits the issue
// list with an empty ReadyIDs slice and an error string.
func collectExport(reg *registry.Registry, mk func(dir string) exportClient) (exportDump, bool) {
	dump := exportDump{ExportedAt: time.Now(), Repos: make([]exportRepo, 0, len(reg.Repos))}
	hadError := false
	for _, r := range reg.Repos {
		row := exportRepo{Name: r.Name, Path: r.Path}
		c := mk(r.Path)
		// list --all
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		issues, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			row.Err = "list-all: " + err.Error()
			hadError = true
		} else {
			row.Issues = issues
		}
		// ready (separately so a `bd ready` failure doesn't
		// swallow the issue list)
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		ready, err := c.Ready(ctx)
		cancel()
		if err != nil {
			// Append to the existing error rather than overwrite —
			// a fully-broken repo gets both call's diagnostics.
			if row.Err != "" {
				row.Err += "; ready: " + err.Error()
			} else {
				row.Err = "ready: " + err.Error()
			}
			hadError = true
		} else {
			row.ReadyIDs = make([]string, 0, len(ready))
			for _, i := range ready {
				row.ReadyIDs = append(row.ReadyIDs, i.ID)
			}
		}
		dump.Repos = append(dump.Repos, row)
	}
	sort.Slice(dump.Repos, func(i, j int) bool { return dump.Repos[i].Name < dump.Repos[j].Name })
	return dump, hadError
}

// filterDumpSince trims each repo's Issues slice to issues whose
// UpdatedAt is at or after the cutoff. ReadyIDs is left intact —
// "ready" is a present-tense view of the workspace and doesn't
// have a temporal axis. Repos with no matching issues stay in
// the dump (empty Issues) so a downstream tool can tell which
// repos had no activity vs. weren't queried.
func filterDumpSince(dump exportDump, cutoff time.Time) exportDump {
	out := dump
	out.Repos = make([]exportRepo, len(dump.Repos))
	for i, r := range dump.Repos {
		filtered := make([]beads.Issue, 0, len(r.Issues))
		for _, issue := range r.Issues {
			if !issue.UpdatedAt.Before(cutoff) {
				filtered = append(filtered, issue)
			}
		}
		r.Issues = filtered
		out.Repos[i] = r
	}
	return out
}

// emitExportJSON pretty-prints the dump to w. Indented because
// the common consumer is a human eyeballing jq output or piping
// into a one-off script; the size difference vs. compact JSON is
// negligible compared to issue body content.
func emitExportJSON(w io.Writer, dump exportDump) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(dump)
}
