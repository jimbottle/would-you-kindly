package main

import (
	"errors"
	"strings"
	"testing"
)

func TestTeachBDConvention_PassesKeyAndMemory(t *testing.T) {
	// The seam lets us assert what teachBDConvention sends to bd
	// without needing a real bd binary. Pin the key (so prime
	// continues to find the memory across releases) and the
	// presence of the canonical labels + query in the memory body.
	var gotRoot, gotKey, gotMemory string
	prev := bdRememberRunner
	bdRememberRunner = func(repoRoot, key, memory string) error {
		gotRoot, gotKey, gotMemory = repoRoot, key, memory
		return nil
	}
	defer func() { bdRememberRunner = prev }()

	if err := teachBDConvention("/some/repo"); err != nil {
		t.Fatalf("teachBDConvention: %v", err)
	}
	if gotRoot != "/some/repo" {
		t.Errorf("repoRoot = %q, want %q", gotRoot, "/some/repo")
	}
	if gotKey != rememberedConventionKey {
		t.Errorf("key = %q, want %q", gotKey, rememberedConventionKey)
	}
	for _, want := range []string{"label=human", "label=src:agent", agentInboxQuery} {
		if !strings.Contains(gotMemory, want) {
			t.Errorf("memory missing %q\n---\n%s", want, gotMemory)
		}
	}
}

func TestTeachBDConvention_PropagatesError(t *testing.T) {
	// Best-effort failures are surfaced to the caller (runInit
	// turns them into a WARN line). Confirm the error wrapping
	// passes through.
	prev := bdRememberRunner
	bdRememberRunner = func(_, _, _ string) error {
		return errors.New("bd: simulated boom")
	}
	defer func() { bdRememberRunner = prev }()
	err := teachBDConvention("/x")
	if err == nil || !strings.Contains(err.Error(), "simulated boom") {
		t.Errorf("expected the underlying error to propagate; got %v", err)
	}
}
