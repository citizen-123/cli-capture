package terminal

import (
	"errors"
	"io"
	"log"
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

// TestVTCloseCoordinatesWithPublicOperations guards VT's lifecycle boundary:
// Close and every public operation may arrive from different goroutines, but
// shutdown must be idempotent and must not let an operation enter x/vt while
// the emulator is being torn down.
//
// Removing Close from VT's operation lock makes this test report a race on
// VT's lifecycle state. Calling x/vt's Close from it also exposes the upstream
// Read/Write/Close race. Removing the closed-state checks lets Write mutate the
// emulator after shutdown instead of returning io.ErrClosedPipe.
func TestVTCloseCoordinatesWithPublicOperations(t *testing.T) {
	vt := NewVT(20, 5, io.Discard)
	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("Write(enable mouse): %v", err)
	}

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var operations sync.WaitGroup
	for range workers {
		operations.Add(1)
		go func() {
			defer operations.Done()
			<-start
			for range 100 {
				_, _ = vt.Write([]byte("x"))
				vt.Resize(21, 6)
				_ = vt.Render(20, 5)
				vt.ForwardMouse(MouseEvent{
					X:      1,
					Y:      1,
					Button: MouseButtonLeft,
					Action: MouseActionPress,
				})
			}
		}()
	}
	for range workers {
		go func() {
			<-start
			errs <- vt.Close()
		}()
	}

	close(start)
	operations.Wait()
	for range workers {
		if err := <-errs; err != nil {
			t.Errorf("Close: %v", err)
		}
	}

	if n, err := vt.Write([]byte("after close")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write after Close = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	// The void operations have no error channel; their post-close contract is
	// simply to be safe no-ops.
	vt.Resize(40, 10)
	_ = vt.Render(40, 10)
	vt.ForwardMouse(MouseEvent{X: 1, Y: 1, Button: MouseButtonLeft, Action: MouseActionPress})
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

// failingWriter reports one attempted write and rejects it. A PTY master
// normally reaches this state after the child exits or its descriptor closes.
type failingWriter struct {
	entered chan struct{}
	once    sync.Once
}

func newFailingWriter() *failingWriter {
	return &failingWriter{entered: make(chan struct{})}
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return 0, errors.New("target PTY is closed")
}

// gatedRecordingWriter parks its first Write until release is closed, then
// records that write and every later one. writes is deliberately larger than
// the reply FIFO exercised below, so the test itself never applies backpressure
// after releasing the simulated PTY.
type gatedRecordingWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	writes  chan []byte
}

func newGatedRecordingWriter(capacity int) *gatedRecordingWriter {
	return &gatedRecordingWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		writes:  make(chan []byte, capacity),
	}
}

func (w *gatedRecordingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	b := append([]byte(nil), p...)
	w.writes <- b
	return len(p), nil
}

func writeVTWithin(v *VT, p string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, err := v.Write([]byte(p))
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("VT.Write blocked")
	}
}

// TestVTCloseReturnsAfterSecondReplyWithStalledWriter covers the complete hang
// sequence Close's bounded wait and the decoupled emulator drain prevent.
//
// The reply drain and target writer are now separate, so Close can stop a drain
// parked in Read and a writer parked on its queue. The one parking spot it
// still cannot wake is the external resp.Write call itself, where the writer
// sits whenever the child has stopped draining its PTY. Darwin will not take a
// write deadline on a PTY master to fix that at the cause. Waiting without a
// bound there turns a wedged child into a wedged quit.
//
// The second query is essential: it proves the emulator's sole pipe reader is
// still draining after the first reply parks in resp.Write. Close must then
// return within its bound while that external write remains stalled. The writer
// blocks unconditionally because a real full PTY only reaches that state by
// timing.
func TestVTCloseReturnsAfterSecondReplyWithStalledWriter(t *testing.T) {
	w := newBlockingWriter()
	vt := NewVT(20, 5, w)
	defer func() {
		close(w.release)
		_ = vt.Close()
	}()

	if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
		t.Fatalf("initial device-status query: %v", err)
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("reply forwarding never parked in resp.Write")
	}

	if err := writeVTWithin(vt, "\x1b[3;4H\x1b[6n", time.Second); err != nil {
		t.Fatalf("second query with stalled reply writer: %v; emulator drain stopped behind resp.Write", err)
	}

	done := make(chan error, 1)
	go func() { done <- vt.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(closeGrace + time.Second):
		t.Fatal("Close did not return within its bound after a second reply with resp.Write stalled")
	}
}

