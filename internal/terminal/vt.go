package terminal

import (
	"io"
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"
)

// vt10x attribute-mode bits (from the emulator's internal glyph mode).
const (
	attrReverse   = 1 << 0
	attrUnderline = 1 << 1
	attrBold      = 1 << 2
	attrItalic    = 1 << 4
)

// VT is a full VT100/xterm terminal emulator backed by hinshun/vt10x. Unlike the
// line-oriented Screen, it maintains a real cell grid — alternate screen buffer,
// absolute cursor positioning, scroll regions — so full-screen TUI targets (vim,
// htop, claude) render correctly. This is the default emulator.
type VT struct {
	term       vt10x.Terminal
	cols, rows int
}

// NewVT creates an emulator of the given size. resp, if non-nil, receives the
// terminal's replies to queries (cursor-position reports, device attributes) —
// wire it to the target's PTY input so query-driven TUIs don't hang.
func NewVT(cols, rows int, resp io.Writer) *VT {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	opts := []vt10x.TerminalOption{vt10x.WithSize(cols, rows)}
	if resp != nil {
		opts = append(opts, vt10x.WithWriter(resp))
	}
	return &VT{term: vt10x.New(opts...), cols: cols, rows: rows}
}

func (v *VT) Write(p []byte) (int, error) { return v.term.Write(p) }

// Resize changes the emulated grid. vt10x guards its own state, so this is safe
// alongside concurrent Write/Render.
func (v *VT) Resize(cols, rows int) {
	if cols < 1 || rows < 1 || (cols == v.cols && rows == v.rows) {
		return
	}
	v.cols, v.rows = cols, rows
	v.term.Resize(cols, rows)
}

// Render returns the current screen clipped to width×height, with SGR escapes so
// colors and attributes survive into the pane.
func (v *VT) Render(width, height int) string {
	v.Resize(width, height)

	v.term.Lock()
	defer v.term.Unlock()
	cols, rows := v.term.Size()
	if width > 0 && width < cols {
		cols = width
	}
	if height > 0 && height < rows {
		rows = height
	}

	var b strings.Builder
	// Sentinels that never equal a real color/mode, so the first cell emits SGR.
	prevFG, prevBG := vt10x.Color(0xffffffff), vt10x.Color(0xfffffffe)
	prevMode := int16(-1)
	for y := 0; y < rows; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			g := v.term.Cell(x, y)
			if g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
				b.WriteString(sgr(g))
				prevFG, prevBG, prevMode = g.FG, g.BG, g.Mode
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// sgr builds the escape sequence for a glyph's attributes and colors, resetting
// first so no attribute leaks from the previous cell.
func sgr(g vt10x.Glyph) string {
	parts := []string{"0"}
	if g.Mode&attrBold != 0 {
		parts = append(parts, "1")
	}
	if g.Mode&attrUnderline != 0 {
		parts = append(parts, "4")
	}
	if g.Mode&attrItalic != 0 {
		parts = append(parts, "3")
	}
	if g.Mode&attrReverse != 0 {
		parts = append(parts, "7")
	}
	parts = append(parts, colorSGR(g.FG, true)...)
	parts = append(parts, colorSGR(g.BG, false)...)
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// colorSGR maps a vt10x color to SGR parameters. Default colors emit nothing
// (the reset already restored the terminal default).
func colorSGR(c vt10x.Color, fg bool) []string {
	if c >= 1<<24 { // DefaultFG/DefaultBG/DefaultCursor
		return nil
	}
	if c < 16 { // the 16 ANSI colors
		base := 30
		if !fg {
			base = 40
		}
		if c >= 8 { // bright variants use the 90/100 range
			base += 60
			c -= 8
		}
		return []string{strconv.Itoa(base + int(c))}
	}
	// 16–255: xterm 256-color palette
	if fg {
		return []string{"38", "5", strconv.Itoa(int(c))}
	}
	return []string{"48", "5", strconv.Itoa(int(c))}
}
