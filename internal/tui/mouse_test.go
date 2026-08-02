package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/terminal"
)

// fakeEmulator is a minimal terminal.Emulator stand-in that records every
// ForwardMouse call, so mouse-forwarding tests can check the TUI's own job —
// hit-testing and coordinate translation — without spinning up a real VT and
// caring whether the child has enabled mouse tracking (that gate now lives
// entirely in terminal.VT; see its own tests).
type fakeEmulator struct {
	forwarded []terminal.MouseEvent
}

func (f *fakeEmulator) Write(p []byte) (int, error)         { return len(p), nil }
func (f *fakeEmulator) Render(width, height int) string     { return "" }
func (f *fakeEmulator) Resize(cols, rows int)               {}
func (f *fakeEmulator) ForwardMouse(ev terminal.MouseEvent) { f.forwarded = append(f.forwarded, ev) }

func newFlows(n int) []*capture.Flow {
	flows := make([]*capture.Flow, 0, n)
	for i := 0; i < n; i++ {
		f := capture.NewFlow("c", fmt.Sprintf("host%02d:443", i))
		f.Request = &capture.Message{Meta: map[string]string{}}
		flows = append(flows, f)
	}
	return flows
}

// --- geometry ---

// TestPaneRectsAdjoinAndMatchContentGrid pins the relationship the rest of
// the mouse code leans on: the two panes sit flush against each other, they
// differ by at most the one column an odd terminal width cannot split, and
// contentGrid(left) always agrees with leftSize() — the function the
// PTY/emulator resize path already depended on before mouse support existed.
//
// This used to require the panes be exactly equal, which is precisely the
// assumption that forced the layout to overflow: at an odd width equal panes
// can only be had by rendering a column too many. Widths must sum exactly —
// see TestPaneBoxesExactlyFillTheWidth.
func TestPaneRectsAdjoinAndMatchContentGrid(t *testing.T) {
	sizes := []struct{ width, height int }{
		{100, 40}, {81, 24}, {60, 20}, {200, 60}, {40, 10},
	}
	for _, sz := range sizes {
		m := Model{width: sz.width, height: sz.height, splitRatio: 0.5}
		left, right := m.paneRects()
		if right.X != left.X+left.W {
			t.Errorf("width=%d height=%d: right pane doesn't start where left ends (left=%+v right=%+v)",
				sz.width, sz.height, left, right)
		}
		if d := left.W - right.W; d < -1 || d > 1 {
			t.Errorf("width=%d height=%d: panes differ by %d columns, want at most 1, got left=%+v right=%+v",
				sz.width, sz.height, d, left, right)
		}
		if left.H != right.H {
			t.Errorf("width=%d height=%d: panes should match in height, got left=%+v right=%+v",
				sz.width, sz.height, left, right)
		}
		if got := left.W + right.W; got != sz.width {
			t.Errorf("width=%d: panes sum to %d, want exactly %d — anything more wraps the row",
				sz.width, got, sz.width)
		}
		lw, lh := contentGrid(left)
		wantLW, wantLH := m.leftSize()
		if lw != wantLW || lh != wantLH {
			t.Errorf("width=%d height=%d: contentGrid(left)=(%d,%d), leftSize()=(%d,%d); must match",
				sz.width, sz.height, lw, lh, wantLW, wantLH)
		}
	}
}

// --- flow-row hit testing ---

