package terminal

import (
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// VT is a full VT100/xterm terminal emulator backed by charmbracelet/x/vt: a
// real cell grid — alternate screen buffer, absolute cursor positioning,
// scroll regions — so full-screen TUI targets (vim, htop, claude) render
// correctly. This is the only Emulator implementation.
type VT struct {
	// mu serializes every call into term. Unlike vt10x (which this replaced),
	// x/vt's Emulator does no locking of its own: Write runs on the PTY pump
	// goroutine, while Render and input forwarding run on Bubble Tea's
	// Update/View goroutine. Without a lock spanning each full operation a
	// Write landing mid-Render would tear a frame across old and new cells,
	// and Resize could race the render loop's own cols/rows read.
	mu                sync.Mutex
	term              *vt.Emulator
	replyInput        io.Closer
	cols, rows        int
	applicationCursor bool
	bracketedPaste    bool
	closed            bool
	closeErr          error

	// replies separates the emulator's sole pipe reader from the target PTY
	// writer. It is deliberately bounded: see pumpReplies for the overflow
	// policy.
	replies chan []byte

	// stop wakes forwardReplies when Close begins. The lifecycle fields above
	// make closing it safe when ownership cleanup calls Close more than once.
	stop chan struct{}

	// drainDone and writerDone let Close wait for both owned goroutines without
	// spawning another waiter that could itself leak behind a blocked writer.
	drainDone  chan struct{}
	writerDone chan struct{}
}

// replyFIFOSize retains a normal burst of terminal replies while bounding the
// memory a child that has stopped reading its PTY can consume.
const replyFIFOSize = 64

// NewVT creates an emulator of the given size. resp, if non-nil, receives the
// terminal's replies to queries and mouse input emitted by the emulator — wire
// it to the target's PTY input so query-driven TUIs don't hang and forwarded
// clicks reach the child.
//
// The pump goroutine always runs, even when resp is nil: x/vt's Read is
// pull-based (backed by an io.Pipe), and query/mouse replies write into that
// pipe synchronously. Draining unconditionally prevents a query or mouse event
// from wedging the emulator when no response writer is configured.
func NewVT(cols, rows int, resp io.Writer) *VT {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	term := vt.NewEmulator(cols, rows)
	replyInput, ok := term.InputPipe().(io.Closer)
	if !ok {
		panic("terminal: x/vt input pipe is not closable")
	}
	v := &VT{
		term:       term,
		replyInput: replyInput,
		cols:       cols,
		rows:       rows,
		stop:       make(chan struct{}),
		drainDone:  make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	// x/vt remains the source of truth for terminal modes. Its callbacks keep
	// the two input-affecting modes available under VT.mu so encoding can stay
	// synchronous with the caller's direct PTY write.
	term.SetCallbacks(vt.Callbacks{
		EnableMode: func(mode ansi.Mode) {
			switch mode {
			case ansi.ModeCursorKeys:
				v.applicationCursor = true
			case ansi.ModeBracketedPaste:
				v.bracketedPaste = true
			}
		},
		DisableMode: func(mode ansi.Mode) {
			switch mode {
			case ansi.ModeCursorKeys:
				v.applicationCursor = false
			case ansi.ModeBracketedPaste:
				v.bracketedPaste = false
			}
		},
	})
	if resp != nil {
		v.replies = make(chan []byte, replyFIFOSize)
		go v.forwardReplies(resp)
	} else {
		// There is no forwarding goroutine in the discard case, so mark its
		// lifecycle complete at construction.
		close(v.writerDone)
	}
	go v.pumpReplies()
	return v
}

// pumpReplies is the sole reader of the emulator's outgoing byte stream. Read
// blocks on an empty pipe rather than returning zero bytes, which is exactly
// what lets this goroutine sit idle between replies instead of busy-looping.
//
// x/vt backs that stream with an unbuffered io.Pipe. Query handlers and mouse
// forwarding synchronously write the other end, so this loop must never wait
// for the child to drain its PTY: doing so makes the next producer wait for
// this reader while that producer holds VT.mu, freezing Bubble Tea's only
// update/render loop.
//
// Each read is copied into a fixed FIFO for forwardReplies. When all 64 slots
// are occupied, the newest whole read chunk is dropped; retained chunks are
// never displaced or reordered. Dropping a reply for a child that is already
// not reading is preferable to letting that child indefinitely block the UI.
// When resp is nil, replies are discarded directly without starting a second
// goroutine.
func (v *VT) pumpReplies() {
	defer close(v.drainDone)
	buf := make([]byte, 4096)
	for {
		n, err := v.term.Read(buf)
		if n > 0 && v.replies != nil {
			reply := append([]byte(nil), buf[:n]...)
			select {
			case v.replies <- reply:
			default:
				// Drop silently. Logging here would synchronously call another
				// arbitrary io.Writer from the sole emulator-pipe reader, recreating
				// the deadlock this non-blocking hand-off exists to prevent. A mouse
				// event storm would also turn saturation into log amplification.
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("Encountered %v while reading terminal emulator output. Stopping reply pump.", err)
			}
			return
		}
	}
}

// forwardReplies is the only goroutine that writes to resp, preserving the
// order pumpReplies observed whenever the target is healthy. A write error ends
// forwarding but cannot end pumpReplies, so later emulator writes still have
// their required pipe reader. Likewise, a blocked write parks only this one
// goroutine: it holds neither VT.mu nor the emulator pipe reader.
func (v *VT) forwardReplies(resp io.Writer) {
	defer close(v.writerDone)
	for {
		// Prefer cancellation over taking queued work when Close has already
		// begun. The second check covers stop closing at the same time the
		// receive below becomes ready.
		select {
		case <-v.stop:
			return
		default:
		}
		select {
		case reply := <-v.replies:
			select {
			case <-v.stop:
				return
			default:
			}
			n, err := resp.Write(reply)
			if err == nil && n != len(reply) {
				err = io.ErrShortWrite
			}
			if err != nil {
				log.Printf("Encountered %v while forwarding terminal reply to target PTY. Bytes: %d", err, len(reply))
				return
			}
		case <-v.stop:
			return
		}
	}
}

// closeGrace bounds how long Close waits for the reply goroutines to exit. The
// wait is normally instant — closing the emulator's reply input unblocks
// pumpReplies and stop unblocks forwardReplies — so this only elapses when an
// arbitrary resp.Writer is already blocked inside Write and provides no
// cancellation mechanism.
const closeGrace = 250 * time.Millisecond

// Close stops the emulator, signals the reply writer, and waits for both owned
// goroutines to exit. Repeated calls are safe.
//
// The wait is bounded because no operation on VT can wake an arbitrary
// io.Writer already blocked in resp.Write. That one goroutine owns no locks and
// no unbounded queue, but waiting for it forever would turn a wedged child into
// a wedged quit — taking away the operator's final escape hatch.
//
// A write deadline would cancel the actual PTY write, but darwin rejects
// SetWriteDeadline on a PTY master ("file type does not support deadline").
// The stop channel therefore cancels every writer state under our control; past
// the grace period, only the single in-progress external Write can remain.
//
// Unlike a goroutine-per-write timeout, this design can never accumulate an
// unbounded number of abandoned goroutines or reorder writes.
func (v *VT) Close() error {
	v.mu.Lock()
	if !v.closed {
		v.closed = true
		close(v.stop)
		// x/vt's Close cannot be used here until charmbracelet/x#879/#881 is
		// fixed: it races its plain closed bool against pumpReplies' concurrent
		// Read. InputPipe is the same io.PipeWriter Close closes, so closing it
		// directly provides the real shutdown signal without touching that
		// unsynchronized flag.
		v.closeErr = v.replyInput.Close()
	}
	err := v.closeErr
	v.mu.Unlock()

	timer := time.NewTimer(closeGrace)
	defer timer.Stop()

	drainDone, writerDone := v.drainDone, v.writerDone
	for drainDone != nil || writerDone != nil {
		select {
		case <-drainDone:
			drainDone = nil
		case <-writerDone:
			writerDone = nil
		case <-timer.C:
			return err
		}
	}
	return err
}

func (v *VT) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, io.ErrClosedPipe
	}
	return v.term.Write(p)
}

