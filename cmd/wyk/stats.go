package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/registry"
)

// runStats implements `wyk stats`: aggregate counts and timing
// across every registered workspace. v1 scope:
//
//   - Issue counts by status (open / in_progress / blocked / deferred / closed)
//   - Handoff counts (currently human-flagged, split by src:agent / src:human)
//   - Inbox count (src:agent without human, status != closed) — the
//     agent's pending pickup queue
//   - Closed in the last 7d / 30d windows
//   - Median + p95 time-to-close for human-flagged issues (across history)
//
// bd's interactions.jsonl doesn't track label add/remove events, so
// total-handoffs-filed-over-time is not derivable today. The metrics
// here come from current bd state plus created_at/closed_at on each
// issue.
//
// Exit codes match the other wyk subcommands: 0 success, 1 generic,
// 2 bd missing or no workspace, 64 usage error.
func runStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	dir := fs.String("C", "", "scope to a single workspace; default is every registered repo")
	asJSON := fs.Bool("json", false, "emit a JSON object suitable for scripting")
	repoName := fs.String("repo", "", "restrict the rollup to the registered repo with this name (mutually exclusive with -C)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wyk stats [-C <dir>] [-json] [-repo name]")
		return 64
	}
	if *dir != "" && *repoName != "" {
		fmt.Fprintln(os.Stderr, "wyk stats: -C and -repo are mutually exclusive")
		return 64
	}

	subs, code := statsSubs(*dir, *repoName)
	if code != 0 {
		return code
	}

	all, firstErr := fetchAllIssues(subs)
	if len(all) == 0 && firstErr != nil {
		switch {
		case errors.Is(firstErr, beads.ErrBDNotFound):
			fmt.Fprintln(os.Stderr, "wyk: bd is not installed (or not on PATH)")
			return 2
		case errors.Is(firstErr, beads.ErrNoWorkspace):
			fmt.Fprintln(os.Stderr, "wyk: no beads workspace here — run `bd init`")
			return 2
		default:
			fmt.Fprintln(os.Stderr, "wyk stats:", firstErr)
			return 1
		}
	}

	s := computeStats(all, time.Now())
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s); err != nil {
			fmt.Fprintln(os.Stderr, "wyk stats: encode:", err)
			return 1
		}
		return 0
	}
	renderStatsText(s, len(subs))
	return 0
}

type statsSub struct {
	client *beads.Client
	name   string
}

func statsSubs(dir, repoName string) ([]statsSub, int) {
	if dir != "" {
		c := beads.NewClient()
		c.Dir = dir
		return []statsSub{{client: c}}, 0
	}
	regPath, err := registry.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk stats:", err)
		return nil, 1
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wyk stats:", err)
		return nil, 1
	}
	if len(reg.Repos) == 0 {
		c := beads.NewClient()
		return []statsSub{{client: c}}, 0
	}
	if repoName != "" {
		filtered, err := filterRegistryByName(reg, repoName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wyk stats:", err)
			return nil, 1
		}
		reg = filtered
	}
	out := make([]statsSub, len(reg.Repos))
	for i, r := range reg.Repos {
		c := beads.NewClient()
		c.Dir = r.Path
		out[i] = statsSub{client: c, name: r.Name}
	}
	return out, 0
}

// fetchAllIssues calls ListAll across every sub in parallel — needed
// because stats covers closed history (not just active work).
func fetchAllIssues(subs []statsSub) ([]beads.Issue, error) {
	type result struct {
		issues []beads.Issue
		err    error
	}
	results := make([]result, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s statsSub) {
			defer wg.Done()
			issues, err := s.client.ListAll(context.Background())
			results[i] = result{issues: issues, err: err}
		}(i, s)
	}
	wg.Wait()

	var all []beads.Issue
	var firstErr error
	for i, s := range subs {
		r := results[i]
		if r.err != nil {
			if firstErr == nil {
				if s.name != "" {
					firstErr = fmt.Errorf("%s: %w", s.name, r.err)
				} else {
					firstErr = r.err
				}
			}
			continue
		}
		for j := range r.issues {
			r.issues[j].Repo = s.name
		}
		all = append(all, r.issues...)
	}
	return all, firstErr
}

// Stats is the computed snapshot, kept exported-shape friendly so
// the -json output is stable for scripting.
type Stats struct {
	TotalIssues      int            `json:"total_issues"`
	ByStatus         map[string]int `json:"by_status"`
	HumanFlagged     int            `json:"human_flagged"`
	HumanFromAgent   int            `json:"human_from_agent"`
	HumanFromHuman   int            `json:"human_from_human"`
	InboxCount       int            `json:"inbox_count"`
	ClosedLast7d     int            `json:"closed_last_7d"`
	ClosedLast30d    int            `json:"closed_last_30d"`
	TimeToCloseHuman *Timings       `json:"time_to_close_human,omitempty"`
}