// TestVTWriteErrorDoesNotStopEmulatorDrain guards the x/vt pipe's single-reader
// invariant. A target PTY write error may stop forwarding, but it must not stop
// the goroutine draining the emulator: query replies and mouse reports write to
// an unbuffered io.Pipe, and without that reader every later producer blocks.
func TestVTWriteErrorDoesNotStopEmulatorDrain(t *testing.T) {
	w := newFailingWriter()
	vt := NewVT(20, 5, w)
	defer vt.Close() //nolint:errcheck

	if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
		t.Fatalf("first device-status query: %v", err)
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("reply forwarding never reached the failing target writer")
	}

	if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
		t.Fatalf("query after target write error: %v; the emulator's sole pipe reader stopped", err)
	}

	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("enable mouse after target write error: %v", err)
	}
	mouseDone := make(chan struct{})
	go func() {
		vt.ForwardMouse(MouseEvent{X: 4, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress})
		close(mouseDone)
	}()
	select {
	case <-mouseDone:
	case <-time.After(time.Second):
		t.Fatal("ForwardMouse blocked after target write error; the Bubble Tea update loop would freeze")
	}
}

// TestVTStalledTargetDropsNewestRepliesAtBoundedCapacity proves both sides of
// the saturation policy. The first write is already parked in resp.Write; the
// next 64 chunks fill the fixed FIFO exactly; the following 8 cursor replies and
// one mouse report are the newest and must be dropped. Every synchronous query
// and ForwardMouse call must still return because pumpReplies remains available
// to drain x/vt's unbuffered pipe.
//
// Once the target resumes, the writer receives the in-flight chunk followed by
// the 64 queued chunks in FIFO order, but none of the 8 overflow chunks. A
// distinct cursor-position query then proves forwarding recovers after the
// backlog drains.
func TestVTStalledTargetDropsNewestRepliesAtBoundedCapacity(t *testing.T) {
	const (
		replyFIFOSize = 64
		overflow      = 8
	)
	w := newGatedRecordingWriter(replyFIFOSize + 2)
	vt := NewVT(20, 5, w)
	released := false
	defer func() {
		if !released {
			close(w.release)
		}
		_ = vt.Close()
	}()

	if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
		t.Fatalf("initial device-status query: %v", err)
	}
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("reply forwarding never parked in the target writer")
	}

	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("enable mouse with stalled target: %v", err)
	}
	for i := 0; i < replyFIFOSize; i++ {
		if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
			t.Fatalf("device-status query %d with stalled target: %v", i+1, err)
		}
	}
	for i := 0; i < overflow; i++ {
		// Cursor replies are observably different from the retained status
		// replies. If saturation evicted the oldest chunks instead of dropping
		// these newest ones, the assertion below would see one here.
		if err := writeVTWithin(vt, "\x1b[2;7H\x1b[6n", time.Second); err != nil {
			t.Fatalf("overflow cursor-position query %d with stalled target: %v", i+1, err)
		}
	}
	uiDone := make(chan struct{})
	go func() {
		vt.ForwardMouse(MouseEvent{X: 4, Y: 2, Button: MouseButtonLeft, Action: MouseActionPress})
		vt.Render(20, 5)
		close(uiDone)
	}()
	select {
	case <-uiDone:
	case <-time.After(time.Second):
		t.Fatal("ForwardMouse or Render blocked with a saturated reply FIFO; the Bubble Tea UI would freeze")
	}

	close(w.release)
	released = true

	for i := 0; i < 1+replyFIFOSize; i++ {
		select {
		case got := <-w.writes:
			if want := "\x1b[?0n"; string(got) != want {
				t.Fatalf("forwarded reply %d = %q, want %q", i+1, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("received only %d of %d retained replies", i, 1+replyFIFOSize)
		}
	}

	if err := writeVTWithin(vt, "\x1b[3;4H\x1b[6n", time.Second); err != nil {
		t.Fatalf("cursor-position query after target resumed: %v", err)
	}
	select {
	case got := <-w.writes:
		if want := "\x1b[3;4R"; string(got) != want {
			t.Fatalf("first reply after retained FIFO = %q, want %q; overflow replies were not dropped newest", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarding did not recover after the stalled target resumed")
	}
}

// TestVTSaturatedReplyDrainDoesNotBlockOnLogging guards the drain's other
// output boundary. Once the target writer parks and the 64-chunk FIFO fills,
// dropping a chunk must not synchronously write telemetry: the process logger
// is an arbitrary io.Writer too, and a slow log file would otherwise replace
// the PTY deadlock with the same deadlock one line later. Repeated mouse reports
// would also amplify one stalled target into unbounded log traffic.
func TestVTSaturatedReplyDrainDoesNotBlockOnLogging(t *testing.T) {
	const replyFIFOSize = 64

	oldLogOutput := log.Writer()
	blockedLog := newBlockingWriter()
	log.SetOutput(blockedLog)

	target := newBlockingWriter()
	vt := NewVT(20, 5, target)
	defer func() {
		// log.Logger holds its mutex across Output.Write, so unblock that Write
		// before asking SetOutput to take the mutex and restore global state.
		close(blockedLog.release)
		log.SetOutput(oldLogOutput)
		close(target.release)
		_ = vt.Close()
	}()

	if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
		t.Fatalf("initial device-status query: %v", err)
	}
	select {
	case <-target.entered:
	case <-time.After(time.Second):
		t.Fatal("reply forwarding never parked in the target writer")
	}

	for i := 0; i < replyFIFOSize; i++ {
		if err := writeVTWithin(vt, "\x1b[5n", time.Second); err != nil {
			t.Fatalf("device-status query %d while filling FIFO: %v", i+1, err)
		}
	}
	// This reply is the first newest chunk dropped at capacity. Against the
	// regression it parks the sole emulator reader in log.Printf.
	if err := writeVTWithin(vt, "\x1b[2;7H\x1b[6n", time.Second); err != nil {
		t.Fatalf("first overflow query: %v", err)
	}

	select {
	case <-blockedLog.entered:
		// Continue: the next query below proves whether parking the logger also
		// parked the sole emulator reader.
	case <-time.After(100 * time.Millisecond):
		// No synchronous drop log is the desired implementation.
	}
	if err := writeVTWithin(vt, "\x1b[3;4H\x1b[6n", time.Second); err != nil {
		t.Fatalf("query after saturated drop: %v; drop telemetry blocked the emulator drain", err)
	}
	select {
	case <-blockedLog.entered:
		t.Fatal("saturated reply drop synchronously wrote to the process logger")
	default:
	}
}

// TestVTRepliesStayOrderedWhenTargetIsHealthy pins the property the bounded
// hand-off must preserve: the one forwarding goroutine writes reply chunks in
// exactly the order x/vt produced them.
func TestVTRepliesStayOrderedWhenTargetIsHealthy(t *testing.T) {
	w := newGatedRecordingWriter(3)
	close(w.release)
	vt := NewVT(20, 5, w)
	defer vt.Close() //nolint:errcheck

	queries := []string{
		"\x1b[5n",
		"\x1b[3;4H\x1b[6n",
		"\x1b[2;7H\x1b[6n",
	}
	want := []string{
		"\x1b[?0n",
		"\x1b[3;4R",
		"\x1b[2;7R",
	}
	for i, query := range queries {
		if err := writeVTWithin(vt, query, time.Second); err != nil {
			t.Fatalf("query %d: %v", i+1, err)
		}
	}
	for i, expected := range want {
		select {
		case got := <-w.writes:
			if string(got) != expected {
				t.Fatalf("reply %d = %q, want %q", i+1, got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("reply %d did not arrive", i+1)
		}
	}
}

// TestVTCloseCanBeCalledMoreThanOnce protects the shutdown signal used by the
// reply writer: repeated ownership cleanup must not close the same channel
// twice and panic.
func TestVTCloseCanBeCalledMoreThanOnce(t *testing.T) {
	vt := NewVT(10, 2, io.Discard)
	if err := vt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := vt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
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