func TestFlowRowIndex(t *testing.T) {
	tests := []struct {
		name                         string
		y, h, nVis, selected         int
		filterLineShown, pausedShown bool
		wantIdx                      int
		wantOK                       bool
	}{
		{"header row is never a hit", 0, 10, 3, 0, false, false, 0, false},
		{"first list row", 1, 10, 3, 0, false, false, 0, true},
		{"last real row", 3, 10, 3, 0, false, false, 2, true},
		{"past the end of a short list", 4, 10, 3, 0, false, false, 0, false},
		{"way past the end doesn't panic", 9, 10, 3, 0, false, false, 0, false},
		{"filter line eats the row above the list", 1, 10, 3, 0, true, false, 0, false},
		{"list shifts down one row when filtering", 2, 10, 3, 0, true, false, 0, true},
		{"paused prompt shrinks available rows", 8, 10, 10, 0, false, true, 0, false},
		{"one row before the paused-shrunk cutoff is still valid", 7, 10, 10, 0, false, true, 6, true},
		{"scrolled window follows a far-down selection", 1, 10, 30, 25, false, false, 17, true},
		{"scrolled window: bottom visible row is the selection", 9, 10, 30, 25, false, false, 25, true},
		{"scrolled window: one past the bottom", 10, 10, 30, 25, false, false, 0, false},
		{"empty list never hits", 1, 10, 0, 0, false, false, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, ok := flowRowIndex(tc.y, tc.h, tc.nVis, tc.selected, tc.filterLineShown, false, tc.pausedShown)
			if ok != tc.wantOK || (ok && idx != tc.wantIdx) {
				t.Errorf("flowRowIndex(y=%d,h=%d,nVis=%d,selected=%d,filter=%v,paused=%v) = (%d,%v), want (%d,%v)",
					tc.y, tc.h, tc.nVis, tc.selected, tc.filterLineShown, tc.pausedShown, idx, ok, tc.wantIdx, tc.wantOK)
			}
		})
	}
}

// --- left pane: forward-to-child translation ---

// TestOnLeftPaneMouseForwardsTranslatedCoordinates checks the part of mouse
// forwarding that's still the TUI's job now that gating on the child's
// opt-in lives entirely in terminal.VT: hit-testing the click into the
// pane's content area and translating it to the emulator's 0-based
// coordinate space.
func TestOnLeftPaneMouseForwardsTranslatedCoordinates(t *testing.T) {
	screen := &fakeEmulator{}
	m := Model{
		width:      100,
		height:     40,
		splitRatio: 0.5,
		fi:         newFilter(),
		vp:         viewport.New(0, 0),
		screen:     screen,
		target:     &runner.Target{},
	}
	left, _ := m.paneRects()
	ev := tea.MouseMsg{X: left.X + 2, Y: left.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}

	newModel, cmd := m.Update(ev)
	if cmd != nil {
		t.Errorf("mouse click should not produce a command, got %v", cmd)
	}
	if got := newModel.(Model).focus; got != focusTerminal {
		t.Errorf("clicking the left pane should focus the terminal, got focus=%v", got)
	}

	if len(screen.forwarded) != 1 {
		t.Fatalf("want exactly one forwarded event, got %d: %+v", len(screen.forwarded), screen.forwarded)
	}
	want := terminal.MouseEvent{X: 1, Y: 1, Button: terminal.MouseButtonLeft, Action: terminal.MouseActionPress}
	if got := screen.forwarded[0]; got != want {
		t.Errorf("forwarded event = %+v, want %+v", got, want)
	}
}

// TestOnLeftPaneMouseBorderClickDoesNotForward guards the hit-testing: a
// click that lands on the pane's border (not its content area) must still
// focus the terminal pane, but must never reach the emulator.
func TestOnLeftPaneMouseBorderClickDoesNotForward(t *testing.T) {
	screen := &fakeEmulator{}
	m := Model{
		width:      100,
		height:     40,
		splitRatio: 0.5,
		fi:         newFilter(),
		vp:         viewport.New(0, 0),
		screen:     screen,
		target:     &runner.Target{},
	}
	left, _ := m.paneRects()
	ev := tea.MouseMsg{X: left.X, Y: left.Y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft} // top-left border cell

	newModel, _ := m.Update(ev)
	if got := newModel.(Model).focus; got != focusTerminal {
		t.Errorf("clicking the left pane's border should still focus the terminal, got focus=%v", got)
	}
	if len(screen.forwarded) != 0 {
		t.Errorf("border click should not forward anything, got %+v", screen.forwarded)
	}
}

