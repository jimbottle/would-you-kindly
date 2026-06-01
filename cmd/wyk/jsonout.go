package main

import (
	"encoding/json"
	"io"
)

// emitJSON is the single JSON encoder for every agent-facing `-json`
// surface. Pretty-printed (2-space indent) by default for the
// human-eyeballing / piping-into-jq case; compact (no indentation)
// when compact is true. Indentation is pure overhead for an LLM
// consumer, so the agent commands expose a `-compact` flag that routes
// through here — keeping one consistent convention instead of each
// command re-deriving the encoder. Part of the agent-token-efficiency
// epic.
func emitJSON(w io.Writer, v any, compact bool) error {
	enc := json.NewEncoder(w)
	if !compact {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}