// Timings is the median + p95 + sample count for a population of
// durations. nil when the population is empty.
type Timings struct {
	Samples int           `json:"samples"`
	Median  time.Duration `json:"median_ns"`
	P95     time.Duration `json:"p95_ns"`
}

func computeStats(all []beads.Issue, now time.Time) Stats {
	s := Stats{ByStatus: map[string]int{}}
	s.TotalIssues = len(all)

	var humanCloseDurations []time.Duration
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	for _, i := range all {
		s.ByStatus[i.Status]++

		human := i.HasLabel("human")
		closed := i.Status == "closed"

		if human && !closed {
			s.HumanFlagged++
			switch {
			case i.HasLabel("src:agent"):
				s.HumanFromAgent++
			case i.HasLabel("src:human"):
				s.HumanFromHuman++
			}
		}
		// Inbox: src:agent that has lost its human label and isn't closed.
		if i.HasLabel("src:agent") && !human && !closed {
			s.InboxCount++
		}
		if closed && !i.ClosedAt.IsZero() {
			if i.ClosedAt.After(sevenDaysAgo) {
				s.ClosedLast7d++
			}
			if i.ClosedAt.After(thirtyDaysAgo) {
				s.ClosedLast30d++
			}
			// time-to-close for human-flagged: includes issues that
			// once had `human` and have since been closed. We can't
			// see the historical label set here (interactions.jsonl
			// only records status changes), so we approximate by
			// "currently labeled human" — closed issues keep their
			// labels.
			if human && !i.CreatedAt.IsZero() {
				humanCloseDurations = append(humanCloseDurations, i.ClosedAt.Sub(i.CreatedAt))
			}
		}
	}

	if len(humanCloseDurations) > 0 {
		s.TimeToCloseHuman = computeTimings(humanCloseDurations)
	}
	return s
}

func computeTimings(d []time.Duration) *Timings {
	if len(d) == 0 {
		return nil
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	median := d[len(d)/2]
	p95Idx := int(float64(len(d)) * 0.95)
	if p95Idx >= len(d) {
		p95Idx = len(d) - 1
	}
	return &Timings{
		Samples: len(d),
		Median:  median,
		P95:     d[p95Idx],
	}
}

func renderStatsText(s Stats, subCount int) {
	scope := "the workspace"
	if subCount > 1 {
		scope = fmt.Sprintf("%d registered repos", subCount)
	}
	fmt.Printf("Across %s — %d issues total.\n\n", scope, s.TotalIssues)

	fmt.Println("By status:")
	// Stable order regardless of map iteration.
	for _, st := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
		if n := s.ByStatus[st]; n > 0 {
			fmt.Printf("  %-14s %d\n", st, n)
		}
	}
	fmt.Println()

	fmt.Println("Human-flagged (currently open):")
	fmt.Printf("  total          %d\n", s.HumanFlagged)
	fmt.Printf("  src:agent      %d  (handoffs from an agent)\n", s.HumanFromAgent)
	fmt.Printf("  src:human      %d  (self-filed)\n", s.HumanFromHuman)
	fmt.Println()

	fmt.Printf("Agent inbox:     %d  (src:agent without human, not closed)\n", s.InboxCount)
	fmt.Println()

	fmt.Println("Recent activity:")
	fmt.Printf("  closed in last 7d   %d\n", s.ClosedLast7d)
	fmt.Printf("  closed in last 30d  %d\n", s.ClosedLast30d)
	fmt.Println()

	if s.TimeToCloseHuman != nil {
		fmt.Println("Time-to-close for human-flagged issues (lifetime):")
		fmt.Printf("  median         %s\n", humanizeDuration(s.TimeToCloseHuman.Median))
		fmt.Printf("  p95            %s\n", humanizeDuration(s.TimeToCloseHuman.P95))
		fmt.Printf("  samples        %d\n", s.TimeToCloseHuman.Samples)
	} else {
		fmt.Println("Time-to-close for human-flagged issues: no closed samples yet.")
	}
}

// humanizeDuration renders a duration as the most legible single
// unit — minutes for under an hour, hours for under a day, days
// otherwise. Stats need to be readable at a glance.
func humanizeDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd %dh", days, hours)
	}
}
