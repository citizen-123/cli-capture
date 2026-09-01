// Package terminal renders a child PTY's output stream into text the left
// pane can display, and turns stateful keyboard, paste, and mouse input back
// into the protocol selected by the child.
//
// VT, the sole implementation, wraps charmbracelet/x/vt: a real cell-grid
// VT100/xterm emulator with absolute cursor positioning, scroll regions, and
// an alternate screen buffer, so full-screen TUI targets (vim, htop, claude)
// render correctly — not just simple line-oriented shell output.
//
// The package exposes that behavior through the Emulator interface rather
// than the concrete type. That seam is what made swapping the underlying
// emulator library (hinshun/vt10x → charmbracelet/x/vt, when the former went
// unmaintained) a contained change: internal/tui only ever depends on
// Emulator, never on the library underneath it.
package terminal

// CursorKey identifies an unmodified cursor key whose byte sequence depends on
// the child-controlled DECCKM mode.
type CursorKey int

const (
	CursorUp CursorKey = iota
	CursorDown
	CursorRight
	CursorLeft
)

// Emulator consumes PTY bytes and renders a viewport of the given size, and
// turns input over that viewport back into the protocol selected by the
// emulated child.
type Emulator interface {
	Write(p []byte) (int, error)
	Render(width, height int) string
	// Resize informs the emulator of the terminal grid size.
	Resize(cols, rows int)
	// CursorKeyBytes returns an unmodified arrow encoded as CSI or SS3
	// according to the child-controlled DECCKM mode.
	CursorKeyBytes(key CursorKey) []byte
	// PasteBytes restores bracketed-paste markers only when the child enabled
	// DECSET 2004.
	PasteBytes(text string) []byte
	// Close stops the emulator and its reply pump.
	Close() error
	// ForwardMouse translates a mouse event landing on the pane's content
	// grid into the terminal's native mouse-reporting protocol and feeds it
	// to the emulated child. Whether anything is actually sent — and in
	// which encoding — is entirely the emulator's call: a child that never
	// opted into mouse tracking via DECSET gets nothing at all, so callers
	// don't need to ask first whether forwarding is safe.
	ForwardMouse(ev MouseEvent)
}
