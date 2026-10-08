package main

import (
	"errors"
	"testing"
	"time"

	"github.com/jimbottle/would-you-kindly/internal/beads"
	"github.com/jimbottle/would-you-kindly/internal/lease"
)

func leased(id, owner string, until time.Time, prio int) beads.Issue {
	return beads.Issue{ID: id, Status: "in_progress", Priority: prio, Assignee: owner, Labels: []string{"src:agent"},
		Metadata: beads.Metadata{lease.KeyOwner: owner, lease.KeyUntil: until.UTC().Format(time.RFC3339)}}
}

func TestRankNext_OrderAndExclusions(t *testing.T) {
	now := claimNow
	ttl := time.Hour
	sub := inboxSub{name: "r1"}
	f := nextFetch{sub: sub,
		inProgress: []beads.Issue{
			leased("r1-mine", "me", now.Add(time.Hour), 3),
			leased("r1-abandoned", "ghost", now.Add(-time.Hour), 0), // lapsed → takeover candidate
			leased("r1-busy", "them", now.Add(time.Hour), 0),        // live, theirs → hidden
		},
		inbox: []beads.Issue{
			{ID: "r1-inbox-p2", Status: "open", Priority: 2, Labels: []string{"src:agent"}},
			{ID: "r1-inbox-p0", Status: "open", Priority: 0, Labels: []string{"src:agent"}},
			leased("r1-inbox-theirs", "them", now.Add(time.Hour), 0),  // live, someone else's → dropped
			leased("r1-inbox-lapsed", "them", now.Add(-time.Hour), 1), // expired → back in the pool
			{ID: "r1-fenced", Status: "open", Priority: 0, Labels: []string{"src:agent", "agent-handoff"}},
		},
		ready: []beads.Issue{
			{ID: "r1-ready-p1", Status: "open", Priority: 1},
			{ID: "r1-ready-p0", Status: "open", Priority: 0},
			{ID: "r1-inbox-p2", Status: "open", Priority: 2, Labels: []string{"src:agent"}}, // dup of an inbox row
			{ID: "r1-human", Status: "open", Priority: 0, Labels: []string{"human"}},
			{ID: "r1-closed", Status: "closed", Priority: 0},
		},
	}
	rows, errs := rankNext([]nextFetch{f}, "me", now, ttl)
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	// The abandoned P0 shares the ready band, so it sorts with ready-p0 by
	// priority then id.
	want := []string{"r1-mine", "r1-inbox-p0", "r1-inbox-lapsed", "r1-inbox-p2", "r1-abandoned", "r1-ready-p0", "r1-ready-p1"}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.ID
	}
	if len(got) != len(want) {
		t.Fatalf("ids=%v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids=%v\nwant %v", got, want)
		}
	}
	if rows[0].Source != sourceMine || rows[0].Lease == nil || rows[0].Lease.Owner != "me" {
		t.Fatalf("top row should be mine with its lease: %+v", rows[0])
	}
	if rows[2].Lease == nil || rows[2].Lease.State != lease.Expired || rows[2].Source != sourceInbox {
		t.Fatalf("lapsed row should carry its expired lease and stay an inbox row: %+v", rows[2])
	}
	if rows[4].Source != sourceExpired || rows[4].Lease.Owner != "ghost" {
		t.Fatalf("abandoned row should be an expired takeover naming ghost: %+v", rows[4])
	}
	if rows[0].Repo != "r1" {
		t.Fatal("rows must be stamped with their repo")
	}
}

func TestRankNext_InboxRowIHoldIsMine(t *testing.T) {
	// A bounced-back issue I already hold a live lease on resumes at the
	// top, not in the inbox band.
	f := nextFetch{sub: inboxSub{name: "r"}, inbox: []beads.Issue{leased("r-1", "me", claimNow.Add(time.Hour), 2)}}
	rows, _ := rankNext([]nextFetch{f}, "me", claimNow, time.Hour)
	if len(rows) != 1 || rows[0].Source != sourceMine {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestRankNext_MyLapsedLeaseIsStillMine(t *testing.T) {
	// Nobody took it over, so it is still assigned to me: resume, don't
	// present it as someone else's abandoned work.
	f := nextFetch{sub: inboxSub{name: "r"}, inProgress: []beads.Issue{leased("r-1", "me", claimNow.Add(-time.Hour), 2)}}
	rows, _ := rankNext([]nextFetch{f}, "me", claimNow, time.Hour)
	if len(rows) != 1 || rows[0].Source != sourceMine {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestRankNext_NoIdentityTreatsEveryLiveLeaseAsOthers(t *testing.T) {
	f := nextFetch{sub: inboxSub{name: "r"}, ready: []beads.Issue{
		{ID: "r-open", Status: "open"},
	}, inbox: []beads.Issue{leased("r-held", "someone", claimNow.Add(time.Hour), 0)}}
	rows, _ := rankNext([]nextFetch{f}, "", claimNow, time.Hour)
	if len(rows) != 1 || rows[0].ID != "r-open" {
		t.Fatalf("rows=%+v, want only the open row", rows)
	}
}

func TestRankNext_FailedSubIsReportedNotSilentlyEmpty(t *testing.T) {
	ok := nextFetch{sub: inboxSub{name: "good"}, ready: []beads.Issue{{ID: "good-1", Status: "open"}}}
	bad := nextFetch{sub: inboxSub{name: "bad"}, err: errors.New("boom"), ready: []beads.Issue{{ID: "bad-1", Status: "open"}}}
	rows, errs := rankNext([]nextFetch{ok, bad}, "me", claimNow, time.Hour)
	if len(rows) != 1 || rows[0].ID != "good-1" {
		t.Fatalf("rows=%+v", rows)
	}
	if len(errs) != 1 || errs[0].repo != "bad" {
		t.Fatalf("errs=%v", errs)
	}
}

func TestRankNext_CrossRepoTieBreak(t *testing.T) {
	a := nextFetch{sub: inboxSub{name: "b-repo"}, ready: []beads.Issue{{ID: "b-repo-1", Status: "open", Priority: 1}}}
	b := nextFetch{sub: inboxSub{name: "a-repo"}, ready: []beads.Issue{{ID: "a-repo-1", Status: "open", Priority: 1}}}
	rows, _ := rankNext([]nextFetch{a, b}, "me", claimNow, time.Hour)
	if rows[0].Repo != "a-repo" {
		t.Fatalf("same priority should order by repo name: %+v", rows)
	}
}

func TestFilterRowsToIdentity(t *testing.T) {
	rows := []nextRow{
		{Issue: beads.Issue{ID: "mine", Labels: []string{"src:agent", "src:agent:me"}}},
		{Issue: beads.Issue{ID: "theirs", Labels: []string{"src:agent", "src:agent:them"}}},
		{Issue: beads.Issue{ID: "unrouted", Labels: []string{"src:agent"}}},
		{Issue: beads.Issue{ID: "ready-plain"}},
	}
	got := filterRowsToIdentity(rows, "me")
	if len(got) != 3 || got[0].ID != "mine" || got[1].ID != "unrouted" || got[2].ID != "ready-plain" {
		t.Fatalf("got=%+v", got)
	}
}
