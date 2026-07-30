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
	// goroutine, Render runs on bubbletea's Update/View goroutine, and
	// ForwardMouse runs there too. Without a lock spanning each full
	// operation a Write landing mid-Render would tear a frame across old and
	// new cells, and Resize could race the render loop's own cols/rows read.
	mu         sync.Mutex
	term       *vt.Emulator
	cols, rows int

	// done is closed once pumpReplies returns, so Close can block until the
	// goroutine NewVT started is actually gone rather than leaking it.
	done chan struct{}
}

// NewVT creates an emulator of the given size. resp, if non-nil, receives the
// terminal's replies to queries (cursor-position reports, device attributes)
// and any mouse events ForwardMouse turns into bytes — wire it to the
// target's PTY input so query-driven TUIs don't hang and forwarded clicks
// reach the child.
//
// The pump goroutine always runs, even when resp is nil: x/vt's Read is
// pull-based (backed by an io.Pipe), and SendMouse/query replies write into
// that pipe synchronously — with nothing ever reading the other end, that
// write blocks forever and wedges the emulator. Draining unconditionally
// (and discarding when resp is nil) is what keeps "no reply target" from
// turning into "first query hangs the whole thing."
func NewVT(cols, rows int, resp io.Writer) *VT {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	v := &VT{term: vt.NewEmulator(cols, rows), cols: cols, rows: rows, done: make(chan struct{})}
	go v.pumpReplies(resp)
	return v
}

// pumpReplies drains the emulator's outgoing byte stream into resp (when
// resp is non-nil) until the emulator is closed. Read blocks on an empty pipe
// rather than returning zero bytes, which is exactly what lets this
// goroutine sit idle between replies instead of busy-looping.
func (v *VT) pumpReplies(resp io.Writer) {
	defer close(v.done)
	buf := make([]byte, 4096)
	for {
		n, err := v.term.Read(buf)
		if n > 0 && resp != nil {
			if _, werr := resp.Write(buf[:n]); werr != nil {
				log.Printf("Encountered %v while forwarding terminal reply to target PTY. Bytes: %d", werr, n)
				return
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

// closeGrace bounds how long Close waits for the reply pump to exit. The wait
// is normally instant — term.Close unblocks a pump parked in Read immediately —
// so this only ever elapses in the case it exists for, where the pump cannot
// be woken at all.
const closeGrace = 250 * time.Millisecond

// Close stops the emulator — unblocking a pending Read with io.EOF — and waits
// for the reply pump to exit, so callers don't leak the goroutine NewVT
// started.
//
// The wait is bounded because term.Close can only reach a pump parked in Read.
// pumpReplies has a second parking spot: resp.Write, into the target's PTY,
// which is where it sits whenever a stalled child has stopped draining its own
// pty. Nothing Close does from here wakes a goroutine blocked there, so an
// unbounded wait turned "the child wedged" into "quitting the TUI wedges too" —
// the operator's one remaining escape hatch, gone.
//
// Bounding the wait treats the symptom. The cause-level fix would be a write
// deadline on the pty, but darwin rejects SetWriteDeadline on a pty master
// outright ("file type does not support deadline"), so there is nothing to set.
// Past the grace period the pump is abandoned: it is blocked writing to a
// descriptor the process is about to drop, and shutting down beats reclaiming
// it. The emulator's error is returned either way — whether the pump got out is
// not the caller's business.
func (v *VT) Close() error {
	err := v.term.Close()
	select {
	case <-v.done:
	case <-time.After(closeGrace):
	}
	return err
}

func (v *VT) Write(p []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.term.Write(p)
}

// Resize changes the emulated grid.
func (v *VT) Resize(cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
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
