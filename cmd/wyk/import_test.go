package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// fakeImportClient is the in-memory bd stand-in. It records every
// mutation so tests can assert which writes fired and in what
// shape, and lets a test inject failure modes by setting ListErr
// / CreateErr / etc.
type fakeImportClient struct {
	existing  []beads.Issue
	ListErr   error
	CreateErr error
	SetPriCalls,
	SetAssCalls,
	SetDescCalls,
	AddLabelCalls,
	RemoveLabelCalls,
	CreateCalls int
	LastCreate beads.CreateOptions
}

func (f *fakeImportClient) ListAll(_ context.Context) ([]beads.Issue, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return f.existing, nil
}
func (f *fakeImportClient) Create(_ context.Context, opts beads.CreateOptions) (string, error) {
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	f.CreateCalls++
	f.LastCreate = opts
	return "new-id-" + opts.Title, nil
}
func (f *fakeImportClient) SetPriority(_ context.Context, _ string, _ int) error {
	f.SetPriCalls++
	return nil
}
func (f *fakeImportClient) SetAssignee(_ context.Context, _, _ string) error {
	f.SetAssCalls++
	return nil
}
func (f *fakeImportClient) UpdateDescription(_ context.Context, _, _ string) error {
	f.SetDescCalls++
	return nil
}
func (f *fakeImportClient) AddLabel(_ context.Context, _, _ string) error {
	f.AddLabelCalls++
	return nil
}
func (f *fakeImportClient) RemoveLabel(_ context.Context, _, _ string) error {
	f.RemoveLabelCalls++
	return nil
}

func TestRunImportPlan_UpdatesExistingAndCreatesMissing(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name: "repo-a",
		Issues: []beads.Issue{
			{ID: "a-1", Title: "exists", Priority: 1, Owner: "alice", Description: "new body", Status: "open"},
			{ID: "a-2", Title: "missing", Priority: 3, Owner: "bob", Status: "open", IssueType: "task"},
			{ID: "a-3", Title: "closed-skipped", Status: "closed"},
		},
	}}}

	fake := &fakeImportClient{
		existing: []beads.Issue{{ID: "a-1", Priority: 2, Owner: "alice", Description: "old body"}},
	}
	mk := func(_ string) importClient { return fake }

	s := runImportPlan(reg, dump, false, mk)
	if s.HadError {
		t.Fatalf("HadError=true; want false (per-repo errors=%v)", s.Repos[0].Err)
	}
	if got := len(s.Repos[0].Updated); got != 1 {
		t.Errorf("updated=%d, want 1 (a-1 diffs on priority+description)", got)
	}
	if got := len(s.Repos[0].Created); got != 1 {
		t.Errorf("created=%d, want 1 (a-2)", got)
	}
	if got := len(s.Repos[0].Skipped); got != 1 {
		t.Errorf("skipped=%d, want 1 (closed a-3)", got)
	}
	if fake.CreateCalls != 1 {
		t.Errorf("Create called %d times, want 1", fake.CreateCalls)
	}
	if fake.SetPriCalls != 1 {
		t.Errorf("SetPriority called %d times, want 1 (a-1 priority diff)", fake.SetPriCalls)
	}
	if fake.SetDescCalls < 1 {
		t.Errorf("UpdateDescription called %d times, want >=1", fake.SetDescCalls)
	}
}

