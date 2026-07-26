package terminal

import (
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

// TestVTAbsolutePositioning is the capability the line-oriented Screen lacked:
// a target that jumps the cursor to an absolute row/column and writes there —
// the way full-screen TUIs (claude, vim) paint — must land in the right cell.
func TestVTAbsolutePositioning(t *testing.T) {
	vt := NewVT(20, 5, nil)
	vt.Write([]byte("\x1b[2J"))        // clear screen
	vt.Write([]byte("\x1b[3;5HHELLO")) // move to row 3, col 5, then write

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
	vt.Write([]byte("\x1b[31mRED"))
	out := vt.Render(10, 2)
	if strings.Index(out, "\x1b[") < 0 {
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
	vt.Write([]byte("first"))
	vt.Write([]byte("\x1b[2J\x1b[H")) // clear, home
	vt.Write([]byte("second"))
	out := stripANSI(vt.Render(10, 2))
	if !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Errorf("after clear+rewrite, want 'second' not 'first': %q", out)
	}
}