// Resize changes the emulated grid.
func (v *VT) Resize(cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	v.resizeLocked(cols, rows)
}

// resizeLocked is Resize's body, split out so Render (which already holds
// mu) can resize without recursively locking a non-reentrant mutex.
func (v *VT) resizeLocked(cols, rows int) {
	if cols < 1 || rows < 1 || (cols == v.cols && rows == v.rows) {
		return
	}
	v.cols, v.rows = cols, rows
	v.term.Resize(cols, rows)
}

// Render returns the current screen clipped to width×height, with SGR
// escapes so colors and attributes survive into the pane. It walks CellAt
// directly — Emulator.Render renders the whole screen unclipped, which isn't
// what a fixed-size pane needs — and uses Style.Diff to emit only the SGR
// delta between adjacent cells, the same approach x/vt's own Line.Render
// uses internally.
func (v *VT) Render(width, height int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ""
	}
	v.resizeLocked(width, height)

	cols, rows := v.cols, v.rows
	if width > 0 && width < cols {
		cols = width
	}
	if height > 0 && height < rows {
		rows = height
	}

	var b strings.Builder
	var pen uv.Style // the style already emitted; Diff computes the delta from this
	for y := 0; y < rows; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			cell := v.term.CellAt(x, y)
			if cell == nil || cell.IsZero() {
				// Out of bounds, or the second column of a wide rune: x/vt
				// leaves those as the zero Cell, and the terminal itself
				// advances two columns for the wide glyph, so nothing needs
				// to be written here.
				continue
			}
			if seq := cell.Style.Diff(&pen); seq != "" {
				b.WriteString(seq)
				pen = cell.Style
			}
			b.WriteString(cell.Content)
		}
	}
	if !pen.IsZero() {
		b.WriteString(ansi.ResetStyle)
	}
	return b.String()
}

