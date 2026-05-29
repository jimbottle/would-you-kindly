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
	emitExportJSON(&buf, dump)

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
