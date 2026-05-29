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
	_ = fs.Bool("json", true, "(default; only output format currently supported)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk export [-json]")
		return 64
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

	dump, hadError := collectExport(reg)
	emitExportJSON(os.Stdout, dump)
	if hadError {
		return 1
	}
	return 0
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
func collectExport(reg *registry.Registry) (exportDump, bool) {
	dump := exportDump{ExportedAt: time.Now(), Repos: make([]exportRepo, 0, len(reg.Repos))}
	hadError := false
	for _, r := range reg.Repos {
		row := exportRepo{Name: r.Name, Path: r.Path}
		c := beads.NewClient()
		c.Dir = r.Path
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

// emitExportJSON pretty-prints the dump to w. Indented because
// the common consumer is a human eyeballing jq output or piping
// into a one-off script; the size difference vs. compact JSON is
// negligible compared to issue body content.
func emitExportJSON(w io.Writer, dump exportDump) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(dump)
}
