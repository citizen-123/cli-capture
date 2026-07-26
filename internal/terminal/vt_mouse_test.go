package terminal

import (
	"os"
	"testing"
	"time"
)

// readWithTimeout reads whatever is available on r within timeout, or
// returns "" if nothing arrived within it. Mouse bytes only appear on r once
// VT's pumpReplies goroutine pulls them out of the emulator's io.Pipe and
// writes them here, and Read() on an empty pipe blocks forever — a bare Read
// would hang a test that's checking "nothing should be forwarded" rather
// than fail it. Every caller in this file reads at most once per pipe, so a
// timed-out call's goroutine — left blocked until the pipe is closed at test
// end — never has a second read on the same fd to race against.
func readWithTimeout(t *testing.T, r *os.File, timeout time.Duration) string {
	t.Helper()
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := r.Read(buf)
		ch <- result{string(buf[:n]), err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("Read: %v", res.err)
		}
		return res.s
	case <-time.After(timeout):
		return "" // nothing arrived in time — the caller decides if that's expected
	}
}

// TestVTForwardMouseGatesOnChildOptIn pins the safety rule that used to live
// in the TUI as MouseEnabled/MouseSGR checks: cli-capture must never inject
// mouse-shaped bytes into a child that never asked for them. x/vt's
// SendMouse now owns that gate internally (see ForwardMouse).
//
// It proves the "before DECSET, nothing" half by forwarding a click, then
// enabling tracking, then forwarding the same click again, and reading only
// once: if the first (disabled) ForwardMouse had leaked any bytes onto the
// pipe, this single read would see them prefixed onto — or instead of — the
// second click's SGR sequence. There's nowhere for a leak to hide.
func TestVTForwardMouseGatesOnChildOptIn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	vt := NewVT(20, 5, w)
	defer func() {
		if err := vt.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	click := MouseEvent{X: 4, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress}
	vt.ForwardMouse(click) // the child hasn't enabled mouse tracking yet: must be silently dropped

	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("Write(enable mouse): %v", err)
	}
	vt.ForwardMouse(click)

	const want = "\x1b[<0;5;3M"
	if got := readWithTimeout(t, r, time.Second); got != want {
		t.Fatalf("ForwardMouse(%+v) after enabling = %q, want exactly %q (a leading/duplicate write here means the pre-DECSET click wasn't dropped)", click, got, want)
	}
}

// TestVTForwardMouseEncoding drives a range of buttons and modifiers through
// a mouse-enabled emulator and checks the SGR bytes it produces against
// xterm's documented button-code encoding (shift=4, alt=8, ctrl=16,
// motion=32, wheel=64), not against any one decoder's inverse — the two
// derivations should agree independently.
func TestVTForwardMouseEncoding(t *testing.T) {
	tests := []struct {
		name string
		ev   MouseEvent
		want string
	}{
		{"left press", MouseEvent{X: 6, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress}, "\x1b[<0;7;3M"},
		{"left release", MouseEvent{X: 6, Y: 2, Button: MouseButtonLeft, Action: MouseActionRelease}, "\x1b[<0;7;3m"},
		{"middle press", MouseEvent{X: 6, Y: 2, Button: MouseButtonMiddle, Action: MouseActionPress}, "\x1b[<1;7;3M"},
		{"right press", MouseEvent{X: 6, Y: 2, Button: MouseButtonRight, Action: MouseActionPress}, "\x1b[<2;7;3M"},
		{"drag with left held", MouseEvent{X: 6, Y: 2, Button: MouseButtonLeft, Action: MouseActionMotion}, "\x1b[<32;7;3M"},
		{"motion with no button", MouseEvent{X: 6, Y: 2, Button: MouseButtonNone, Action: MouseActionMotion}, "\x1b[<35;7;3M"},
		{"wheel up", MouseEvent{X: 6, Y: 2, Button: MouseButtonWheelUp, Action: MouseActionPress}, "\x1b[<64;7;3M"},
		{"wheel down", MouseEvent{X: 6, Y: 2, Button: MouseButtonWheelDown, Action: MouseActionPress}, "\x1b[<65;7;3M"},
		{
			"wheel never gains the motion bit", // 96 would decode as a drag, not a scroll
			MouseEvent{X: 6, Y: 2, Button: MouseButtonWheelUp, Action: MouseActionMotion},
			"\x1b[<64;7;3M",
		},
		{"shift+left press", MouseEvent{X: 6, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress, Shift: true}, "\x1b[<4;7;3M"},
		{"alt+middle press", MouseEvent{X: 6, Y: 2, Button: MouseButtonMiddle, Action: MouseActionPress, Alt: true}, "\x1b[<9;7;3M"},
		{"ctrl+right press", MouseEvent{X: 6, Y: 2, Button: MouseButtonRight, Action: MouseActionPress, Ctrl: true}, "\x1b[<18;7;3M"},
		{"backward press", MouseEvent{X: 6, Y: 2, Button: MouseButtonBackward, Action: MouseActionPress}, "\x1b[<128;7;3M"},
		{"forward press", MouseEvent{X: 6, Y: 2, Button: MouseButtonForward, Action: MouseActionPress}, "\x1b[<129;7;3M"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			defer r.Close()

			vt := NewVT(20, 5, w)
			defer func() {
				if err := vt.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			}()
			if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
				t.Fatalf("Write(enable mouse): %v", err)
			}

			vt.ForwardMouse(tc.ev)
			if got := readWithTimeout(t, r, time.Second); got != tc.want {
				t.Errorf("ForwardMouse(%+v) = %q, want %q", tc.ev, got, tc.want)
			}
		})
	}
}

// TestVTForwardMouseWithNilRespIsANoop guards the resp==nil construction path
// (no query-driven reply target): ForwardMouse must not panic or block just
// because there's nowhere for the bytes to go.
func TestVTForwardMouseWithNilRespIsANoop(t *testing.T) {
	vt := NewVT(20, 5, nil)
	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("Write(enable mouse): %v", err)
	}
	vt.ForwardMouse(MouseEvent{X: 0, Y: 0, Button: MouseButtonLeft, Action: MouseActionPress}) // must not panic or hang
}
