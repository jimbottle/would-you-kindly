package main

import "testing"

func TestHandoff_CreateAndPositionalAreMutuallyExclusive(t *testing.T) {
	// The two modes of `wyk handoff` are -create (file a new issue)
	// and positional <id> (act on an existing issue). Both at once
	// would be ambiguous — runHandoff must refuse with the usage
	// exit code 64 before reading stdin or touching bd.
	code := runHandoff([]string{"-create", "some title", "would-you-kindly-42"})
	if code != 64 {
		t.Errorf("expected exit 64 when both -create and positional id given; got %d", code)
	}
}

func TestHandoff_MissingArgsReturnsUsageCode(t *testing.T) {
	// No -create, no positional id → usage error (64), no stdin read,
	// no bd contact. Pure flag-parsing validation.
	code := runHandoff([]string{})
	if code != 64 {
		t.Errorf("expected exit 64 when no <id> and no -create; got %d", code)
	}
}
