package tui

import (
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/terminal"
)

// recordingEmulator notes whether a mouse event was forwarded, so a test can
// assert that a remapped wheel notch did NOT also reach the child as a mouse
// report (which would scroll it twice, once per interpretation).
type recordingEmulator struct {
	forwarded []terminal.MouseEvent
}

func (e *recordingEmulator) Write(p []byte) (int, error) { return len(p), nil }
func (e *recordingEmulator) Render(w, h int) string      { return "" }
func (e *recordingEmulator) Resize(cols, rows int)       {}
func (e *recordingEmulator) ForwardMouse(ev terminal.MouseEvent) {
	e.forwarded = append(e.forwarded, ev)
}

// leftPaneHarness wires a Model to a real pipe standing in for the child PTY,
// and returns a reader for whatever the model writes to it.
func leftPaneHarness(t *testing.T) (Model, *os.File, *recordingEmulator) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		r.Close()
		w.Close()
	})
	emu := &recordingEmulator{}
	m := Model{
		width: 100, height: 40, splitRatio: 0.5,
		fi: newFilter(), vp: viewport.New(0, 0),
		target: &runner.Target{Pty: w},
	}
	return m, r, emu
}

// readWithin returns whatever is readable within d, or "" if nothing arrives.
func readWithin(t *testing.T, r *os.File, d time.Duration) string {
	t.Helper()
	type res struct {
		n   int
		err error
	}
	buf := make([]byte, 64)
	ch := make(chan res, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- res{n, err}
	}()
	select {
	case got := <-ch:
		if got.n > 0 {
			return string(buf[:got.n])
		}
		return ""
	case <-time.After(d):
		return ""
	}
}

// TestWheelOverTerminalPaneSendsPageKeys is the behaviour change: a wheel notch
// over the hosted app pages, rather than nudging it a line at a time. Claude
// Code (and any target that enables mouse tracking) would otherwise receive a
// raw wheel report and scroll line-wise.
func TestWheelOverTerminalPaneSendsPageKeys(t *testing.T) {
	tests := []struct {
		name   string
		button tea.MouseButton
		want   string
	}{
		{"wheel up pages up", tea.MouseButtonWheelUp, "\x1b[5~"},
		{"wheel down pages down", tea.MouseButtonWheelDown, "\x1b[6~"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, r, emu := leftPaneHarness(t)
			m.screen = emu
			left, _ := m.paneRects()

			m.onMouse(tea.MouseMsg{
				X: left.X + 2, Y: left.Y + 2,
				Action: tea.MouseActionPress, Button: tc.button,
			})

			if got := readWithin(t, r, time.Second); got != tc.want {
				t.Errorf("wrote %q to the child PTY, want %q", got, tc.want)
			}
			if len(emu.forwarded) != 0 {
				t.Errorf("wheel was ALSO forwarded as a mouse report (%d calls); the child would scroll twice", len(emu.forwarded))
			}
		})
	}
}

// TestWheelReleaseDoesNotPageTwice guards the duplicate-paging failure: bubbletea
// can report companion release/motion events for a single notch, and acting on
// each would move two screenfuls per physical scroll.
func TestWheelReleaseDoesNotPageTwice(t *testing.T) {
	m, r, emu := leftPaneHarness(t)
	m.screen = emu
	left, _ := m.paneRects()

	for _, action := range []tea.MouseAction{tea.MouseActionRelease, tea.MouseActionMotion} {
		m.onMouse(tea.MouseMsg{
			X: left.X + 2, Y: left.Y + 2,
			Action: action, Button: tea.MouseButtonWheelUp,
		})
		if got := readWithin(t, r, 200*time.Millisecond); got != "" {
			t.Errorf("action %v emitted %q; only a press should page", action, got)
		}
	}
}

// TestNonWheelMouseStillForwards keeps the rest of mouse support intact: only
// the vertical wheel is remapped, so a target's clickable UI must still receive
// clicks and horizontal wheel events.
func TestNonWheelMouseStillForwards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		button tea.MouseButton
	}{
		{"left click", tea.MouseButtonLeft},
		{"horizontal wheel", tea.MouseButtonWheelLeft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, r, emu := leftPaneHarness(t)
			m.screen = emu
			left, _ := m.paneRects()

			m.onMouse(tea.MouseMsg{
				X: left.X + 2, Y: left.Y + 2,
				Action: tea.MouseActionPress, Button: tc.button,
			})

			if got := readWithin(t, r, 200*time.Millisecond); got != "" {
				t.Errorf("%s was turned into a page key (%q); only the vertical wheel should be", tc.name, got)
			}
		})
	}
}

// TestWheelWithNoPtyDoesNotPanic covers a Target that exists but was never
// started — the shape several existing tests construct.
func TestWheelWithNoPtyDoesNotPanic(t *testing.T) {
	m := Model{
		width: 100, height: 40, splitRatio: 0.5,
		fi: newFilter(), vp: viewport.New(0, 0),
		target: &runner.Target{}, // Pty is nil
		screen: &recordingEmulator{},
	}
	left, _ := m.paneRects()
	m.onMouse(tea.MouseMsg{
		X: left.X + 2, Y: left.Y + 2,
		Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp,
	})
}
