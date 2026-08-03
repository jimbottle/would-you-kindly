package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBulkVerbsCoverEveryBulkAction pins bulkVerbs against the actions
// actually passed to runBulkWrite in the source.
//
// A missing entry is silent: handleBulkWriteResult falls back to the raw
// action name, so the banner read "type 3 rows" instead of "retyped 3
// rows" for as long as the type bulk-write existed
// (would-you-kindly-6gjb). Extracting the call sites from source means a
// new bulk action fails here until its past tense is added.
//
// Literal action names only — a call site passing a variable escapes
// this guard; the package convention is uniformly literal, so keep it.
func TestBulkVerbsCoverEveryBulkAction(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	siteRE := regexp.MustCompile(`runBulkWrite\("([^"]+)"`)
	inSource := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range siteRE.FindAllStringSubmatch(string(b), -1) {
			inSource[m[1]] = true
		}
	}
	if len(inSource) == 0 {
		t.Fatal("found no runBulkWrite call sites — the extraction regex has drifted")
	}
	for action := range inSource {
		verb, ok := bulkVerbs[action]
		if !ok {
			t.Errorf("runBulkWrite(%q) has no bulkVerbs entry — its banner would read %q instead of a past tense", action, action+" N rows")
			continue
		}
		// A verb equal to the action isn't a past tense; it would be
		// indistinguishable from the missing-entry fallback.
		if verb == action {
			t.Errorf("bulkVerbs[%q] = %q — needs the past-tense form", action, verb)
		}
	}
	for action := range bulkVerbs {
		if !inSource[action] {
			t.Errorf("bulkVerbs lists %q but no runBulkWrite(%q) call site exists — remove the stale entry", action, action)
		}
	}
}