// TestOnLeftPaneMouseNilTargetDoesNotPanic guards the target==nil case: the
// terminal pane can be clicked before a child has been wired up (or after it
// exits), and that must never crash the TUI.
func TestOnLeftPaneMouseNilTargetDoesNotPanic(t *testing.T) {
	m := Model{width: 100, height: 40, splitRatio: 0.5, fi: newFilter(), vp: viewport.New(0, 0)}
	left, _ := m.paneRects()
	ev := tea.MouseMsg{X: left.X + 2, Y: left.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}

	newModel, _ := m.Update(ev) // must not panic
	if got := newModel.(Model).focus; got != focusTerminal {
		t.Errorf("clicking the left pane should focus the terminal even with no target, got focus=%v", got)
	}
}

// --- right pane: row selection ---

// TestOnMouseRightPaneClickSelectsRow checks a click maps to the flow under
// the cursor using the exact same row accounting renderTraffic draws with.
func TestOnMouseRightPaneClickSelectsRow(t *testing.T) {
	m := Model{width: 100, height: 40, splitRatio: 0.5, fi: newFilter(), vp: viewport.New(0, 0), flows: newFlows(5)}
	_, right := m.paneRects()

	const wantIdx = 2
	// Content row for flow i (no filter line, no paused prompt, no scroll) is
	// listTop(1) + i; add the pane's own border (+1) to get an absolute row.
	ev := tea.MouseMsg{
		X:      right.X + 2,
		Y:      right.Y + 1 + 1 + wantIdx,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	}

	newModel, cmd := m.Update(ev)
	if cmd != nil {
		t.Errorf("row click should not produce a command, got %v", cmd)
	}
	got := newModel.(Model)
	if got.focus != focusTraffic {
		t.Errorf("clicking the right pane should focus traffic, got focus=%v", got.focus)
	}
	if got.selected != wantIdx {
		t.Errorf("selected = %d, want %d", got.selected, wantIdx)
	}
}

// TestOnMouseRightPaneClickPastEndDoesNotPanicOrSelect is the explicit
// "clicking past the end of the list" requirement: no panic, no selection
// change.
func TestOnMouseRightPaneClickPastEndDoesNotPanicOrSelect(t *testing.T) {
	m := Model{width: 100, height: 40, splitRatio: 0.5, fi: newFilter(), vp: viewport.New(0, 0), flows: newFlows(2), selected: 0}
	_, right := m.paneRects()
	_, rows := contentGrid(right)

	ev := tea.MouseMsg{
		X:      right.X + 2,
		Y:      right.Y + 1 + rows - 1, // last content row of the pane, far past 2 flows
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	}

	newModel, _ := m.Update(ev) // must not panic
	if got := newModel.(Model).selected; got != 0 {
		t.Errorf("clicking past the end of the list should leave selection unchanged, got %d", got)
	}
}

// --- right pane: wheel ---

func TestOnMouseRightPaneWheelMovesSelectionClamped(t *testing.T) {
	m := Model{width: 100, height: 40, splitRatio: 0.5, fi: newFilter(), vp: viewport.New(0, 0), flows: newFlows(3), selected: 0}
	_, right := m.paneRects()
	wheel := func(btn tea.MouseButton) tea.MouseMsg {
		return tea.MouseMsg{X: right.X + 2, Y: right.Y + 2, Action: tea.MouseActionPress, Button: btn}
	}

	next, _ := m.Update(wheel(tea.MouseButtonWheelDown))
	if got := next.(Model).selected; got != 1 {
		t.Fatalf("wheel down: selected = %d, want 1", got)
	}

	atTop, _ := m.Update(wheel(tea.MouseButtonWheelUp))
	if got := atTop.(Model).selected; got != 0 {
		t.Errorf("wheel up at the top should clamp at 0, got %d", got)
	}

	atEnd := m
	atEnd.selected = 2 // len(flows)-1
	past, _ := atEnd.Update(wheel(tea.MouseButtonWheelDown))
	if got := past.(Model).selected; got != 2 {
		t.Errorf("wheel down at the end should clamp at len-1, got %d", got)
	}
}

// --- modal gating ---