func TestRunImportPlan_LabelAndOwnerDiffs(t *testing.T) {
	// Existing test only varies priority+description; this one
	// proves the label add/remove and assignee paths fire on a
	// real (non-dry-run) plan, covering the wiring labelDiff
	// drives.
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name: "repo-a",
		Issues: []beads.Issue{
			{ID: "a-1", Status: "open", Owner: "bob", Labels: []string{"keep", "new"}},
		},
	}}}
	fake := &fakeImportClient{existing: []beads.Issue{
		{ID: "a-1", Owner: "alice", Labels: []string{"keep", "old"}},
	}}
	mk := func(_ string) importClient { return fake }

	s := runImportPlan(reg, dump, false, mk)
	if s.HadError {
		t.Fatalf("HadError true: %v", s.Repos[0].Err)
	}
	if fake.SetAssCalls != 1 {
		t.Errorf("SetAssignee=%d, want 1", fake.SetAssCalls)
	}
	if fake.AddLabelCalls != 1 {
		t.Errorf("AddLabel=%d, want 1 (new)", fake.AddLabelCalls)
	}
	if fake.RemoveLabelCalls != 1 {
		t.Errorf("RemoveLabel=%d, want 1 (old)", fake.RemoveLabelCalls)
	}
	if got := len(s.Repos[0].Updated); got != 1 {
		t.Errorf("updated=%d, want 1", got)
	}
}

func TestRunImportPlan_EmptyDescriptionDoesNotClobber(t *testing.T) {
	// Dump issue carries no description; local has one. The
	// reconcile must NOT wipe the local description (the guard
	// added in roborev-fix for 1372).
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name: "repo-a",
		Issues: []beads.Issue{
			{ID: "a-1", Status: "open", Description: ""},
		},
	}}}
	fake := &fakeImportClient{existing: []beads.Issue{
		{ID: "a-1", Description: "do-not-clear"},
	}}
	mk := func(_ string) importClient { return fake }

	s := runImportPlan(reg, dump, false, mk)
	if s.HadError {
		t.Fatalf("HadError true: %v", s.Repos[0].Err)
	}
	if fake.SetDescCalls != 0 {
		t.Errorf("UpdateDescription called %d times for empty-dump-description; want 0", fake.SetDescCalls)
	}
	if got := len(s.Repos[0].Unchanged); got != 1 {
		t.Errorf("unchanged=%d, want 1 (no diff applied)", got)
	}
}

// fakeImportClientPartial fails on SetDescription so the partial-
// write accounting path is exercised: SetPriority succeeds,
// SetDescription errors, and the row should still land in
// `Updated` (with the error noted) rather than vanishing from the
// summary entirely.
type fakeImportClientPartial struct {
	*fakeImportClient
	descErr error
}

func (f *fakeImportClientPartial) UpdateDescription(_ context.Context, _, _ string) error {
	f.SetDescCalls++
	return f.descErr
}

func TestRunImportPlan_PartialFailureCountedAsUpdated(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name: "repo-a",
		Issues: []beads.Issue{
			{ID: "a-1", Status: "open", Priority: 1, Description: "new body"},
		},
	}}}
	base := &fakeImportClient{existing: []beads.Issue{
		{ID: "a-1", Priority: 2, Description: "old body"},
	}}
	mk := func(_ string) importClient {
		return &fakeImportClientPartial{fakeImportClient: base, descErr: errors.New("bd timeout")}
	}

	s := runImportPlan(reg, dump, false, mk)
	if !s.HadError {
		t.Errorf("partial failure should set HadError")
	}
	if got := len(s.Repos[0].Updated); got != 1 {
		t.Errorf("updated=%d, want 1 (partial change still counts)", got)
	}
	if !strings.Contains(s.Repos[0].Err, "bd timeout") {
		t.Errorf("err should name underlying cause; got %q", s.Repos[0].Err)
	}
}

func TestRunImportPlan_DryRunSkipsWrites(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name: "repo-a",
		Issues: []beads.Issue{
			{ID: "a-1", Title: "exists", Priority: 1, Status: "open"},
			{ID: "a-2", Title: "missing", Status: "open"},
		},
	}}}
	fake := &fakeImportClient{existing: []beads.Issue{{ID: "a-1", Priority: 2}}}
	mk := func(_ string) importClient { return fake }

	s := runImportPlan(reg, dump, true, mk)
	if fake.CreateCalls != 0 || fake.SetPriCalls != 0 || fake.SetDescCalls != 0 {
		t.Errorf("dry-run wrote: create=%d setpri=%d setdesc=%d",
			fake.CreateCalls, fake.SetPriCalls, fake.SetDescCalls)
	}
	if len(s.Repos[0].Created) != 1 || !strings.HasPrefix(s.Repos[0].Created[0], "would-create:") {
		t.Errorf("dry-run created should carry placeholder ID; got %v", s.Repos[0].Created)
	}
}

