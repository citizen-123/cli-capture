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
// for the left and right panes, using the exact expressions View() lays them
// out with (paneStyle().Width/Height, then lipgloss.JoinHorizontal). Both
// View() and mouse hit-testing call this instead of each keeping their own
// copy of the geometry, so they can't drift apart.
// The two boxes must sum to EXACTLY m.width. Sizing by content and adding the
// border afterwards (the old `m.width/2 - 1`, then +2 per pane) produced a row
// m.width+2 wide: lipgloss puts a border column on each side of each pane, so
// two panes cost four border columns, not two. The terminal then wrapped every
// pane line, which pushed the layout down the screen and left mouse Y
// coordinates pointing at rows other than the ones actually drawn.
func (m Model) paneRects() (left, right rect) {
	boxH := m.height - 1 // the status bar owns the last row
	leftW := m.width / 2
	// An odd terminal width can't split evenly; give the spare column to the
	// right pane rather than letting the pair overflow by one.
	left = rect{X: 0, Y: 0, W: leftW, H: boxH}
	right = rect{X: leftW, Y: 0, W: m.width - leftW, H: boxH}
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

// onMouse dispatches a mouse event to whichever pane it landed in.
func (m Model) onMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width == 0 {
		return m, nil // nothing has been sized/drawn yet
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

// onRightPaneWheel scrolls the open detail viewport, or otherwise moves the
// flow-list selection exactly the way the j/k keys do (see onKey).
func (m Model) onRightPaneWheel(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
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

// trafficRowLayout mirrors renderTraffic's row accounting: how many rows sit
// above the scrollable flow list (the header, plus the filter line when it's
// shown) and how many rows the list itself gets once the header/filter/paused
// prompt have taken their share of h. renderTraffic and clickedFlowIndex both
// call this so the two can't disagree about where a row lands on screen.
func trafficRowLayout(h int, filterLineShown, pausedShown bool) (listTop, listRows int) {
	listTop = 1 // header
	reserved := 1
	if filterLineShown {
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
// clicks on the header/filter rows or past the end of the (possibly short)
// visible list — never a panic, just "nothing there."
func flowRowIndex(y, h, nVis, selected int, filterLineShown, pausedShown bool) (idx int, ok bool) {
	listTop, listRows := trafficRowLayout(h, filterLineShown, pausedShown)
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
	return flowRowIndex(y, h, len(m.visible()), m.selected, filterLineShown, m.paused != nil)
}