// modalModel builds a sized model with a 5-flow list and every input
// constructed, ready for a test to raise one modal flag on it. focus starts at
// focusTerminal and selected at 0, so any leak through the modal gate shows up
// as one of those changing.
func modalModel(screen *fakeEmulator) Model {
	m := Model{
		width:      100,
		height:     40,
		splitRatio: 0.5,
		fi:         newFilter(),
		ci:         newCommandLine(),
		ta:         newEditor(),
		vp:         viewport.New(0, 0),
		rep:        repeaterState{respVP: viewport.New(0, 0)},
		flows:      newFlows(5),
		target:     &runner.Target{},
	}
	// Assigned only when non-nil, rather than unconditionally. m.screen is an
	// interface, so a nil *fakeEmulator stored in it makes a NON-nil interface
	// holding a nil pointer — onLeftPaneMouse's `m.screen == nil` guard would
	// wave it through and ForwardMouse would panic on the nil receiver. Leaving
	// the field alone keeps the guard working for callers that pass nil.
	if screen != nil {
		m.screen = screen
	}
	return m
}

// editingModel builds a model with the raw editor open over the right pane,
// seeded with body and the cursor parked on its first line.
//
// The Focus() call is load-bearing rather than cosmetic: bubbles' textarea
// short-circuits Update entirely while blurred, so an unfocused editor silently
// ignores everything sent to it and a scroll assertion would fail for a reason
// that has nothing to do with the wheel. enterEdit and enterInject are the only
// two places that set m.editing, and both focus the textarea, so this mirrors
// the only state the app can actually reach.
func editingModel(body string) Model {
	m := modalModel(nil)
	m.editing = true
	m.ta.SetWidth(40)
	m.ta.SetHeight(5)
	m.ta.SetValue(body)
	m.ta.Focus()
	for m.ta.Line() > 0 {
		m.ta.CursorUp()
	}
	return m
}

