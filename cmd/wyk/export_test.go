package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

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
