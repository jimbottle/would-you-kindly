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

// Query returns the bd query expression that materialises a preset.
// `me` is the current user, used by PresetMine; empty `me` falls
// back to all open issues, since "mine" with no identity is moot.
//
// The PresetReady preset is special: bd's `bd ready` command applies
// blocker-aware semantics that `bd query` cannot replicate exactly.
// Callers should check Preset == PresetReady and use the dedicated
// ready endpoint rather than this query string.
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
	case PresetReady:
		return `status=open` // fallback; prefer bd ready
	case PresetAll:
		fallthrough
	default:
		return `status!=closed`
	}
}
