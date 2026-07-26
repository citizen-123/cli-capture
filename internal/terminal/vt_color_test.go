package terminal

import (
	"strings"
	"testing"
)

// TestVTTruecolorReachesThePane drives a real truecolor SGR through the
// emulator. This is the regression the vt10x → x/vt migration exists for:
// vt10x packed 24-bit colors into the same numeric space as palette indices,
// so this cell used to render as "38;5;16737280" — a palette index 16M out of
// range, which terminals drop, leaving the text default-white. x/vt's Style
// keeps a color.Color (a distinct Go type per color kind — see
// TestVTPaletteColorIsDistinctFromTruecolor) instead of one shared uint32, so
// the ambiguity can't recur.
func TestVTTruecolorReachesThePane(t *testing.T) {
	vt := NewVT(20, 2, nil)
	if _, err := vt.Write([]byte("\x1b[38;2;255;100;0mORANGE\x1b[0m")); err != nil {
		t.Fatalf("Write: %v", err)
	}

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

// TestVTTruecolorBackground covers the 48;2 path, which has its own branch in
// the SGR parser.
func TestVTTruecolorBackground(t *testing.T) {
	vt := NewVT(20, 2, nil)
	if _, err := vt.Write([]byte("\x1b[48;2;10;20;30mBG\x1b[0m")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := vt.Render(20, 2)
	if !strings.Contains(out, "48;2;10;20;30") {
		t.Errorf("truecolor background SGR missing from render: %q", out)
	}
}

// TestVTNearBlackTruecolorSurvives is the concrete case the old vt10x-backed
// code could never represent: a truecolor triple that packs to a value under
// 256 (e.g. rgb(0,0,200) packs to 200) is numerically indistinguishable from
// a palette index in vt10x's shared-uint32 encoding, so it always rendered as
// palette color 200 instead of a near-black blue. x/vt keeps truecolor and
// palette as distinct Go types (color.RGBA vs ansi.IndexedColor), so this
// must now round-trip as truecolor.
func TestVTNearBlackTruecolorSurvives(t *testing.T) {
	vt := NewVT(20, 2, nil)
	if _, err := vt.Write([]byte("\x1b[38;2;0;0;200mBLUE\x1b[0m")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := vt.Render(20, 2)
	if !strings.Contains(out, "38;2;0;0;200") {
		t.Errorf("near-black truecolor missing from render, want 38;2;0;0;200: %q", out)
	}
	if strings.Contains(out, "38;5;200") {
		t.Errorf("near-black truecolor collapsed to palette index 200: %q", out)
	}
	if !strings.Contains(stripANSI(out), "BLUE") {
		t.Errorf("text lost: %q", stripANSI(out))
	}
}

// TestVTPaletteColorIsDistinctFromTruecolor pins the 256-color (indexed)
// path, which must keep working exactly as it did before: an xterm palette
// index is not a truecolor triple and the render must say so with 38;5, not
// 38;2.
func TestVTPaletteColorIsDistinctFromTruecolor(t *testing.T) {
	vt := NewVT(20, 2, nil)
	if _, err := vt.Write([]byte("\x1b[38;5;200mX\x1b[0m")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := vt.Render(20, 2)
	if !strings.Contains(out, "38;5;200") {
		t.Errorf("palette SGR missing from render: %q", out)
	}
	if strings.Contains(out, "38;2;") {
		t.Errorf("palette index rendered as truecolor: %q", out)
	}
}
