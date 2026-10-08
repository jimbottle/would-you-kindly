package beads

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Metadata is bd's per-issue key/value store as wyk reads it. bd stores
// whatever JSON the user hands `--metadata`, so a value is not
// guaranteed to be a string — a number, bool, or nested object is
// legal. Decoding straight into map[string]string would make one such
// value fail the parse of the WHOLE list payload (every row gone from
// the TUI because someone stored `{"retries": 3}`), so the custom
// decoder coerces scalars to their JSON text and keeps compound values
// as their raw JSON. wyk only ever writes strings (the lease keys).
type Metadata map[string]string

// UnmarshalJSON accepts a JSON object with values of any type. Strings
// are taken verbatim; every other value is kept as its compact JSON
// encoding so nothing is lost and nothing panics. null decodes to nil.
func (m *Metadata) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "null" {
		*m = nil
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	out := make(Metadata, len(raw))
	for k, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	*m = out
	return nil
}

// SortedKeys returns the keys in lexical order — deterministic argv for
// the metadata-writing bd calls, and stable test output.
func (m Metadata) SortedKeys() []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
