package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// stubExportClient is a minimal exportClient impl recorded
// per-call so collectExport tests can drive the list-ok/ready-
// fail / list-fail/ready-ok / both-fail error-folding branches.
type stubExportClient struct {
	listIssues []beads.Issue
	listErr    error
	readyIssue []beads.Issue
	readyErr   error
}

func (s *stubExportClient) ListAll(_ context.Context) ([]beads.Issue, error) {
	return s.listIssues, s.listErr
}
func (s *stubExportClient) Ready(_ context.Context) ([]beads.Issue, error) {
	return s.readyIssue, s.readyErr
}

func TestCollectExport_FoldsErrorsAndPreservesPartial(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{
		{Name: "ok", Path: "/tmp/ok"},
		{Name: "list-broken", Path: "/tmp/lb"},
		{Name: "ready-broken", Path: "/tmp/rb"},
		{Name: "both-broken", Path: "/tmp/bb"},
	}}
	stubs := map[string]*stubExportClient{
		"/tmp/ok": {
			listIssues: []beads.Issue{{ID: "a-1"}},
			readyIssue: []beads.Issue{{ID: "a-1"}},
		},
		"/tmp/lb": {
			listErr:    errors.New("list boom"),
			readyIssue: []beads.Issue{{ID: "b-2"}},
		},
		"/tmp/rb": {
			listIssues: []beads.Issue{{ID: "c-3"}},
			readyErr:   errors.New("ready boom"),
		},
		"/tmp/bb": {
			listErr:  errors.New("list boom"),
			readyErr: errors.New("ready boom"),
		},
	}
	mk := func(dir string) exportClient { return stubs[dir] }

	dump, hadError := collectExport(reg, mk)
	if !hadError {
		t.Errorf("hadError should be true when any sub failed")
	}
	if len(dump.Repos) != 4 {
		t.Fatalf("expected 4 repos in output; got %d", len(dump.Repos))
	}

	// Sort produces alphabetical order: both-broken, list-broken,
	// ok, ready-broken.
	byName := map[string]exportRepo{}
	for _, r := range dump.Repos {
		byName[r.Name] = r
	}

	// ok: both calls succeed, no error.
	if r := byName["ok"]; r.Err != "" || len(r.Issues) != 1 || r.ReadyIDs[0] != "a-1" {
		t.Errorf("ok row: got %+v", r)
	}
	// list-broken: list errors → Err carries 'list-all:' prefix;
	// ready still populates ReadyIDs.
	if r := byName["list-broken"]; r.Err == "" ||
		!bytes.Contains([]byte(r.Err), []byte("list-all: list boom")) ||
		len(r.ReadyIDs) != 1 {
		t.Errorf("list-broken row: got %+v", r)
	}
	// ready-broken: list succeeds; ready errors → Err carries
	// 'ready:' prefix.
	if r := byName["ready-broken"]; r.Err == "" ||
		!bytes.Contains([]byte(r.Err), []byte("ready: ready boom")) ||
		len(r.Issues) != 1 {
		t.Errorf("ready-broken row: got %+v", r)
	}
	// both-broken: Err carries BOTH prefixes joined with `; `.
	if r := byName["both-broken"]; !bytes.Contains([]byte(r.Err), []byte("list-all: list boom")) ||
		!bytes.Contains([]byte(r.Err), []byte("; ready: ready boom")) {
		t.Errorf("both-broken row should fold both errors; got %+v", r)
	}

	// Sort: dump.Repos[0] must be the alphabetically first name.
	if dump.Repos[0].Name != "both-broken" {
		t.Errorf("repos should be alphabetical; first name = %q", dump.Repos[0].Name)
	}
}