// TestModalStatesSwallowClicksBehindThem is finding 1: before this gate,
// tea.MouseMsg was dispatched straight to the panes without consulting a single
// modal flag, so a click behind an overlay silently moved the selection — or
// reached the child — under a screen that isn't showing either pane.
//
// The states here are the ones that own the whole screen (help, repeater) or
// own the input stream (filter, ':' command line), matching the precedence
// Update's tea.KeyMsg branch already enforces for keystrokes.
func TestModalStatesSwallowClicksBehindThem(t *testing.T) {
	modals := []struct {
		name string
		set  func(*Model)
	}{
		{"help overlay", func(m *Model) { m.showHelp = true }},
		{"repeater", func(m *Model) { m.repeating = true }},
		{"filter input", func(m *Model) { m.filtering = true }},
		{"command line", func(m *Model) { m.cmdline = true }},
	}
	for _, modal := range modals {
		t.Run(modal.name, func(t *testing.T) {
			base := modalModel(nil)
			left, right := base.paneRects()
			clicks := []struct {
				where string
				x, y  int
			}{
				{"left pane", left.X + 2, left.Y + 2},
				{"right pane flow row", right.X + 2, right.Y + 3},
			}
			for _, click := range clicks {
				screen := &fakeEmulator{}
				m := modalModel(screen)
				modal.set(&m)

				ev := tea.MouseMsg{X: click.x, Y: click.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
				newModel, _ := m.Update(ev)
				got := newModel.(Model)

				if got.focus != focusTerminal {
					t.Errorf("%s: click on the %s changed focus to %v; nothing behind a modal may be clicked",
						modal.name, click.where, got.focus)
				}
				if got.selected != 0 {
					t.Errorf("%s: click on the %s moved the selection to %d, want 0",
						modal.name, click.where, got.selected)
				}
				if len(screen.forwarded) != 0 {
					t.Errorf("%s: click on the %s reached the child: %+v",
						modal.name, click.where, screen.forwarded)
				}
			}
		})
	}
}

// TestHelpOverlayWheelScrolls is the routing half of the help gate: the overlay
// swallows clicks, but the body runs well past a screen, so the wheel has to
// scroll it the way j/k do in onHelpKey — including the same clamp at the top.
func TestHelpOverlayWheelScrolls(t *testing.T) {
	m := modalModel(nil)
	m.showHelp = true
	wheel := func(btn tea.MouseButton) tea.MouseMsg {
		return tea.MouseMsg{X: 10, Y: 10, Action: tea.MouseActionPress, Button: btn}
	}

	down, _ := m.Update(wheel(tea.MouseButtonWheelDown))
	if got := down.(Model).helpScroll; got != wheelLines {
		t.Errorf("wheel down over the help overlay: helpScroll = %d, want %d", got, wheelLines)
	}

	atTop, _ := m.Update(wheel(tea.MouseButtonWheelUp))
	if got := atTop.(Model).helpScroll; got != 0 {
		t.Errorf("wheel up at the top of the help overlay should clamp at 0, got %d", got)
	}
}

// TestHelpOverlayWheelClampsAtBottom pins the other end of the clamp, so a
// determined scroll can't run the window past the end of the body and render a
// short (or empty) overlay.
func TestHelpOverlayWheelClampsAtBottom(t *testing.T) {
	m := modalModel(nil)
	m.showHelp = true
	max := m.maxHelpScroll(m.height)
	// Without this the test passes vacuously on a terminal tall enough to show
	// the whole help body: max would be 0, the scroll would already be at the
	// bottom, and "it didn't move" would prove nothing about the clamp.
	if max == 0 {
		t.Fatal("help body fits this test terminal, so there is no clamp to observe; shorten m.height")
	}
	m.helpScroll = max

	ev := tea.MouseMsg{X: 10, Y: 10, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
	newModel, _ := m.Update(ev)
	if got := newModel.(Model).helpScroll; got != max {
		t.Errorf("wheel down at the bottom of the help overlay: helpScroll = %d, want it clamped at %d", got, max)
	}
}

// TestEditorWheelScrollsEditorNotTrafficList is finding 2: the click path
// guarded on m.editing but the wheel path guarded only on m.viewing, so a notch
// over the open editor moved the flow selection hidden underneath it. The wheel
// now walks the editor instead.
func TestEditorWheelScrollsEditorNotTrafficList(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i)
	}
	m := editingModel(strings.Join(lines, "\n"))

	_, right := m.paneRects()
	ev := tea.MouseMsg{X: right.X + 2, Y: right.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}

	newModel, _ := m.Update(ev)
	got := newModel.(Model)
	if got.ta.Line() != wheelLines {
		t.Errorf("wheel down over the open editor: cursor line = %d, want %d — the editor did not scroll",
			got.ta.Line(), wheelLines)
	}
	if got.selected != 0 {
		t.Errorf("wheel over the open editor moved the flow selection to %d; the list is behind the editor", got.selected)
	}
}

// TestEditorWheelUpClampsAtFirstLine checks the wheel can't walk the cursor off
// the top of the editor.
func TestEditorWheelUpClampsAtFirstLine(t *testing.T) {
	m := editingModel("line 0\nline 1\nline 2")

	_, right := m.paneRects()
	ev := tea.MouseMsg{X: right.X + 2, Y: right.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}

	newModel, _ := m.Update(ev)
	if got := newModel.(Model).ta.Line(); got != 0 {
		t.Errorf("wheel up at the first line of the editor: cursor line = %d, want 0", got)
	}
}

// TestOnMouseRightPaneWheelScrollsViewportWhenViewing checks the detail-view
// override: with the viewer open, wheel events scroll m.vp instead of moving
// the flow-list selection underneath it.
func TestOnMouseRightPaneWheelScrollsViewportWhenViewing(t *testing.T) {
	vp := viewport.New(10, 3)
	vp.SetContent(strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8"}, "\n"))
	m := Model{width: 100, height: 40, splitRatio: 0.5, fi: newFilter(), vp: vp, viewing: true, flows: newFlows(3), selected: 0}
	_, right := m.paneRects()
	ev := tea.MouseMsg{X: right.X + 2, Y: right.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}

	newModel, _ := m.Update(ev)
	got := newModel.(Model)
	if got.vp.YOffset == 0 {
		t.Error("wheel over an open detail view should scroll the viewport, YOffset is still 0")
	}
	if got.selected != 0 {
		t.Errorf("scrolling the viewport must not change the flow selection, got selected=%d", got.selected)
	}
}
