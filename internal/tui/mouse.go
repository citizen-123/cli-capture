package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/terminal"
)

// rect is a half-open screen region: columns [X, X+W), rows [Y, Y+H). Mouse
// hit-testing works in absolute terminal coordinates (what tea.MouseMsg.X/Y
// report), so rects are anchored at the top-left of the whole screen, not a
// pane's own content.
type rect struct {
	X, Y, W, H int
}

func (r rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// paneRects returns the on-screen bounding boxes — lipgloss border included —
// for the left and right panes. Both View() (via paneBoxWidths → leftPaneWidth
// / rightPaneWidth) and mouse hit-testing derive their geometry from the same
// paneBoxWidths split, so a click always maps to the row actually drawn — and
// the two boxes sum to exactly m.width, at whatever splitRatio the operator has
// dragged the divider to.
func (m Model) paneRects() (left, right rect) {
	boxH := m.height - 1 // the status bar owns the last row
	leftBox, rightBox := m.paneBoxWidths()
	left = rect{X: 0, Y: 0, W: leftBox, H: boxH}
	right = rect{X: leftBox, Y: 0, W: rightBox, H: boxH}
	return left, right
}

// contentGrid returns the interior grid (cols, rows) a pane box's content
// renders into: the border (2 cols, 2 rows) comes off first, then the small
// margin View() leaves at the pane's right and bottom edge (see leftSize's
// callers — m.screen.Render and renderTraffic are both sized this way).
// Hit-testing divides a click up the same way so it never drifts from what's
// actually drawn.
func contentGrid(box rect) (cols, rows int) {
	paneW, paneH := box.W-2, box.H-2
	return paneW - 2, paneH - 1
}

// onMouse dispatches a mouse event to whichever pane it landed in — after the
// modal states have had their say.
//
// The gate below mirrors the precedence Update's tea.KeyMsg branch already
// establishes, because a mouse event is no less modal than a keystroke. Where
// it splits the states differently is on what View() actually draws:
//
//   - showHelp and repeating paint over the WHOLE screen, so the panes are not
//     on it and a click has nothing behind the overlay to hit. Both are handled
//     here, and both route the wheel to the thing that is on screen.
//   - filtering and cmdline leave both panes drawn, but a focused text input
//     owns the input stream — no keystroke reaches a pane while one is open, so
//     no click should either.
//   - editing and viewing replace only the RIGHT pane's content; the terminal
//     pane is still drawn and still live. They stay pane-local in
//     onRightPaneMouse rather than swallowing input meant for the left pane.
func (m Model) onMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width == 0 {
		return m, nil // nothing has been sized/drawn yet
	}
	switch {
	case m.showHelp:
		return m.onHelpMouse(ev)
	case m.repeating:
		return m.onRepeaterMouse(ev)
	case m.filtering, m.cmdline:
		return m, nil
	}
	left, right := m.paneRects()
	switch {
	case left.contains(ev.X, ev.Y):
		return m.onLeftPaneMouse(ev, left)
	case right.contains(ev.X, ev.Y):
		return m.onRightPaneMouse(ev, right)
	}
	return m, nil // the status bar row, or the sliver past the two panes
}

// wheelLines is how many lines one wheel notch moves an overlay that scrolls
// by line. It matches the delta bubbles' viewport uses by default, so the help
// overlay, the detail view and the raw editor all scroll at the same rate.
const wheelLines = 3

// onHelpMouse handles mouse input while the help overlay is up. The wheel
// scrolls it, clamped exactly the way onHelpKey clamps j/k — the body runs well
// past a screen, so scrolling is what makes the ':' commands near the bottom
// reachable at all, and the wheel is the obvious way to ask for it. Everything
// else is swallowed: the overlay is the only thing drawn, so a click has
// nothing to land on and must not disturb the panes hidden behind it.
func (m Model) onHelpMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ev.Action != tea.MouseActionPress {
		return m, nil
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		m.helpScroll -= wheelLines
	case tea.MouseButtonWheelDown:
		m.helpScroll += wheelLines
	default:
		return m, nil
	}
	return m.clampHelpScroll(), nil
}

// onRepeaterMouse handles mouse input while the Repeater modal is up. Like the
// help overlay it owns the whole screen, so clicks are swallowed; the wheel
// goes to whichever section has focus, which is the same fall-through
// onRepeaterKey uses for keys it doesn't claim. textarea forwards MouseMsg to
// its private viewport and then keeps the logical cursor visible, so Request
// and Payload scroll without changing where the next character is inserted.
func (m Model) onRepeaterMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !tea.MouseEvent(ev).IsWheel() {
		return m, nil
	}
	var cmd tea.Cmd
	switch m.rep.focus {
	case repFocusReq:
		m.rep.req, cmd = m.rep.req.Update(ev)
	case repFocusPayload:
		m.rep.payload, cmd = m.rep.payload.Update(ev)
	case repFocusResp:
		m.rep.respVP, cmd = m.rep.respVP.Update(ev)
	}
	return m, cmd
}

