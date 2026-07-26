package tui

import (
	"fmt"
	"log"

	tea "github.com/charmbracelet/bubbletea"
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
func (m Model) paneRects() (left, right rect) {
	paneW := m.width/2 - 1
	paneH := m.height - 3
	boxW, boxH := paneW+2, paneH+2 // + lipgloss's border on every side
	left = rect{X: 0, Y: 0, W: boxW, H: boxH}
	right = rect{X: boxW, Y: 0, W: boxW, H: boxH}
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

// onLeftPaneMouse focuses the terminal pane and, only when the child has
// opted into SGR mouse reporting, forwards the event into its PTY translated
// to coordinates relative to the pane's content area.
func (m Model) onLeftPaneMouse(ev tea.MouseMsg, box rect) (tea.Model, tea.Cmd) {
	m.focus = focusTerminal
	if m.target == nil || m.screen == nil || !m.screen.MouseEnabled() {
		return m, nil // the child never asked for mouse input
	}
	if !m.screen.MouseSGR() {
		// Legacy X10 mouse encoding caps coordinates at 223 and packs the
		// button into a single byte with a different offset than SGR; it's
		// out of scope here, so a child that enables mouse tracking without
		// also asking for SGR (mode 1006) gets nothing forwarded.
		return m, nil
	}

	cols, rows := contentGrid(box)
	col := ev.X - box.X - 1 // -1 for the left border
	row := ev.Y - box.Y - 1 // -1 for the top border
	if col < 0 || row < 0 || col >= cols || row >= rows {
		return m, nil // landed on the border or the unused content margin
	}

	b := encodeSGRMouse(ev, col+1, row+1) // SGR coordinates are 1-based
	if b == nil {
		return m, nil
	}
	if _, err := m.target.Pty.Write(b); err != nil {
		log.Printf("Encountered error while forwarding mouse event to target PTY. Error: %v, Event: %s", err, ev)
	}
	return m, nil
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

// encodeSGRMouse turns a bubbletea mouse event into the SGR (xterm mode 1006)
// escape sequence a target expects on stdin: ESC [ < Cb ; Cx ; Cy M (press or
// motion) or m (release), with Cx/Cy 1-based. Returns nil when the event
// carries nothing worth forwarding (an extra button cli-capture doesn't map,
// or bare motion with no button held).
func encodeSGRMouse(ev tea.MouseMsg, col, row int) []byte {
	const (
		bitShift  = 0b0000_0100
		bitAlt    = 0b0000_1000
		bitCtrl   = 0b0001_0000
		bitMotion = 0b0010_0000
		bitWheel  = 0b0100_0000
		bitAdd    = 0b1000_0000
	)

	var cb int
	switch ev.Button {
	case tea.MouseButtonLeft:
		cb = 0
	case tea.MouseButtonMiddle:
		cb = 1
	case tea.MouseButtonRight:
		cb = 2
	case tea.MouseButtonWheelUp:
		cb = bitWheel | 0
	case tea.MouseButtonWheelDown:
		cb = bitWheel | 1
	case tea.MouseButtonWheelLeft:
		cb = bitWheel | 2
	case tea.MouseButtonWheelRight:
		cb = bitWheel | 3
	case tea.MouseButtonBackward:
		cb = bitAdd | 0
	case tea.MouseButtonForward:
		cb = bitAdd | 1
	case tea.MouseButtonNone:
		if ev.Action != tea.MouseActionMotion {
			return nil // no button held and nothing moving: nothing to encode
		}
		cb = 3 // "no button" motion, per the SGR/X10 convention
	default:
		return nil // buttons 10/11 aren't mapped; nothing sane to forward
	}

	if ev.Action == tea.MouseActionMotion && !tea.MouseEvent(ev).IsWheel() {
		cb |= bitMotion
	}
	if ev.Shift {
		cb |= bitShift
	}
	if ev.Alt {
		cb |= bitAlt
	}
	if ev.Ctrl {
		cb |= bitCtrl
	}

	final := byte('M')
	if ev.Action == tea.MouseActionRelease {
		final = 'm'
	}
	return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, col, row, final))
}
