package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEmitJSON_CompactVsPretty(t *testing.T) {
	v := map[string]any{"a": 1, "b": []int{2, 3}}

	var pretty bytes.Buffer
	if err := emitJSON(&pretty, v, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty.String(), "\n  ") {
		t.Errorf("pretty output should be indented; got %q", pretty.String())
	}

	var compact bytes.Buffer
	if err := emitJSON(&compact, v, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compact.String(), "\n  ") {
		t.Errorf("compact output should NOT be indented; got %q", compact.String())
	}
	// Compact must be strictly smaller for a nested value.
	if compact.Len() >= pretty.Len() {
		t.Errorf("compact (%d) should be smaller than pretty (%d)", compact.Len(), pretty.Len())
	}
}
