// Package lease is the "who is working this, and until when" model that
// lets several agents share one bd workspace without colliding
// (wyk-contract/v4, docs/CONTRACT.md).
//
// A claim is a LEASE: it belongs to a stable agent identity (never a
// session), and it expires. The lease lives on the issue itself —
// bd's assignee + in_progress status are the lock (bd's atomic
// `--claim` refuses a second holder), and three bd metadata keys
// carry the identity, expiry and branch:
//
//	wyk.lease.owner   the identity holding the lease
//	wyk.lease.until   RFC3339 expiry; renewed by `wyk claim -renew`
//	wyk.lease.branch  the git branch the holder is working on
//
// Expiry is computed ON READ: nothing sweeps the workspace, and no
// daemon is needed. A row whose lease has lapsed is simply treated as
// unclaimed by every consumer (`wyk next`, the TUI badge, `wyk claim`'s
// takeover path). A bare `bd update --claim` that left no metadata still
// gets an IMPLICIT lease — assignee as owner, updated_at + TTL as
// expiry — so a non-wyk agent's claim neither holds forever nor goes
// unseen.
package lease

import (
	"fmt"
	"strings"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

// Metadata keys the lease is stored under. Namespaced with `wyk.` so a
// workspace's other metadata (any tool may write keys) can't collide.
const (
	KeyOwner  = "wyk.lease.owner"
	KeyUntil  = "wyk.lease.until"
	KeyBranch = "wyk.lease.branch"
)

// Keys lists the lease metadata keys, for an unset-everything release.
func Keys() []string { return []string{KeyOwner, KeyUntil, KeyBranch} }

// DefaultTTL is how long a claim lives without renewal when neither the
// config key claim_ttl nor $WYK_CLAIM_TTL says otherwise. Two hours is
// long enough to cover one agent's uninterrupted stretch on a task and
// short enough that an abandoned session frees its items the same
// afternoon. Overridable because the right number depends on how the
// team runs its agents.
const DefaultTTL = 2 * time.Hour

// TTL is the process-wide effective TTL, resolved once at startup from
// config / env by the CLI (ResolveTTL) and read by every consumer that
// has no config handle of its own (the TUI badge). Defaults to
// DefaultTTL so library users and tests need no setup.
var TTL = DefaultTTL

// State is what a reader concludes about an issue's lease.
type State int

const (
	// None: nobody holds the issue — open, closed, or in_progress with
	// nothing to identify a holder. Claimable.
	None State = iota
	// Live: held and not yet expired. Another identity must not touch it.
	Live
	// Expired: a holder is recorded but the expiry has passed. Claimable
	// by takeover; the TUI shows it as EXPIRED so the lapse is visible
	// rather than silently recycled.
	Expired
)

// String renders the state for text output and the TUI badge.
func (s State) String() string {
	switch s {
	case Live:
		return "live"
	case Expired:
		return "expired"
	default:
		return "none"
	}
}

// MarshalText renders the state by name in JSON.
func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Lease is one issue's claim as a reader sees it.
type Lease struct {
	// Owner is the holding identity (wyk.lease.owner, else bd's assignee).
	// Empty when the issue is in_progress but nobody is recorded — treated
	// as held-by-unknown so two agents still can't both pick it up.
	Owner string `json:"owner,omitempty"`
	// Until is when the lease lapses. Zero means "never": bd's `hooked`
	// status marks an issue attached to a non-wyk agent's hook, with no
	// expiry wyk can know, so it is Live indefinitely.
	Until time.Time `json:"until,omitzero"`
	// Branch is the holder's git branch, if `wyk claim` recorded one.
	Branch string `json:"branch,omitempty"`
	// Implicit is true when the lease was derived from assignee /
	// status / updated_at alone — a claim made without wyk, whose
	// expiry is a fallback rather than a stamp.
	Implicit bool `json:"implicit,omitempty"`
	// State is the conclusion for the `now` the lease was read at —
	// "live" / "expired" / "none" in JSON, so an agent reading `wyk next
	// -json` needn't recompute the expiry rules.
	State State `json:"state"`
}

// Of reads the lease on i as of now, using ttl for the implicit-expiry
// fallback. It never errors: a malformed stamp degrades to the implicit
// rules rather than hiding the row.
//
// The gate is bd's STATUS, not the metadata: a stale wyk.lease.* stamp
// on an issue someone reopened with raw bd does not keep it held. Only
// in_progress and hooked issues can carry a lease at all.
func Of(i beads.Issue, now time.Time, ttl time.Duration) Lease {
	switch i.Status {
	case "hooked":
		// bd's own in-flight marker for hook-driven agents: a holder we
		// can't name or expire. Live until the status changes.
		l := Lease{Owner: firstNonEmpty(i.Metadata[KeyOwner], i.Assignee), Implicit: true, State: Live}
		l.Branch = i.Metadata[KeyBranch]
		return l
	case "in_progress":
	default:
		return Lease{State: None}
	}
	l := Lease{
		Owner:  firstNonEmpty(i.Metadata[KeyOwner], i.Assignee),
		Branch: i.Metadata[KeyBranch],
	}
	if raw := i.Metadata[KeyUntil]; raw != "" {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
			l.Until = t
		}
	}
	if l.Until.IsZero() {
		// No usable stamp: fall back to "no activity for one TTL". UpdatedAt
		// rather than StartedAt because bd never resets started_at on a
		// later claim by someone else, so it dates the FIRST holder, not
		// the current one, and would expire a fresh bare claim instantly.
		l.Implicit = true
		base := i.UpdatedAt
		if base.IsZero() {
			base = i.StartedAt
		}
		if !base.IsZero() && ttl > 0 {
			l.Until = base.Add(ttl)
		}
	}
	switch {
	case !l.Until.IsZero() && now.After(l.Until):
		l.State = Expired
	default:
		l.State = Live
	}
	return l
}

