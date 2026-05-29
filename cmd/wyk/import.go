package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// runImport handles `wyk import`. Reads a `wyk export` JSON dump
// from stdin (or -file) and reconciles each repo entry against
// its registered workspace: open issues whose ID already exists
// are updated in-place; open issues whose ID is missing are
// created fresh (bd assigns a new ID — preserving the original
// is out of scope, see the brief). Closed issues in the dump are
// skipped, since recreating them would clutter the inbox and the
// common consumers (test-fixture seeding, restore-from-backup)
// don't need them.
//
// Exit codes:
//
//	0  import completed (possibly with per-repo skips logged)
//	1  registry / read / write I/O error
//	64 usage error
func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	filePath := fs.String("file", "", "path to JSON dump (default: read from stdin)")
	dryRun := fs.Bool("dry-run", false, "print the plan without touching bd")
	repoName := fs.String("repo", "", "restrict the reconcile to the dump entry with this name (empty = every entry)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk import [-file path] [-dry-run] [-repo name]")
		return 64
	}

	src, closer, err := openImportSource(*filePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk import:", err)
		return 1
	}
	defer closer()

	var dump exportDump
	dec := json.NewDecoder(src)
	if err := dec.Decode(&dump); err != nil {
		fmt.Fprintln(os.Stderr, "wyk import: parse JSON:", err)
		return 1
	}

	if *repoName != "" {
		filtered, err := filterDumpByName(dump, *repoName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk import:", err)
			return 1
		}
		dump = filtered
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk import:", err)
		return 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk import: load registry:", err)
		return 1
	}
	if len(reg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "wyk import: no repos registered. Run `wyk init` in a bd workspace first.")
		return 1
	}

	summary := runImportPlan(reg, dump, *dryRun, defaultImportClient)
	emitImportSummary(os.Stdout, summary, *dryRun)
	if summary.HadError {
		return 1
	}
	return 0
}

// filterDumpByName returns a copy of dump containing only the
// repo whose Name matches. Missing name errors out with a hint
// listing the names that ARE in the dump — same shape as
// filterRegistryByName but operating on the JSON side.
func filterDumpByName(dump exportDump, name string) (exportDump, error) {
	for _, r := range dump.Repos {
		if r.Name == name {
			out := dump
			out.Repos = []exportRepo{r}
			return out, nil
		}
	}
	names := make([]string, len(dump.Repos))
	for i, r := range dump.Repos {
		names[i] = r.Name
	}
	return exportDump{}, fmt.Errorf("no dump entry named %q (names in dump: %v)", name, names)
}

// openImportSource picks stdin or the -file path. Returns a closer
// even for the stdin path so the caller can defer unconditionally.
func openImportSource(path string) (io.Reader, func(), error) {
	if path == "" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path) //nolint:gosec // user-provided path is intentional
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// importClient is the optional Client capability runImportPlan
// needs. Mirrors exportClient's shape so tests can stub a fake
// without spawning bd.
type importClient interface {
	ListAll(ctx context.Context) ([]beads.Issue, error)
	Create(ctx context.Context, opts beads.CreateOptions) (string, error)
	SetPriority(ctx context.Context, id string, priority int) error
	SetAssignee(ctx context.Context, id, assignee string) error
	UpdateDescription(ctx context.Context, id, description string) error
	AddLabel(ctx context.Context, id, label string) error
	RemoveLabel(ctx context.Context, id, label string) error
}

var defaultImportClient = func(dir string) importClient {
	c := beads.NewClient()
	c.Dir = dir
	return c
}

// importRepoResult tracks per-repo accounting so the summary can
// show what changed where. Skipped covers closed-in-dump and any
// rows we declined to touch; Err captures a fatal per-repo
// failure (e.g. the local ListAll itself errored).
type importRepoResult struct {
	Name      string
	Path      string
	Created   []string // local IDs of newly-created issues (or original IDs in dry-run)
	Updated   []string // local IDs we changed
	Unchanged []string // existed and matched — no-op
	Skipped   []string // closed-in-dump or no matching registered repo
	Err       string
}