func TestRunImportPlan_UnknownRepoSkippedWithError(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name:   "ghost",
		Issues: []beads.Issue{{ID: "g-1", Status: "open"}},
	}}}
	mk := func(_ string) importClient { return &fakeImportClient{} }
	s := runImportPlan(reg, dump, false, mk)
	if !s.HadError {
		t.Errorf("expected HadError for unregistered repo")
	}
	if len(s.Repos[0].Skipped) != 1 {
		t.Errorf("skipped=%d, want 1", len(s.Repos[0].Skipped))
	}
}

func TestRunImportPlan_ListAllErrorAbortsRepo(t *testing.T) {
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: "/tmp/a"}}}
	dump := exportDump{Repos: []exportRepo{{
		Name:   "repo-a",
		Issues: []beads.Issue{{ID: "a-1", Status: "open"}},
	}}}
	fake := &fakeImportClient{ListErr: errors.New("bd down")}
	mk := func(_ string) importClient { return fake }

	s := runImportPlan(reg, dump, false, mk)
	if !s.HadError {
		t.Errorf("ListAll failure should set HadError")
	}
	if !strings.Contains(s.Repos[0].Err, "bd down") {
		t.Errorf("err should mention underlying cause; got %q", s.Repos[0].Err)
	}
	if fake.CreateCalls != 0 {
		t.Errorf("must not Create when ListAll failed")
	}
}

func TestLabelDiff_AddsAndRemovesAreSorted(t *testing.T) {
	adds, removes := labelDiff([]string{"keep", "drop1", "drop2"}, []string{"keep", "add2", "add1"})
	if got := strings.Join(adds, ","); got != "add1,add2" {
		t.Errorf("adds=%v, want [add1 add2]", adds)
	}
	if got := strings.Join(removes, ","); got != "drop1,drop2" {
		t.Errorf("removes=%v, want [drop1 drop2]", removes)
	}
}

func TestEmitImportSummary_DryRunBanner(t *testing.T) {
	var buf bytes.Buffer
	emitImportSummary(&buf, importSummary{Repos: []importRepoResult{{Name: "r", Path: "/p"}}}, true)
	if !strings.Contains(buf.String(), "DRY-RUN") {
		t.Errorf("dry-run banner missing: %q", buf.String())
	}
}

func TestRunImport_StdinHappyPath(t *testing.T) {
	// End-to-end through runImport: feed a tiny dump on stdin
	// against a populated registry. Needs XDG_CONFIG_HOME pointed
	// at a tempdir holding a synthetic registry file so
	// registry.DefaultPath() / Load() find our fixture.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	regPath, err := registry.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	reg := &registry.Registry{Repos: []registry.Repo{{Name: "repo-a", Path: t.TempDir()}}}
	if err := reg.Save(regPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dump := exportDump{Repos: []exportRepo{{
		Name:   "repo-a",
		Issues: []beads.Issue{{ID: "a-1", Title: "fresh", Status: "open"}},
	}}}
	body, _ := json.Marshal(dump)

	// Plant the stub client. Without it the real bd binary would run.
	prev := defaultImportClient
	defaultImportClient = func(_ string) importClient { return &fakeImportClient{} }
	defer func() { defaultImportClient = prev }()

	// Pipe stdin so the no-arg form reads our body.
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	go func() { _, _ = io.Copy(w, bytes.NewReader(body)); w.Close() }()

	_ = captureStdout(t, func() {
		if code := runImport([]string{"-dry-run"}); code != 0 {
			t.Errorf("runImport exit %d, want 0", code)
		}
	})
}