// HeldByOther reports whether i carries a LIVE lease held by someone
// other than me. This is the single predicate every "what can I work
// on" surface uses to drop another agent's rows; an expired lease is
// NOT held, so lapsed work flows back into the pool. An empty me
// matches no owner, so every live lease counts as someone else's.
func HeldByOther(i beads.Issue, me string, now time.Time, ttl time.Duration) bool {
	l := Of(i, now, ttl)
	return l.State == Live && (me == "" || l.Owner != me)
}

// Stamp builds the metadata a claim or renewal writes.
func Stamp(owner string, until time.Time, branch string) beads.Metadata {
	m := beads.Metadata{
		KeyOwner: owner,
		KeyUntil: until.UTC().Format(time.RFC3339),
	}
	if branch != "" {
		m[KeyBranch] = branch
	}
	return m
}

// ParseTTL parses a lease TTL from config / env: a Go duration ("2h",
// "90m", "45m30s") or a bare number of minutes ("120"). Zero and
// negative values are rejected — a lease that expires immediately (or
// never, via 0) is a misconfiguration, not a setting.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty TTL")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		var mins int
		if _, serr := fmt.Sscanf(s, "%d", &mins); serr != nil || fmt.Sprint(mins) != s {
			return 0, fmt.Errorf("invalid TTL %q: use a duration like 2h or 90m, or whole minutes", s)
		}
		d = time.Duration(mins) * time.Minute
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid TTL %q: must be positive", s)
	}
	return d, nil
}

// Remaining renders how long a lease has left (or how long ago it
// lapsed) in coarse human units, for text output and the TUI.
func Remaining(l Lease, now time.Time) string {
	if l.Until.IsZero() {
		return "no expiry"
	}
	d := l.Until.Sub(now)
	if d < 0 {
		return "expired " + coarse(-d) + " ago"
	}
	return coarse(d) + " left"
}

func coarse(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%02dm", h, m)
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
