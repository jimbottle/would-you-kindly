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

// IsPreset reports whether s names a known preset. Use to
// validate command-line input (e.g. `wyk -preset <name>`) before
// constructing a Preset value — silently coercing an unknown
// name to PresetAll would hide a typo from the user.
func IsPreset(s string) bool {
	for _, p := range presetOrder {
		if string(p) == s {
			return true
		}
	}
	return false
}

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
	return QueryWithClosed(p, me, false)
}

// QueryWithClosed is Query with an explicit `includeClosed` flag.
// When true, the `status!=closed` exclusion is dropped so the
// caller sees closed issues alongside open ones — the C-key
// toggle in the TUI surfaces this. PresetAll still returns the
// empty string (callers map it to bd list or bd list --all based
// on the same flag); PresetReady is unaffected (ready by
// definition excludes closed, and bd ready has no --all equivalent).
func QueryWithClosed(p Preset, me string, includeClosed bool) string {
	switch p {
	case PresetHuman:
		if includeClosed {
			return `label=human`
		}
		return `label=human AND status!=closed`
	case PresetBlocked:
		// status=blocked already excludes closed by construction.
		return `status=blocked`
	case PresetMine:
		base := ``
		if me != "" {
			base = `assignee=` + me
		}
		if includeClosed {
			if base == "" {
				return ``
			}
			return base
		}
		if base == "" {
			return `status!=closed`
		}
		return base + ` AND status!=closed`
	case PresetReady, PresetAll:
		return ""
	default:
		if includeClosed {
			return ``
		}
		return `status!=closed`
	}
}
