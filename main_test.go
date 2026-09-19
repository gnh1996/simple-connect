package main

import (
	"errors"
	"strings"
	"testing"
)

func TestIsTTYOpenErr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("connection refused"), false},
		{"open TTY", errors.New("bubbletea: error opening TTY: bubbletea: could not open TTY: open /dev/tty: no such device or address"), true},
		{"could not open", errors.New("bubbletea: could not open TTY: access denied"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isTTYOpenErr(tc.err); got != tc.want {
				t.Fatalf("isTTYOpenErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAnnotateTTYErr(t *testing.T) {
	t.Parallel()
	other := errors.New("boom")
	if got := annotateTTYErr(other); got != other {
		t.Fatalf("non-TTY error should pass through, got %v", got)
	}

	src := errors.New("bubbletea: error opening TTY: open /dev/tty: no such device or address")
	got := annotateTTYErr(src)
	if got == src {
		t.Fatal("TTY error should be annotated")
	}
	msg := got.Error()
	if !strings.Contains(msg, src.Error()) {
		t.Fatalf("annotated error missing original: %s", msg)
	}
	if !strings.Contains(msg, ttyRequiredHint) {
		t.Fatalf("annotated error missing hint: %s", msg)
	}
}
