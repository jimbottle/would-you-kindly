package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jimbottle/would-you-kindly/internal/beads"
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

// identitySublabelPrefix is the prefix of a per-identity routing label.
// Note it ends in a colon, so the collective umbrella `src:agent` (no
// trailing colon) is deliberately NOT a sublabel.
const identitySublabelPrefix = "src:agent:"

// hasIdentitySublabel reports whether the issue carries ANY per-identity
// routing label (src:agent:<something>) — i.e. it has been routed to
// some specific agent. The bare `src:agent` umbrella does not count.
func hasIdentitySublabel(i beads.Issue) bool {
	for _, l := range i.Labels {
		if strings.HasPrefix(l, identitySublabelPrefix) {
			return true
		}
	}
	return false
}

// issueBelongsToIdentity is the phase-2 "unclaimed sweep" predicate: an
// issue belongs in identity `name`'s inbox when it is EITHER routed to
// that identity (carries src:agent:<name>) OR un-routed entirely (carries
// no src:agent:<other> sublabel, so it's collective work nobody has
// claimed). Only rows routed to a DIFFERENT identity are excluded.
//
// bd 1.0.4 can't express `NOT label=src:agent:*`, so the caller fetches
// the collective set and applies this filter in Go (would-you-kindly-r4h7).
func issueBelongsToIdentity(i beads.Issue, name string) bool {
	if i.HasLabel(identityLabel(name)) {
		return true
	}
	return !hasIdentitySublabel(i)
}

// filterToIdentity keeps only the issues that belong in identity `name`'s
// swept inbox (routed-to-name OR un-routed), preserving order.
func filterToIdentity(issues []beads.Issue, name string) []beads.Issue {
	out := issues[:0]
	for _, i := range issues {
		if issueBelongsToIdentity(i, name) {
			out = append(out, i)
		}
	}
	return out
}

// labelAdder is the slice of the bd client `applyIdentityRouting`
// needs — just adding a label. *beads.Client satisfies it; a test can
// substitute a stub so the bare-id routing path is exercised without a
// real bd binary.
type labelAdder interface {
	AddLabel(ctx context.Context, id, label string) error
}

// applyIdentityRouting tags an already-handed-off issue with its
// identity routing label (the bare-id path; -create sets the label at
// creation time instead). Caller guarantees ident != "". A failure is
// reported but non-fatal — the handoff itself already succeeded, so the
// human still sees the task; only the bounce-back routing is missing.
func applyIdentityRouting(ctx context.Context, c labelAdder, id, ident string) {
	if err := c.AddLabel(ctx, id, identityLabel(ident)); err != nil {
		fmt.Fprintf(os.Stderr, "wyk handoff: identity routing failed (handoff itself succeeded): %v\n", err)
		return
	}
	fmt.Printf("routed %s to identity %q\n", id, ident)
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
