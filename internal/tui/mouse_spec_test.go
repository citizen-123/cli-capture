package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestEncodeSGRMouseAgainstXtermSpec pins encodeSGRMouse to xterm's documented
// SGR (1006) encoding rather than to bubbletea's decoder, which is where the
// encoder was originally derived from. Deriving an encoder by inverting one
// specific decoder can reproduce that decoder's quirks; these expectations come
// from the xterm ctlseqs button-code definition instead, so the two derivations
// have to agree independently:
//
//	bits 0-1 : button (0=left, 1=middle, 2=right, 3=none)
//	bit 2  (4)  : shift
//	bit 3  (8)  : meta/alt
//	bit 4  (16) : control
//	bit 5  (32) : motion
//	bit 6  (64) : wheel — 64=up, 65=down, 66=left, 67=right
//	bit 7  (128): extended buttons — 128=button8 (back), 129=button9 (forward)
//	final byte  : 'M' press/motion, 'm' release; coordinates are 1-based
func TestEncodeSGRMouseAgainstXtermSpec(t *testing.T) {
	tests := []struct {
		name string
		ev   tea.MouseMsg
		want string
	}{
		{"left press", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, "\x1b[<0;7;3M"},
		{"left release", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}, "\x1b[<0;7;3m"},
		{"middle press", tea.MouseMsg{Button: tea.MouseButtonMiddle, Action: tea.MouseActionPress}, "\x1b[<1;7;3M"},
		{"right press", tea.MouseMsg{Button: tea.MouseButtonRight, Action: tea.MouseActionPress}, "\x1b[<2;7;3M"},

		{"wheel up", tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}, "\x1b[<64;7;3M"},
		{"wheel down", tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}, "\x1b[<65;7;3M"},
		{"wheel left", tea.MouseMsg{Button: tea.MouseButtonWheelLeft, Action: tea.MouseActionPress}, "\x1b[<66;7;3M"},
		{"wheel right", tea.MouseMsg{Button: tea.MouseButtonWheelRight, Action: tea.MouseActionPress}, "\x1b[<67;7;3M"},

		{"button8 backward", tea.MouseMsg{Button: tea.MouseButtonBackward, Action: tea.MouseActionPress}, "\x1b[<128;7;3M"},
		{"button9 forward", tea.MouseMsg{Button: tea.MouseButtonForward, Action: tea.MouseActionPress}, "\x1b[<129;7;3M"},

		{"shift+left", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Shift: true}, "\x1b[<4;7;3M"},
		{"alt+left", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Alt: true}, "\x1b[<8;7;3M"},
		{"ctrl+left", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Ctrl: true}, "\x1b[<16;7;3M"},
		{"ctrl+shift+left", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Ctrl: true, Shift: true}, "\x1b[<20;7;3M"},

		{"drag with left held", tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}, "\x1b[<32;7;3M"},
		{"motion with no button", tea.MouseMsg{Button: tea.MouseButtonNone, Action: tea.MouseActionMotion}, "\x1b[<35;7;3M"},

		// A wheel event must never pick up the motion bit — 96 would decode as
		// a drag, not a scroll, and the child would act on the wrong event.
		{"wheel never gains the motion bit", tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionMotion}, "\x1b[<64;7;3M"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeSGRMouse(tc.ev, 7, 3)
			if string(got) != tc.want {
				t.Errorf("encodeSGRMouse(%s) = %q, want %q", tc.name, string(got), tc.want)
			}
		})
	}
}

// TestEncodeSGRMouseCoordinatesAreOneBased guards the off-by-one that would put
// every forwarded click one cell up and to the left inside the child app.
func TestEncodeSGRMouseCoordinatesAreOneBased(t *testing.T) {
	got := string(encodeSGRMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, 1, 1))
	if got != "\x1b[<0;1;1M" {
		t.Errorf("top-left cell encoded as %q, want the 1-based \"\\x1b[<0;1;1M\"", got)
	}
}
