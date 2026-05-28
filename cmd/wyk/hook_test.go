package main

import (
	"reflect"
	"testing"
)

func TestParseCloseRefs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single Closes trailer",
			in:   "Subject line\n\nBody.\n\nCloses: bd-42",
			want: []string{"bd-42"},
		},
		{
			name: "Fixes and Resolves both match",
			in:   "Subject\n\nFixes: bd-1\nResolves: bd-2\n",
			want: []string{"bd-1", "bd-2"},
		},
		{
			name: "case-insensitive keyword",
			in:   "CLOSES: bd-1\ncloses: bd-2\nFiXeS: bd-3",
			want: []string{"bd-1", "bd-2", "bd-3"},
		},
		{
			name: "# separator (github-style)",
			in:   "Closes #bd-42",
			want: []string{"bd-42"},
		},
		{
			name: "hierarchical IDs with dots",
			in:   "Closes: would-you-kindly-ma5.4\nFixes: my-proj-abc.1.2",
			want: []string{"would-you-kindly-ma5.4", "my-proj-abc.1.2"},
		},
		{
			name: "duplicates collapsed, order preserved",
			in:   "Closes: bd-1\nCloses: bd-2\nCloses: bd-1",
			want: []string{"bd-1", "bd-2"},
		},
		{
			name: "inline mention not at line start is ignored",
			in:   "We are not going to close it. closes: bd-99 in the middle of a sentence",
			want: nil,
		},
		{
			name: "code-block-like prefix tolerated (> for quotes only)",
			in:   "> Closes: bd-1",
			want: []string{"bd-1"},
		},
		{
			name: "no refs",
			in:   "Just a subject\n\nNo trailers here.",
			want: nil,
		},
		{
			name: "blank message",
			in:   "",
			want: nil,
		},
		{
			// One ID per line is the documented behaviour. A trailer
			// listing multiple IDs is rejected wholesale rather than
			// partially matched — see the closeRefRE comment for why.
			name: "comma-separated IDs on one line are NOT matched (use separate lines)",
			in:   "Subject\n\nCloses: bd-1, bd-2",
			want: nil,
		},
		{
			name: "two separate Closes lines both match",
			in:   "Subject\n\nCloses: bd-1\nCloses: bd-2",
			want: []string{"bd-1", "bd-2"},
		},
		{
			name: "trailing text after ID rejects the line",
			in:   "Closes: bd-1 (we'll handle bd-2 next week)",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseCloseRefs(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseCloseRefs() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseCloseRefs_RealWykCommit(t *testing.T) {
	// A real-shaped message from this project. The actual commits we
	// shipped used "Refs: <id>" (no auto-close) and "Closes: <id>"
	// (explicit auto-close) — only the latter should fire.
	msg := `feat(beads): wire bd CLI client

internal/beads/client.go shells out to the bd binary.

Refs: would-you-kindly-8et
Closes: would-you-kindly-ci6`
	got := parseCloseRefs(msg)
	want := []string{"would-you-kindly-ci6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
