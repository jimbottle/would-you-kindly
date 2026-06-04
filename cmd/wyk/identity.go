package main

import (
	"fmt"
	"os"
	"regexp"
)

// identityEnvVar is the ambient agent-identity source used by
// `wyk inbox` / `wyk handoff` when no -identity flag is given. Set it
// once per agent session so that agent's inbox reads and handoff writes
// are both scoped to its identity. See docs/CONTRACT.md (wyk-contract/v3).
const identityEnvVar = "WYK_AGENT_IDENTITY"

// identitySlugRE constrains an agent identity to a small, label-safe
// vocabulary: lowercase letters/digits/hyphens, starting alphanumeric.
// No colon (labels are colon-namespaced, so a colon in the name would
// make `src:agent:<name>` ambiguous), no spaces, no uppercase (avoids
// case-folding surprises across tools). wyk-contract/v3.
var identitySlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateIdentity returns an error if name isn't a legal identity slug.
func validateIdentity(name string) error {
	if !identitySlugRE.MatchString(name) {
		return fmt.Errorf("invalid agent identity %q: must match [a-z0-9][a-z0-9-]* "+
			"(lowercase letters, digits, hyphens; no colon, space, or uppercase)", name)
	}
	return nil
}

// identityLabel is the routing label for a named agent identity. It is
// layered ON TOP of the collective `src:agent` umbrella (which is never
// removed), so identity-routed work still matches the collective inbox
// query and every consumer that keys off exactly `src:agent`.
// wyk-contract/v3.
func identityLabel(name string) string {
	return "src:agent:" + name
}

// resolveIdentity picks the agent identity from the -identity flag
// (highest precedence), then the WYK_AGENT_IDENTITY env var, returning
// "" when neither is set — the collective, pre-v3 behavior. A
// set-but-invalid value is an error rather than a silent fallthrough,
// so a typo'd identity can't quietly route to (or read) the collective
// inbox instead of the intended one.
func resolveIdentity(flagVal string) (string, error) {
	name := flagVal
	if name == "" {
		name = os.Getenv(identityEnvVar)
	}
	if name == "" {
		return "", nil
	}
	if err := validateIdentity(name); err != nil {
		return "", err
	}
	return name, nil
}
