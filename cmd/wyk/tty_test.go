package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsTTYOpenErr(t *testing.T) {
	// The --probe hint must survive rewording: bubbletea v1.3.10 says
	// "could not open a new TTY: open /dev/tty: device not configured",
	// but the match is case-insensitive so a lowercase "tty" (or the
	// wrapped /dev/tty PathError) still triggers it.
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("could not open a new TTY: open /dev/tty: device not configured"), true},
		{fmt.Errorf("wrapped: %w", errors.New("open /dev/tty: no such device")), true},
		{errors.New("tty unavailable"), true},
		{errors.New("some unrelated fetch failure"), false},
	}
	for _, c := range cases {
		if got := isTTYOpenErr(c.err); got != c.want {
			t.Errorf("isTTYOpenErr(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}
