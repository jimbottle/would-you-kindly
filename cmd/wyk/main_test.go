package main

import (
	"strings"
	"testing"
)

func TestNoColorRequested(t *testing.T) {
	cases := []struct {
		name    string
		noColor string
		wykNo   string
		want    bool
	}{
		{"both unset", "", "", false},
		{"NO_COLOR=1", "1", "", true},
		{"NO_COLOR=true", "true", "", true},
		// no-color.org spec: any non-empty value counts.
		{"NO_COLOR= (single space)", " ", "", true},
		{"WYK_NO_COLOR=1", "", "1", true},
		{"both set", "1", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("WYK_NO_COLOR", tc.wykNo)
			if got := noColorRequested(); got != tc.want {
				t.Errorf("noColorRequested() = %v, want %v (NO_COLOR=%q WYK_NO_COLOR=%q)", got, tc.want, tc.noColor, tc.wykNo)
			}
		})
	}
}

func TestVersionString_NonEmpty(t *testing.T) {
	// Sanity check: the function always produces SOMETHING usable,
	// regardless of how the binary was built (go install, go build,
	// go run, vendored). Bug reports are useless without a version
	// string and the function should never return "".
	got := versionString()
	if got == "" {
		t.Fatal("versionString returned empty string — bug reports lose the version line")
	}
	if !strings.HasPrefix(got, "wyk ") {
		t.Errorf("expected version string to start with 'wyk '; got %q", got)
	}
}

func TestVersionString_NoDoubleDirty(t *testing.T) {
	// Regression: an earlier draft printed "+dirty" (from Go's
	// pseudoversion suffix) AND "-dirty" (from our vcs.modified
	// inspection) when both signals were present. Whatever the
	// build state, the word "dirty" should appear at most once.
	got := versionString()
	if strings.Count(got, "dirty") > 1 {
		t.Errorf("'dirty' appears more than once in version string: %q", got)
	}
}
