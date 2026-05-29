package filter

import "testing"

func TestQueryWithClosed(t *testing.T) {
	cases := []struct {
		name           string
		preset         Preset
		me             string
		includeClosed  bool
		want           string
	}{
		{"human open-only", PresetHuman, "ev", false, `label=human AND status!=closed`},
		{"human with closed", PresetHuman, "ev", true, `label=human`},
		{"mine open-only with me", PresetMine, "ev", false, `assignee=ev AND status!=closed`},
		{"mine with closed and me", PresetMine, "ev", true, `assignee=ev`},
		{"mine open-only without me", PresetMine, "", false, `status!=closed`},
		{"mine with closed without me", PresetMine, "", true, ``},
		{"blocked stays blocked", PresetBlocked, "ev", false, `status=blocked`},
		{"blocked with closed flag ignored", PresetBlocked, "ev", true, `status=blocked`},
		{"all returns empty", PresetAll, "ev", false, ``},
		{"all returns empty with closed", PresetAll, "ev", true, ``},
		{"ready returns empty", PresetReady, "ev", false, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QueryWithClosed(tc.preset, tc.me, tc.includeClosed); got != tc.want {
				t.Errorf("QueryWithClosed(%q, %q, %v) = %q, want %q", tc.preset, tc.me, tc.includeClosed, got, tc.want)
			}
		})
	}
}

func TestQueryDelegatesToWithClosed(t *testing.T) {
	if Query(PresetHuman, "ev") != QueryWithClosed(PresetHuman, "ev", false) {
		t.Errorf("Query should delegate to QueryWithClosed(.., false)")
	}
}
