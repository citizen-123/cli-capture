package terminal

import (
	"testing"
)

// TestVTForwardMouseWithNilRespIsANoop guards the resp==nil construction path
// (no query-driven reply target): ForwardMouse must not panic or block just
// because there's nowhere for the bytes to go.
func TestVTForwardMouseWithNilRespIsANoop(t *testing.T) {
	vt := NewVT(20, 5, nil)
	if _, err := vt.Write([]byte("\x1b[?1000h\x1b[?1006h")); err != nil {
		t.Fatalf("Write(enable mouse): %v", err)
	}
	vt.ForwardMouse(MouseEvent{X: 0, Y: 0, Button: MouseButtonLeft, Action: MouseActionPress}) // must not panic or hang
}
