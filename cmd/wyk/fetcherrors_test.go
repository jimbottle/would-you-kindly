package main

import (
	"errors"
	"testing"

	"github.com/jimbottle/would-you-kindly/internal/beads"
)

func TestClassifyTotalFetchFailure(t *testing.T) {
	boom := subError{repo: "r1", err: errors.New("boom")}
	bdGone := subError{repo: "r1", err: beads.ErrBDNotFound}
	noWS := subError{repo: "r1", err: beads.ErrNoWorkspace}

	cases := []struct {
		name      string
		queried   int
		subErrs   []subError
		wantCode  int
		wantTotal bool
	}{
		{"no errors", 2, nil, 0, false},
		{"partial failure is not total", 2, []subError{boom}, 0, false},
		{"nothing queried", 0, nil, 0, false},
		{"total generic failure", 2, []subError{boom, boom}, 1, true},
		{"total bd-not-found maps to 2", 1, []subError{bdGone}, 2, true},
		{"total no-workspace maps to 2", 1, []subError{noWS}, 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, total := classifyTotalFetchFailure(c.queried, c.subErrs)
			if code != c.wantCode || total != c.wantTotal {
				t.Errorf("classifyTotalFetchFailure(%d, %d errs) = (%d, %v), want (%d, %v)",
					c.queried, len(c.subErrs), code, total, c.wantCode, c.wantTotal)
			}
		})
	}
}
