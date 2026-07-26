package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestPaneBoxesExactlyFillTheWidth guards the layout bug that made mouse
// hit-testing unreliable: the two pane boxes used to sum to m.width+2, because
// each pane pays for two border columns and only one was budgeted. The terminal
// wrapped every pane row, the layout slid down the screen, and clicks landed on
// rows other than the ones drawn. Odd widths are included because that is where
// an off-by-one split shows up.
func TestPaneBoxesExactlyFillTheWidth(t *testing.T) {
	for _, w := range []int{40, 41, 80, 81, 100, 120, 121, 200, 201} {
		m := Model{width: w, height: 40}
		left, right := m.paneRects()

		if got := left.W + right.W; got != w {
			t.Errorf("width %d: boxes sum to %d, want exactly %d", w, got, w)
		}
		if left.X != 0 {
			t.Errorf("width %d: left pane starts at x=%d, want 0", w, left.X)
		}
		if right.X != left.W {
			t.Errorf("width %d: right pane starts at x=%d, want %d (no gap, no overlap)", w, right.X, left.W)
		}
	}
}

// TestRenderedRowNeverExceedsTerminalWidth measures the real thing: what
// lipgloss actually produces for the two panes at the sizes View() gives them.
// The box arithmetic above is only correct if it survives contact with the
// renderer, so this asserts against rendered cell width, not our own maths.
func TestRenderedRowNeverExceedsTerminalWidth(t *testing.T) {
	for _, w := range []int{40, 41, 80, 81, 120, 121, 200} {
		m := Model{width: w, height: 20}
		leftBox, rightBox := m.paneRects()

		left := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			Width(leftBox.W - 2).Height(leftBox.H - 2).Render("L")
		right := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			Width(rightBox.W - 2).Height(rightBox.H - 2).Render("R")

		widest := 0
		for _, line := range strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, left, right), "\n") {
			if n := ansi.StringWidth(line); n > widest {
				widest = n
			}
		}
		if widest > w {
			t.Errorf("terminal width %d: rendered row is %d cells (overflow +%d) — the terminal will wrap it",
				w, widest, widest-w)
		}
	}
}

// TestClickOnSelectedRowOpensDetail pins the two-step click: the first click on
// a row selects it, and clicking the row that is already selected opens it.
// bubbletea reports no click count, so this is how open-on-click works without
// double-click timing.
func TestClickOnSelectedRowOpensDetail(t *testing.T) {
	newModel := func() Model {
		return Model{
			width: 100, height: 40,
			fi: newFilter(), vp: viewport.New(0, 0),
			flows: newFlows(3), selected: 0,
		}
	}

	// Locate a click position that resolves to a real row, using the same
	// geometry the handler does rather than a guessed coordinate.
	m := newModel()
	_, rightBox := m.paneRects()
	_, rows := contentGrid(rightBox)

	var clickX, clickY, wantIdx int
	found := false
	for row := 0; row < rows && !found; row++ {
		if idx, ok := m.clickedFlowIndex(row, rows); ok {
			clickX, clickY, wantIdx = rightBox.X+1, rightBox.Y+1+row, idx
			found = true
		}
	}
	if !found {
		t.Fatal("no clickable flow row found — geometry helper changed?")
	}

	press := tea.MouseMsg{X: clickX, Y: clickY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}

	// First click: selects, does not open.
	m = newModel()
	m.selected = (wantIdx + 1) % 3 // start on a different row
	got, _ := m.onMouse(press)
	after := got.(Model)
	if after.selected != wantIdx {
		t.Errorf("first click selected row %d, want %d", after.selected, wantIdx)
	}
	if after.viewing {
		t.Error("first click opened the detail view; it should only select")
	}

	// Second click on the row now selected: opens.
	got2, _ := after.onMouse(press)
	after2 := got2.(Model)
	if !after2.viewing {
		t.Error("clicking the already-selected row did not open the detail view")
	}
}

// TestClickFocusesThePaneItLandsIn covers the other half of the report: a click
// must move keyboard focus, not just the mouse's notion of a target.
func TestClickFocusesThePaneItLandsIn(t *testing.T) {
	m := Model{width: 100, height: 40, fi: newFilter(), vp: viewport.New(0, 0), flows: newFlows(3)}
	left, right := m.paneRects()

	m.focus = focusTerminal
	got, _ := m.onMouse(tea.MouseMsg{X: right.X + 2, Y: right.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got.(Model).focus != focusTraffic {
		t.Error("clicking the traffic pane did not take keyboard focus")
	}

	m.focus = focusTraffic
	got, _ = m.onMouse(tea.MouseMsg{X: left.X + 2, Y: left.Y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got.(Model).focus != focusTerminal {
		t.Error("clicking the terminal pane did not take keyboard focus")
	}
}
