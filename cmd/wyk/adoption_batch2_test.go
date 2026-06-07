package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestDebugLogPath(t *testing.T) {
	cases := []struct {
		name    string
		logFile string
		debug   string
		want    string
	}{
		{"both unset -> disabled", "", "", ""},
		{"WYK_LOG_FILE wins", "/tmp/x.log", "1", "/tmp/x.log"},
		{"WYK_DEBUG=1 -> default file", "", "1", "wyk-debug.log"},
		{"WYK_DEBUG=true -> default file", "", "true", "wyk-debug.log"},
		{"WYK_DEBUG=0 -> disabled", "", "0", ""},
		{"WYK_DEBUG=false -> disabled", "", "false", ""},
		{"WYK_DEBUG=off -> disabled", "", "off", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WYK_LOG_FILE", c.logFile)
			t.Setenv("WYK_DEBUG", c.debug)
			if got := debugLogPath(); got != c.want {
				t.Errorf("debugLogPath() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestInboxResult_DegradedJSON(t *testing.T) {
	// An empty inbox WITH a failed repo must marshal degraded=true so a
	// consumer never reads it as "no work" (would-you-kindly-aity).
	res := inboxResult{
		Issues:   []beads.Issue{},
		Degraded: true,
		Errors:   []repoError{{Repo: "beta", Error: "timed out"}},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"degraded":true`) {
		t.Errorf("envelope must carry degraded=true; got %s", s)
	}
	// And the healthy case is explicitly false (always emitted).
	healthy, _ := json.Marshal(inboxResult{Issues: []beads.Issue{}})
	if !strings.Contains(string(healthy), `"degraded":false`) {
		t.Errorf("healthy envelope must carry degraded=false; got %s", healthy)
	}
}