// CursorKeyBytes encodes an arrow from x/vt's callback-maintained DECCKM state.
// Returning bytes keeps keyboard input on the caller's single synchronous PTY
// write path rather than mixing it into the bounded asynchronous reply FIFO.
func (v *VT) CursorKeyBytes(key CursorKey) []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	var final byte
	switch key {
	case CursorUp:
		final = 'A'
	case CursorDown:
		final = 'B'
	case CursorRight:
		final = 'C'
	case CursorLeft:
		final = 'D'
	default:
		return nil
	}
	if v.applicationCursor {
		return []byte{0x1b, 'O', final}
	}
	return []byte{0x1b, '[', final}
}

// PasteBytes encodes pasted text from x/vt's callback-maintained DECSET 2004
// state without putting user input through the droppable reply FIFO.
func (v *VT) PasteBytes(text string) []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	if !v.bracketedPaste {
		return []byte(text)
	}
	b := make([]byte, 0, len(ansi.BracketedPasteStart)+len(text)+len(ansi.BracketedPasteEnd))
	b = append(b, ansi.BracketedPasteStart...)
	b = append(b, text...)
	b = append(b, ansi.BracketedPasteEnd...)
	return b
}

// mouseButtons maps this package's neutral MouseButton to x/vt's, so
// ForwardMouse is a lookup rather than a re-derivation of xterm's button
// numbering.
var mouseButtons = map[MouseButton]vt.MouseButton{
	MouseButtonNone:       vt.MouseNone,
	MouseButtonLeft:       vt.MouseLeft,
	MouseButtonMiddle:     vt.MouseMiddle,
	MouseButtonRight:      vt.MouseRight,
	MouseButtonWheelUp:    vt.MouseWheelUp,
	MouseButtonWheelDown:  vt.MouseWheelDown,
	MouseButtonWheelLeft:  vt.MouseWheelLeft,
	MouseButtonWheelRight: vt.MouseWheelRight,
	MouseButtonBackward:   vt.MouseBackward,
	MouseButtonForward:    vt.MouseForward,
	MouseButton10:         vt.MouseButton10,
	MouseButton11:         vt.MouseButton11,
}

// isWheelButton reports whether b is one of the four scroll-wheel buttons,
// which xterm's protocol always reports as a button press — never a release
// or a motion — regardless of what MouseAction the event carried.
func isWheelButton(b MouseButton) bool {
	switch b {
	case MouseButtonWheelUp, MouseButtonWheelDown, MouseButtonWheelLeft, MouseButtonWheelRight:
		return true
	default:
		return false
	}
}

// ForwardMouse translates ev into the terminal's native mouse-reporting
// protocol and feeds it to the emulator. SendMouse gates itself on the child
// having enabled a mouse-tracking DECSET mode — emitting nothing at all if it
// hasn't — so this never needs to check first. The bytes it does produce flow
// out through the same pumpReplies goroutine and resp writer that carry the
// terminal's own query replies; ForwardMouse itself never touches resp.
func (v *VT) ForwardMouse(ev MouseEvent) {
	button, ok := mouseButtons[ev.Button]
	if !ok {
		return // a button this package doesn't know about
	}

	var mod uv.KeyMod
	if ev.Shift {
		mod |= uv.ModShift
	}
	if ev.Alt {
		mod |= uv.ModAlt
	}
	if ev.Ctrl {
		mod |= uv.ModCtrl
	}
	m := uv.Mouse{X: ev.X, Y: ev.Y, Button: button, Mod: mod}

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	switch {
	case isWheelButton(ev.Button):
		v.term.SendMouse(uv.MouseWheelEvent(m))
	case ev.Action == MouseActionRelease:
		v.term.SendMouse(uv.MouseReleaseEvent(m))
	case ev.Action == MouseActionMotion:
		v.term.SendMouse(uv.MouseMotionEvent(m))
	default: // MouseActionPress
		v.term.SendMouse(uv.MouseClickEvent(m))
	}
}
