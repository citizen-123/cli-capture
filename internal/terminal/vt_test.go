package terminal

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stripANSI removes CSI escape sequences so positional assertions can look at
// the visible characters only.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestVTAbsolutePositioning is the capability the line-oriented Screen this
// package used to fall back on lacked: a target that jumps the cursor to an
// absolute row/column and writes there — the way full-screen TUIs (claude,
// vim) paint — must land in the right cell.
func TestVTAbsolutePositioning(t *testing.T) {
	vt := NewVT(20, 5, nil)
	if _, err := vt.Write([]byte("\x1b[2J")); err != nil { // clear screen
		t.Fatalf("Write(clear): %v", err)
	}
	if _, err := vt.Write([]byte("\x1b[3;5HHELLO")); err != nil { // move to row 3, col 5, then write
		t.Fatalf("Write(HELLO): %v", err)
	}

	lines := strings.Split(stripANSI(vt.Render(20, 5)), "\n")
	if len(lines) < 3 {
		t.Fatalf("want at least 3 rows, got %d", len(lines))
	}
	row3 := lines[2] // 0-based index 2 == 1-based row 3
	idx := strings.Index(row3, "HELLO")
	if idx != 4 {
		t.Errorf("HELLO landed at column %d of row 3 (%q), want column 4", idx, row3)
	}
}

// TestVTColorsSurvive checks that SGR color attributes reach the rendered pane.
func TestVTColorsSurvive(t *testing.T) {
	vt := NewVT(10, 2, nil)
	if _, err := vt.Write([]byte("\x1b[31mRED")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := vt.Render(10, 2)
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("render should contain SGR escapes: %q", out)
	}
	if !strings.Contains(stripANSI(out), "RED") {
		t.Errorf("text lost: %q", stripANSI(out))
	}
}

// TestVTClearAndOverwrite verifies erase + rewrite, another thing the line
// buffer couldn't model.
func TestVTClearAndOverwrite(t *testing.T) {
	vt := NewVT(10, 2, nil)
	if _, err := vt.Write([]byte("first")); err != nil {
		t.Fatalf("Write(first): %v", err)
	}
	if _, err := vt.Write([]byte("\x1b[2J\x1b[H")); err != nil { // clear, home
		t.Fatalf("Write(clear+home): %v", err)
	}
	if _, err := vt.Write([]byte("second")); err != nil {
		t.Fatalf("Write(second): %v", err)
	}
	out := stripANSI(vt.Render(10, 2))
	if !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Errorf("after clear+rewrite, want 'second' not 'first': %q", out)
	}
}

// TestVTRenderClipsToRequestedSize pins the behaviour that survived the
// vt10x → x/vt swap unchanged: Render resizes the emulator to width×height
// and never returns more rows or columns than asked, even though the
// emulator itself may be tracking a larger grid.
func TestVTRenderClipsToRequestedSize(t *testing.T) {
	vt := NewVT(40, 10, nil)
	if _, err := vt.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := stripANSI(vt.Render(8, 1))
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 rendered row, got %d: %q", len(lines), out)
	}
	if len(lines[0]) != 8 {
		t.Fatalf("want 8 rendered columns, got %d: %q", len(lines[0]), lines[0])
	}
	if lines[0] != "01234567" {
		t.Errorf("clipped row = %q, want %q", lines[0], "01234567")
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