// importSummary is the top-level accounting emitted to stdout
// (and used in tests). HadError stays true when *any* repo or
// per-issue write failed — the per-row Err strings carry the
// detail.
type importSummary struct {
	StartedAt time.Time
	Repos     []importRepoResult
	HadError  bool
}

// runImportPlan reconciles every registered repo that appears in
// the dump. Repos in the dump with no matching registered entry
// are reported as a skip (so the user knows the dump carried
// data we didn't apply). Sequential walk for the same reason
// collectExport stays serial: bd subprocess concurrency policy.
func runImportPlan(reg *registry.Registry, dump exportDump, dryRun bool, mk func(dir string) importClient) importSummary {
	out := importSummary{StartedAt: time.Now(), Repos: make([]importRepoResult, 0, len(dump.Repos))}
	byName := make(map[string]registry.Repo, len(reg.Repos))
	for _, r := range reg.Repos {
		byName[r.Name] = r
	}

	for _, dumpRepo := range dump.Repos {
		row := importRepoResult{Name: dumpRepo.Name, Path: dumpRepo.Path}
		match, ok := byName[dumpRepo.Name]
		if !ok {
			row.Err = "no registered repo with this name; nothing applied"
			row.Skipped = collectIDs(dumpRepo.Issues)
			out.Repos = append(out.Repos, row)
			out.HadError = true
			continue
		}

		c := mk(match.Path)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		local, err := c.ListAll(ctx)
		cancel()
		if err != nil {
			row.Err = "list-all: " + err.Error()
			row.Skipped = collectIDs(dumpRepo.Issues)
			out.Repos = append(out.Repos, row)
			out.HadError = true
			continue
		}
		localByID := make(map[string]beads.Issue, len(local))
		for _, li := range local {
			localByID[li.ID] = li
		}

		for _, in := range dumpRepo.Issues {
			if in.Status == "closed" {
				row.Skipped = append(row.Skipped, in.ID)
				continue
			}
			if existing, ok := localByID[in.ID]; ok {
				changed, perr := applyImportUpdate(c, existing, in, dryRun)
				// Count the issue in Updated whenever at least one
				// write fired, even on partial failure, so the
				// summary's create+update+unchanged+skipped still
				// adds up to the input total. The error string
				// names the ID separately so an operator can
				// reconcile per-row.
				switch {
				case changed:
					row.Updated = append(row.Updated, in.ID)
				case perr == nil:
					row.Unchanged = append(row.Unchanged, in.ID)
				}
				if perr != nil {
					row.Err = appendErr(row.Err, in.ID+": "+perr.Error())
					out.HadError = true
				}
				continue
			}
			newID, perr := applyImportCreate(c, in, dryRun)
			if perr != nil {
				row.Err = appendErr(row.Err, in.ID+": "+perr.Error())
				out.HadError = true
				continue
			}
			row.Created = append(row.Created, newID)
		}
		out.Repos = append(out.Repos, row)
	}
	sort.Slice(out.Repos, func(i, j int) bool { return out.Repos[i].Name < out.Repos[j].Name })
	return out
}

