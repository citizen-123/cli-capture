package terminal

import (
	"os"
	"strings"
	"testing"
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
