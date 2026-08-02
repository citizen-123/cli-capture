package terminal

// MouseButton identifies which button (if any) a forwarded mouse event
// concerns. The vocabulary matches xterm's mouse-reporting button codes, so
// translating it into the underlying emulator's own type is a straight
// lookup. It's defined here — rather than reusing x/vt's or bubbletea's
// button type — so internal/tui can build a MouseEvent without either the
// terminal package importing bubbletea or tui importing x/vt.
type MouseButton int

// Mouse buttons, in the order xterm's protocol numbers them.
const (
	MouseButtonNone MouseButton = iota
	MouseButtonLeft
	MouseButtonMiddle
	MouseButtonRight
	MouseButtonWheelUp
	MouseButtonWheelDown
	MouseButtonWheelLeft
	MouseButtonWheelRight
	MouseButtonBackward
	MouseButtonForward
	MouseButton10
	MouseButton11
)

// MouseAction identifies what kind of mouse event occurred: a button going
// down, coming back up, or the pointer moving (with or without a button
// held).
type MouseAction int

const (
	MouseActionPress MouseAction = iota
	MouseActionRelease
	MouseActionMotion
)

// MouseEvent is a neutral description of a mouse event landing on the
// emulator's content grid: which button, what action, which modifiers, and
// where (zero-based, matching the coordinate space Render/CellAt use).
// internal/tui translates tea.MouseMsg into this, which keeps bubbletea
// entirely out of this package.
type MouseEvent struct {
	X, Y             int
	Button           MouseButton
	Action           MouseAction
	Shift, Alt, Ctrl bool
}