func TestFilterDumpSince_KeepsRecentDropsOlder(t *testing.T) {
	cutoff := time.Now().Add(-1 * time.Hour)
	dump := exportDump{Repos: []exportRepo{{
		Name: "r",
		Issues: []beads.Issue{
			{ID: "old", UpdatedAt: cutoff.Add(-1 * time.Minute)},
			{ID: "edge", UpdatedAt: cutoff},
			{ID: "fresh", UpdatedAt: cutoff.Add(1 * time.Minute)},
		},
		ReadyIDs: []string{"old", "edge", "fresh"},
	}}}
	got := filterDumpSince(dump, cutoff)
	ids := make([]string, len(got.Repos[0].Issues))
	for i, x := range got.Repos[0].Issues {
		ids[i] = x.ID
	}
	if len(ids) != 2 || ids[0] != "edge" || ids[1] != "fresh" {
		t.Errorf("filtered IDs=%v, want [edge fresh]", ids)
	}
	// ReadyIDs left intact — present-tense view, no time axis.
	if len(got.Repos[0].ReadyIDs) != 3 {
		t.Errorf("ReadyIDs should not be filtered; got %v", got.Repos[0].ReadyIDs)
	}
}

func TestFilterDumpSince_EmptyRepoStaysInDump(t *testing.T) {
	// A repo with zero matching issues must remain in the output
	// (with an empty Issues slice) so a downstream tool can tell
	// "no recent activity" apart from "wasn't queried."
	cutoff := time.Now().Add(-1 * time.Hour)
	dump := exportDump{Repos: []exportRepo{
		{Name: "r1", Issues: []beads.Issue{{ID: "old", UpdatedAt: cutoff.Add(-time.Hour)}}},
		{Name: "r2", Issues: []beads.Issue{{ID: "fresh", UpdatedAt: cutoff.Add(time.Hour)}}},
	}}
	got := filterDumpSince(dump, cutoff)
	if len(got.Repos) != 2 {
		t.Fatalf("repo count changed: got %d, want 2", len(got.Repos))
	}
	if len(got.Repos[0].Issues) != 0 {
		t.Errorf("r1 should be empty; got %d issues", len(got.Repos[0].Issues))
	}
	if len(got.Repos[1].Issues) != 1 {
		t.Errorf("r2 should keep one issue; got %d", len(got.Repos[1].Issues))
	}
}

func TestEmitExportJSON_ShapeAndIndentation(t *testing.T) {
	dump := exportDump{
		ExportedAt: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		Repos: []exportRepo{
			{
				Name:     "alpha",
				Path:     "/tmp/alpha",
				Issues:   []beads.Issue{{ID: "a-1", Title: "rotate"}},
				ReadyIDs: []string{"a-1"},
			},
			{
				Name: "broken",
				Path: "/tmp/broken",
				Err:  "list-all: bd not on PATH",
			},
		},
	}
	var buf bytes.Buffer
	emitExportJSON(&buf, dump, false)

	// Re-decode to confirm the shape round-trips.
	var got exportDump
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if !got.ExportedAt.Equal(dump.ExportedAt) {
		t.Errorf("ExportedAt round-trip mismatch: got %v, want %v", got.ExportedAt, dump.ExportedAt)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("expected 2 repo rows; got %d", len(got.Repos))
	}
	if got.Repos[0].Name != "alpha" || got.Repos[0].Issues[0].ID != "a-1" {
		t.Errorf("alpha row should contain a-1; got %+v", got.Repos[0])
	}
	if got.Repos[0].ReadyIDs[0] != "a-1" {
		t.Errorf("ReadyIDs should be preserved; got %v", got.Repos[0].ReadyIDs)
	}
	if got.Repos[1].Err == "" {
		t.Errorf("errored row should preserve its Err field; got %+v", got.Repos[1])
	}
	// Pretty-printed: at least one indented line.
	if !bytes.Contains(buf.Bytes(), []byte("  \"")) {
		t.Errorf("output should be indented JSON; got %s", buf.String())
	}
}

func TestEmitExportJSON_CompactSkipsIndentation(t *testing.T) {
	dump := exportDump{
		ExportedAt: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
		Repos:      []exportRepo{{Name: "r"}},
	}
	var buf bytes.Buffer
	emitExportJSON(&buf, dump, true)
	// Compact output must not contain the two-space indent prefix.
	if bytes.Contains(buf.Bytes(), []byte("  \"")) {
		t.Errorf("compact output should NOT be indented; got %s", buf.String())
	}
	// But the output is still valid JSON.
	var got exportDump
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Errorf("compact output should still parse; %v\nraw: %s", err, buf.String())
	}
}
