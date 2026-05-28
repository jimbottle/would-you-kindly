// Package filter defines the named issue-list presets the TUI cycles
// through. Each preset maps to a bd query expression (the canonical
// surface; see docs/CONTRACT.md) — the package is intentionally tiny
// and stringly-typed so it can be embedded in commands and rendered
// in the status bar without ceremony.
package filter

// Preset is a named issue-list view.
type Preset string

const (
	PresetAll     Preset = "all"
	PresetReady   Preset = "ready"
	PresetHuman   Preset = "human"
	PresetMine    Preset = "mine"
	PresetBlocked Preset = "blocked"
)

// presetOrder is the rotation order used by Tab in the TUI.
var presetOrder = []Preset{
	PresetAll,
	PresetReady,
	PresetHuman,
	PresetMine,
	PresetBlocked,
}

// AllPresets returns the rotation in order.
func AllPresets() []Preset { return append([]Preset(nil), presetOrder...) }

// NextPreset advances p one step in the rotation, wrapping around.
func NextPreset(p Preset) Preset {
	for i, q := range presetOrder {
		if q == p {
			return presetOrder[(i+1)%len(presetOrder)]
		}
	}
	return presetOrder[0]
}

// Query returns the bd query expression that materialises a preset,
// or the empty string for presets that have no `bd query` equivalent.
// `me` is the current user, used by PresetMine; empty `me` falls back
// to all open issues, since "mine" with no identity is moot.
//
// Two presets intentionally return "":
//
//   - PresetReady has blocker-aware semantics only `bd ready` can
//     reproduce; a query approximation would silently drop the
//     blocked-by-open-deps exclusion.
//   - PresetAll wants closed issues included; `bd list --all` is the
//     canonical source, and `status!=closed` would drop them.
//
// Sources are expected to special-case these two presets and call
// the dedicated bd subcommands. Returning "" instead of a wrong-but-
// plausible query means a Source that forgets the special case will
// fail loudly (bd rejects an empty query) rather than quietly return
// the wrong set.
func Query(p Preset, me string) string {
	switch p {
	case PresetHuman:
		return `label=human AND status!=closed`
	case PresetBlocked:
		return `status=blocked`
	case PresetMine:
		if me == "" {
			return `status!=closed`
		}
		return `assignee=` + me + ` AND status!=closed`
	case PresetReady, PresetAll:
		return ""
	default:
		return `status!=closed`
	}
}