// onLeftPaneMouse focuses the terminal pane and forwards the event to the
// emulator, translated to coordinates relative to the pane's content area.
// Whether the child actually asked for mouse input — and how it gets
// encoded — is the emulator's call (see terminal.Emulator.ForwardMouse); the
// TUI's only job is the hit-testing and the coordinate translation.
func (m Model) onLeftPaneMouse(ev tea.MouseMsg, box rect) (tea.Model, tea.Cmd) {
	m.focus = focusTerminal
	if m.target == nil || m.screen == nil {
		return m, nil // no child to forward into yet, or it has already exited
	}

	cols, rows := contentGrid(box)
	col := ev.X - box.X - 1 // -1 for the left border
	row := ev.Y - box.Y - 1 // -1 for the top border
	if col < 0 || row < 0 || col >= cols || row >= rows {
		return m, nil // landed on the border or the unused content margin
	}

	if key, ok := wheelPageKey(ev); ok {
		if m.target.Pty == nil {
			return m, nil // target constructed but not started
		}
		if _, err := m.target.Pty.Write(key); err != nil {
			m.status = "scroll: " + err.Error()
		}
		return m, nil
	}

	if me, ok := toMouseEvent(ev, col, row); ok {
		m.screen.ForwardMouse(me)
	}
	return m, nil
}

// Page-key sequences, in the form a child on the alternate screen expects.
var (
	keyPageUp   = []byte("\x1b[5~")
	keyPageDown = []byte("\x1b[6~")
)

// wheelPageKey turns a vertical wheel notch over the terminal pane into a
// PgUp/PgDn keypress instead of a mouse-wheel report.
//
// Forwarding the wheel verbatim is technically the faithful thing to do, and it
// is what a child with mouse tracking on receives from a normal terminal — but
// full-screen targets then scroll a line or so per notch, which reads like
// tapping the arrow keys and takes forever to get anywhere in a long
// transcript. Sending page keys makes the wheel move a screenful, which is what
// scrolling a hosted app is actually for.
//
// Only the vertical wheel is remapped. Clicks, drags and horizontal wheel still
// forward untouched, so a child's clickable UI keeps working.
func wheelPageKey(ev tea.MouseMsg) ([]byte, bool) {
	// Wheel notches arrive as presses; ignore the release/motion companions so
	// one notch doesn't page twice.
	if ev.Action != tea.MouseActionPress {
		return nil, false
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		return keyPageUp, true
	case tea.MouseButtonWheelDown:
		return keyPageDown, true
	}
	return nil, false
}

// mouseButtons maps bubbletea's mouse button vocabulary onto the terminal
// package's neutral one — the one place tea.MouseMsg crosses into the
// terminal package's types, so terminal itself never needs to import
// bubbletea.
var mouseButtons = map[tea.MouseButton]terminal.MouseButton{
	tea.MouseButtonNone:       terminal.MouseButtonNone,
	tea.MouseButtonLeft:       terminal.MouseButtonLeft,
	tea.MouseButtonMiddle:     terminal.MouseButtonMiddle,
	tea.MouseButtonRight:      terminal.MouseButtonRight,
	tea.MouseButtonWheelUp:    terminal.MouseButtonWheelUp,
	tea.MouseButtonWheelDown:  terminal.MouseButtonWheelDown,
	tea.MouseButtonWheelLeft:  terminal.MouseButtonWheelLeft,
	tea.MouseButtonWheelRight: terminal.MouseButtonWheelRight,
	tea.MouseButtonBackward:   terminal.MouseButtonBackward,
	tea.MouseButtonForward:    terminal.MouseButtonForward,
	tea.MouseButton10:         terminal.MouseButton10,
	tea.MouseButton11:         terminal.MouseButton11,
}

// toMouseEvent translates a bubbletea mouse message, already known to have
// landed inside the terminal pane's content area at (col, row), into the
// terminal package's neutral MouseEvent. ok is false for events with nothing
// worth forwarding: no button held and nothing moving.
func toMouseEvent(ev tea.MouseMsg, col, row int) (terminal.MouseEvent, bool) {
	button, ok := mouseButtons[ev.Button]
	if !ok {
		return terminal.MouseEvent{}, false // a button cli-capture doesn't map
	}
	if button == terminal.MouseButtonNone && ev.Action != tea.MouseActionMotion {
		return terminal.MouseEvent{}, false // no button held and nothing moving: nothing to forward
	}

	action := terminal.MouseActionPress
	switch ev.Action {
	case tea.MouseActionRelease:
		action = terminal.MouseActionRelease
	case tea.MouseActionMotion:
		action = terminal.MouseActionMotion
	}

	return terminal.MouseEvent{
		X: col, Y: row,
		Button: button,
		Action: action,
		Shift:  ev.Shift,
		Alt:    ev.Alt,
		Ctrl:   ev.Ctrl,
	}, true
}

