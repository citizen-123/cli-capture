package terminal

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

// TestColorSGR pins the mapping from a vt10x color to SGR parameters. The
// truecolor rows are the regression: vt10x shares one uint32 space between
// palette indices and packed 24-bit colors, so the palette branch used to
// swallow every RGB color and emit an out-of-range index.
func TestColorSGR(t *testing.T) {
	rgb := func(r, g, b int) vt10x.Color { return vt10x.Color(r<<16 | g<<8 | b) }

	tests := []struct {
		name string
		c    vt10x.Color
		fg   bool
		want []string
	}{
		{"ansi red fg", 1, true, []string{"31"}},
		{"ansi red bg", 1, false, []string{"41"}},
		{"bright red fg", 9, true, []string{"91"}},
		{"bright red bg", 9, false, []string{"101"}},
		{"palette low fg", 16, true, []string{"38", "5", "16"}},
		{"palette high fg", 255, true, []string{"38", "5", "255"}},
		{"palette high bg", 255, false, []string{"48", "5", "255"}},
		{"truecolor orange fg", rgb(255, 100, 0), true, []string{"38", "2", "255", "100", "0"}},
		{"truecolor orange bg", rgb(255, 100, 0), false, []string{"48", "2", "255", "100", "0"}},
		{"truecolor white fg", rgb(255, 255, 255), true, []string{"38", "2", "255", "255", "255"}},
		{"truecolor mid fg", rgb(1, 2, 3), true, []string{"38", "2", "1", "2", "3"}},
		{"default fg emits nothing", vt10x.DefaultFG, true, nil},
		{"default bg emits nothing", vt10x.DefaultBG, false, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := colorSGR(tc.c, tc.fg)
			if strings.Join(got, ";") != strings.Join(tc.want, ";") {
				t.Errorf("colorSGR(%d, fg=%v) = %v, want %v", tc.c, tc.fg, got, tc.want)
			}
		})
	}
}

// TestVTTruecolorReachesThePane drives a real truecolor SGR through the
// emulator. The old code turned this cell into "38;5;16737280" — a palette
// index 16M out of range, which terminals drop, leaving the text default-white.
func TestVTTruecolorReachesThePane(t *testing.T) {
	vt := NewVT(20, 2, nil)
	vt.Write([]byte("\x1b[38;2;255;100;0mORANGE\x1b[0m"))

	out := vt.Render(20, 2)
	if !strings.Contains(out, "38;2;255;100;0") {
		t.Errorf("truecolor SGR missing from render: %q", out)
	}
	if strings.Contains(out, "38;5;16737280") {
		t.Errorf("truecolor emitted as an out-of-range palette index: %q", out)
	}
	if !strings.Contains(stripANSI(out), "ORANGE") {
		t.Errorf("text lost: %q", stripANSI(out))
	}
}

// TestVTTruecolorBackground covers the 48;2 path, which has its own branch.
func TestVTTruecolorBackground(t *testing.T) {
	vt := NewVT(20, 2, nil)
	vt.Write([]byte("\x1b[48;2;10;20;30mBG\x1b[0m"))

	out := vt.Render(20, 2)
	if !strings.Contains(out, "48;2;10;20;30") {
		t.Errorf("truecolor background SGR missing from render: %q", out)
	}
}
