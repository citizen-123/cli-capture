//go:build !race

// Every test in this file calls VT.Close, which trips a data race that lives in
// the emulator library rather than in this package.
//
// charmbracelet/x/vt's Emulator.closed is a plain bool: Close writes it
// (emulator.go:265) while a parked Read reads it (emulator.go:252), with no
// synchronization between them. Read blocks on an empty pipe and Close is the
// only thing that unblocks it, so the two necessarily run concurrently and
// there is no way to shut an emulator down that the detector accepts.
// SafeEmulator is not an escape either: its Read deliberately takes no lock and
// it does not override Close, so the one pair that races is the one pair the
// wrapper doesn't cover.
//
// The guard is redundant rather than load-bearing, which is why the upstream fix
// is one line: the real unblocking is pw.CloseWithError(io.EOF) on an
// internally-synchronized io.Pipe, and `closed` is only an early-out. Tracked
// upstream as charmbracelet/x#879, with a fix already proposed in
// charmbracelet/x#881 (atomic.Bool). Applying that patch locally turns this
// package's whole suite green under -race with no exclusions at all, which is
// the evidence that nothing here is racing on its own account.
//
// Until that lands these tests are excluded from `go test -race` (which sets the
// `race` build tag) and run in the plain `go test` step instead — see the two
// Test steps in .github/workflows/ci.yml, which exist for exactly this reason.
//
// To remove this quarantine once the upstream fix is released: bump
// charmbracelet/x/vt, delete this file's build tag, fold the tests back into
// vt_test.go and vt_mouse_test.go, and drop the plain `go test` step from CI.

package terminal

import (
	"io"
	"os"
	"sync"
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

// TestVTCloseStopsReplyPumpWithoutLeaking exercises the shutdown path Close
// exists for: NewVT's pump goroutine is blocked in Read() on an empty pipe,
// and Close must unblock it (rather than leaving it running forever) before
// returning.
func TestVTCloseStopsReplyPumpWithoutLeaking(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	vt := NewVT(10, 2, w)
	// Close must return promptly (it blocks on the pump's done channel) —
	// if the pump were leaking instead of exiting on io.EOF, this call would
	// hang and the test would time out rather than fail cleanly.
	if err := vt.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// blockingWriter parks the first Write it receives until release is closed,
// standing in for the target's PTY once a stalled child has stopped draining
// it. entered closes as the Write parks, so a test can wait for the pump to be
// definitively stuck rather than sleeping and hoping.
type blockingWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

// TestVTCloseReturnsWhileReplyPumpIsStuckWriting is the hang Close's bounded
// wait exists to prevent.
//
// pumpReplies has two parking spots, and term.Close only reaches one of them.
// Unblocking a pump parked in Read is what Close is built for; a pump parked in
// resp.Write — where it sits whenever the child has stopped draining its pty —
// cannot be woken from here at all, and darwin will not take a write deadline
// on a pty master to fix it at the cause. Before the bound, that turned a
// wedged child into a wedged quit: Close waited on a channel that would never
// close.
//
// The writer here blocks unconditionally rather than being a real full pty,
// because "resp.Write never returns" is the property under test and a real pty
// only reaches that state by timing.
func TestVTCloseReturnsWhileReplyPumpIsStuckWriting(t *testing.T) {
	w := newBlockingWriter()
	defer close(w.release) // let the abandoned pump exit before the test binary does

	vt := NewVT(20, 5, w)

	// Make the emulator produce bytes so the pump has something to write. A
	// click is only emitted once the child has asked for mouse tracking, so
	// enable it first (the same setup the ForwardMouse tests use).
	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("Write(enable mouse): %v", err)
	}
	vt.ForwardMouse(MouseEvent{X: 4, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress})

	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reply pump never reached resp.Write; the test never set up the condition it means to check")
	}

	done := make(chan error, 1)
	go func() { done <- vt.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(closeGrace + 5*time.Second):
		t.Fatal("Close did not return with the reply pump stuck in resp.Write — quitting the TUI would hang")
	}
}

// TestVTCloseIsPromptWhenThePumpCanExit guards the other half of the bound: the
// grace period is a ceiling for the stuck case, not a delay every shutdown
// pays. An ordinary Close must still return as soon as the pump is gone.
func TestVTCloseIsPromptWhenThePumpCanExit(t *testing.T) {
	vt := NewVT(10, 2, io.Discard)

	start := time.Now()
	if err := vt.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= closeGrace {
		t.Errorf("Close took %v with a pump that could exit; it waited out the %v grace period instead of returning on done",
			elapsed, closeGrace)
	}
}