// onRightPaneMouse focuses the traffic pane, and either scrolls (wheel) or
// selects the flow row under the cursor (click).
func (m Model) onRightPaneMouse(ev tea.MouseMsg, box rect) (tea.Model, tea.Cmd) {
	m.focus = focusTraffic

	cols, rows := contentGrid(box)
	col := ev.X - box.X - 1
	row := ev.Y - box.Y - 1
	if col < 0 || row < 0 || col >= cols || row >= rows {
		return m, nil // border or content margin, not a real hit
	}

	if tea.MouseEvent(ev).IsWheel() {
		return m.onRightPaneWheel(ev)
	}
	if ev.Action != tea.MouseActionPress {
		return m, nil // only presses select a row; ignore motion/release
	}
	if m.editing || m.viewing {
		return m, nil // the pane is showing the editor/detail view, not the list
	}
	if idx, ok := m.clickedFlowIndex(row, rows); ok {
		// First click on a row selects it; clicking the row that is already
		// selected opens it, the same path "enter" takes. bubbletea reports no
		// click count, so click-again is how you get open-on-click without
		// hand-rolling double-click timing — and it keeps "just move the
		// selection" available, which a plain open-on-first-click would lose.
		if idx == m.selected {
			m.openDetail()
		}
		m.selected = idx
	}
	return m, nil
}

// onRightPaneWheel scrolls whichever overlay the right pane is currently
// showing, or otherwise moves the flow-list selection exactly the way the j/k
// keys do (see onKey). The editing/viewing checks mirror the click path's
// guard: with an overlay drawn over the flow list, moving the selection
// underneath it is a change the operator cannot see.
func (m Model) onRightPaneWheel(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		return m.wheelEditor(ev)
	}
	if m.viewing {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(ev)
		return m, cmd
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		if m.selected > 0 {
			m.selected--
		}
	case tea.MouseButtonWheelDown:
		if m.selected < len(m.visible())-1 {
			m.selected++
		}
	}
	return m, nil
}

// wheelEditor scrolls the raw editor without moving its insertion point.
//
// bubbles' textarea forwards mouse messages to its private viewport, then
// repositions that viewport only as needed to keep the logical cursor visible.
// Using its native path therefore scrolls around an interior caret without
// changing where the next character is inserted.
func (m Model) wheelEditor(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(ev)
	return m, cmd
}

// trafficRowLayout mirrors renderTraffic's row accounting: how many rows sit
// above the scrollable flow list (the header, plus the filter line and the ':'
// command line when either is shown) and how many rows the list itself gets
// once the header/filter/cmdline/paused prompt have taken their share of h.
// renderTraffic and clickedFlowIndex both call this so the two can't disagree
// about where a row lands on screen.
func trafficRowLayout(h int, filterLineShown, cmdlineShown, pausedShown bool) (listTop, listRows int) {
	listTop = 1 // header
	reserved := 1
	if filterLineShown {
		listTop++
		reserved++
	}
	if cmdlineShown {
		listTop++
		reserved++
	}
	if pausedShown {
		reserved += 2
	}
	listRows = h - reserved
	if listRows < 1 {
		listRows = 1
	}
	return listTop, listRows
}

// flowRowIndex maps a click at content row y to an index into a visible-flows
// slice of length nVis, replicating the scroll window renderTraffic computes
// so a click always lands on the row the user actually sees. ok is false for
// clicks on the header/filter/cmdline rows or past the end of the (possibly
// short) visible list — never a panic, just "nothing there."
func flowRowIndex(y, h, nVis, selected int, filterLineShown, cmdlineShown, pausedShown bool) (idx int, ok bool) {
	listTop, listRows := trafficRowLayout(h, filterLineShown, cmdlineShown, pausedShown)
	if y < listTop {
		return 0, false
	}
	sel := clampIndex(selected, nVis)
	start := 0
	if sel >= listRows {
		start = sel - listRows + 1
	}
	row := y - listTop
	if row < 0 || row >= listRows {
		return 0, false
	}
	idx = start + row
	if idx < 0 || idx >= nVis {
		return 0, false
	}
	return idx, true
}

// clickedFlowIndex is flowRowIndex bound to this model's current state.
func (m Model) clickedFlowIndex(y, h int) (idx int, ok bool) {
	filterLineShown := m.filtering || m.fi.Value() != ""
	return flowRowIndex(y, h, len(m.visible()), m.selected, filterLineShown, m.cmdline, m.paused != nil)
}