// applyImportUpdate diff-applies the dump issue onto the local
// one. Returns true if at least one bd write fired (or *would*
// fire in dry-run). The fields compared are intentionally the
// content fields a backup/restore cares about: priority,
// assignee/owner, description, labels. Status transitions are
// skipped — re-opening a closed issue or closing an open one
// belongs to the human reviewing the import diff, not the
// import itself.
func applyImportUpdate(c importClient, existing, in beads.Issue, dryRun bool) (bool, error) {
	changed := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if existing.Priority != in.Priority {
		if !dryRun {
			if err := c.SetPriority(ctx, in.ID, in.Priority); err != nil {
				return changed, err
			}
		}
		changed = true
	}
	if existing.Owner != in.Owner && in.Owner != "" {
		if !dryRun {
			if err := c.SetAssignee(ctx, in.ID, in.Owner); err != nil {
				return changed, err
			}
		}
		changed = true
	}
	// Guard the description diff the same way as owner: an empty
	// value in the dump represents "the source didn't carry one"
	// (e.g. a freshly-created bd issue without a body), not "clear
	// the local description." Without this guard, a restore from a
	// dump that pre-dates a description being added would silently
	// wipe it.
	if existing.Description != in.Description && in.Description != "" {
		if !dryRun {
			if err := c.UpdateDescription(ctx, in.ID, in.Description); err != nil {
				return changed, err
			}
		}
		changed = true
	}
	adds, removes := labelDiff(existing.Labels, in.Labels)
	for _, l := range adds {
		if !dryRun {
			if err := c.AddLabel(ctx, in.ID, l); err != nil {
				return changed, err
			}
		}
		changed = true
	}
	for _, l := range removes {
		if !dryRun {
			if err := c.RemoveLabel(ctx, in.ID, l); err != nil {
				return changed, err
			}
		}
		changed = true
	}
	return changed, nil
}

// applyImportCreate creates a new bd issue mirroring the dump
// row. Returns the new local ID (or the original dump ID prefixed
// with "would-create:" in dry-run mode, so the summary still
// names something concrete). bd's create-time fields are a
// strict subset of an issue's full surface (no description, no
// status); we fix description as a follow-up SetDescription when
// the dump carried one, so the round-trip captures it.
func applyImportCreate(c importClient, in beads.Issue, dryRun bool) (string, error) {
	if dryRun {
		return "would-create:" + in.ID, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := beads.CreateOptions{
		Title:     in.Title,
		Labels:    in.Labels,
		Priority:  strconv.Itoa(in.Priority),
		IssueType: in.IssueType,
		Assignee:  in.Owner,
	}
	newID, err := c.Create(ctx, opts)
	if err != nil {
		return "", err
	}
	if in.Description != "" {
		if err := c.UpdateDescription(ctx, newID, in.Description); err != nil {
			return newID, err
		}
	}
	return newID, nil
}

// labelDiff returns (adds, removes): labels present in `want`
// but missing from `have`, and labels present in `have` but
// missing from `want`. Order in the returned slices is stable
// (sorted) so the summary output is deterministic.
func labelDiff(have, want []string) (adds, removes []string) {
	haveSet := make(map[string]struct{}, len(have))
	for _, l := range have {
		haveSet[l] = struct{}{}
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, l := range want {
		wantSet[l] = struct{}{}
	}
	for l := range wantSet {
		if _, ok := haveSet[l]; !ok {
			adds = append(adds, l)
		}
	}
	for l := range haveSet {
		if _, ok := wantSet[l]; !ok {
			removes = append(removes, l)
		}
	}
	sort.Strings(adds)
	sort.Strings(removes)
	return adds, removes
}

func collectIDs(issues []beads.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, i := range issues {
		ids = append(ids, i.ID)
	}
	return ids
}

func appendErr(prev, next string) string {
	if prev == "" {
		return next
	}
	return prev + "; " + next
}

// emitImportSummary prints a human-readable per-repo plan: counts
// first (so a glance shows the magnitude), then the IDs grouped
// by action (so a careful reader can diff against expectations).
// Dry-run prepends a banner so the output can't be mistaken for
// an applied import.
func emitImportSummary(w io.Writer, s importSummary, dryRun bool) {
	if dryRun {
		fmt.Fprintln(w, "DRY-RUN: no bd writes performed")
	}
	for _, r := range s.Repos {
		fmt.Fprintf(w, "%s (%s): created=%d updated=%d unchanged=%d skipped=%d\n",
			r.Name, r.Path, len(r.Created), len(r.Updated), len(r.Unchanged), len(r.Skipped))
		if r.Err != "" {
			fmt.Fprintln(w, "  error:", r.Err)
		}
		if len(r.Created) > 0 {
			fmt.Fprintln(w, "  created:", r.Created)
		}
		if len(r.Updated) > 0 {
			fmt.Fprintln(w, "  updated:", r.Updated)
		}
	}
}
