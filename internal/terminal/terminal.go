// Package terminal renders a child PTY's output stream into text the left
// pane can display, and turns mouse events over that pane back into whatever
// mouse-reporting protocol the child itself asked for.
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

// Emulator consumes PTY bytes and renders a viewport of the given size, and
// turns mouse events over that viewport into whatever the emulated child
// actually asked for.
type Emulator interface {
	Write(p []byte) (int, error)
	Render(width, height int) string
	// Resize informs the emulator of the terminal grid size.
	Resize(cols, rows int)
	// ForwardMouse translates a mouse event landing on the pane's content
	// grid into the terminal's native mouse-reporting protocol and feeds it
	// to the emulated child. Whether anything is actually sent — and in
	// which encoding — is entirely the emulator's call: a child that never
	// opted into mouse tracking via DECSET gets nothing at all, so callers
	// don't need to ask first whether forwarding is safe.
	ForwardMouse(ev MouseEvent)
}
