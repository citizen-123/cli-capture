// Package terminal renders a child PTY's output stream into text the left pane
// can display. A full VT100 emulator is a big dependency; this is a deliberately
// small line-oriented model that handles the common interactive-shell case
// (text, newlines, carriage returns, backspace) and passes SGR color escapes
// through so colors still render.
//
// Limitation: full-screen alt-buffer TUIs (vim, htop) that rely on absolute
// cursor positioning will not render faithfully here. The Emulator interface
// exists so a complete VT emulator can be substituted without touching the UI.
package terminal

import (
	"strings"
	"sync"
)

// Emulator consumes PTY bytes and renders a viewport of the given size, and
// turns mouse events over that viewport into whatever the emulated child
// actually asked for.
type Emulator interface {
	Write(p []byte) (int, error)
	Render(width, height int) string
	// Resize informs the emulator of the terminal grid size. Line-oriented
	// emulators may ignore it; grid emulators must track it for correct layout.
	Resize(cols, rows int)
	// ForwardMouse translates a mouse event landing on the pane's content
	// grid into the terminal's native mouse-reporting protocol and feeds it
	// to the emulated child. Whether anything is actually sent — and in
	// which encoding — is entirely the emulator's call: a child that never
	// opted into mouse tracking via DECSET gets nothing at all, so callers
	// don't need to ask first whether forwarding is safe.
	ForwardMouse(ev MouseEvent)
}

// Screen is the default Emulator: a growing scrollback of finished lines plus
// the line currently being written.
type Screen struct {
	mu       sync.Mutex
	lines    []string // completed lines (may contain SGR escapes)
	cur      strings.Builder
	maxLines int
}

func New() *Screen { return &Screen{maxLines: 5000} }

// Resize is a no-op for the line-oriented Screen; it clips to the render width.
func (s *Screen) Resize(cols, rows int) {}

// ForwardMouse is a no-op: Screen has no mode-tracking state, so it never
// asked the child anything and never has anywhere sane to forward a click.
func (s *Screen) ForwardMouse(ev MouseEvent) {}

func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < len(p); i++ {
		switch b := p[i]; b {
		case '\n':
			s.newline()
		case '\r':
			s.cur.Reset()
		case '\b':
			s.backspace()
		case 0x1b: // ESC — consume the escape sequence
			i = s.escape(p, i)
		case '\t':
			s.cur.WriteString("    ")
		default:
			if b >= 0x20 || b >= 0x80 {
				s.cur.WriteByte(b)
			}
		}
	}
	return len(p), nil
}

func (s *Screen) newline() {
	s.lines = append(s.lines, s.cur.String())
	s.cur.Reset()
	if len(s.lines) > s.maxLines {
		s.lines = s.lines[len(s.lines)-s.maxLines:]
	}
}

func (s *Screen) backspace() {
	cur := s.cur.String()
	if cur != "" {
		s.cur.Reset()
		s.cur.WriteString(cur[:len(cur)-1])
	}
}

// escape handles an escape sequence starting at p[i] (which is ESC). It returns
// the index of the last byte consumed. SGR color sequences (ending in 'm') are
// preserved inline so the terminal renders colors; all other CSI/OSC sequences
// are dropped to keep the line model intact.
func (s *Screen) escape(p []byte, i int) int {
	if i+1 >= len(p) {
		return i
	}
	switch p[i+1] {
	case '[': // CSI: ESC [ params letter
		j := i + 2
		for j < len(p) && !isFinalCSI(p[j]) {
			j++
		}
		if j < len(p) {
			if p[j] == 'm' { // SGR — keep it so colors survive
				s.cur.Write(p[i : j+1])
			}
			return j
		}
		return len(p) - 1
	case ']': // OSC: ESC ] ... BEL or ESC \
		j := i + 2
		for j < len(p) && p[j] != 0x07 {
			if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				return j + 1
			}
			j++
		}
		return j
	default:
		return i + 1
	}
}

func isFinalCSI(b byte) bool { return b >= 0x40 && b <= 0x7e }

// Render returns the last `height` lines, each clipped to `width` display cells.
func (s *Screen) Render(width, height int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := append([]string{}, s.lines...)
	if s.cur.Len() > 0 {
		all = append(all, s.cur.String())
	}
	if height > 0 && len(all) > height {
		all = all[len(all)-height:]
	}
	for i, ln := range all {
		all[i] = clip(ln, width)
	}
	return strings.Join(all, "\n")
}

// clip truncates a line to n visible columns, counting bytes inside SGR escape
// sequences as zero-width so color codes don't eat the budget.
func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	var out strings.Builder
	visible := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b { // pass an escape sequence through without counting it
			j := i + 1
			for j < len(s) && !isFinalCSI(s[j]) {
				j++
			}
			if j < len(s) {
				out.WriteString(s[i : j+1])
				i = j
			}
			continue
		}
		if visible >= n {
			break
		}
		out.WriteByte(s[i])
		visible++
	}
	return out.String()
}
