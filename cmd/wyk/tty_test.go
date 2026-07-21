package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsTTYOpenErr(t *testing.T) {
	// The --probe hint must survive rewording (two independent
	// case-insensitive prongs: the "open a new TTY" message and the
	// wrapped /dev/tty path) without firing on unrelated errors that
	// merely contain the "tty" substring.
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("could not open a new TTY: open /dev/tty: device not configured"), true},
		{fmt.Errorf("wrapped: %w", errors.New("open /dev/tty: no such device")), true},
		{errors.New("could not Open A New TTY"), true},
		{errors.New("some unrelated fetch failure"), false},
		{errors.New("template getty-pretty failed"), false},
		{errors.New("open /home/betty/config: permission denied"), false},
	}
	for _, c := range cases {
		if got := isTTYOpenErr(c.err); got != c.want {
			t.Errorf("isTTYOpenErr(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}
