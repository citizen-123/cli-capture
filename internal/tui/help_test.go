package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHelpViewCoversShortcuts(t *testing.T) {
	out := (Model{}).helpView()
	for _, want := range []string{
		"keyboard shortcuts",
		"switch pane",
		"intercept",
		"flag",
		"filter",
		"detail view",
		"inject",
		"Content-Length",
		"close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help view missing %q", want)
		}
	}
}

// The overlay body is far taller than a terminal, and the ':' command section
// sits near the bottom. Before scrolling it was simply unreachable, which made
// the "table documents itself" design a fiction.
func TestHelpScrollsToTheCommandSection(t *testing.T) {
	m := Model{height: 30, width: 100}

	if strings.Contains(m.helpViewAt(30, 0), ":export") {
		t.Skip("commands already visible unscrolled; nothing to prove")
	}

	max := m.maxHelpScroll(30)
	if max <= 0 {
		t.Fatalf("maxHelpScroll = %d, want a scrollable body", max)
	}
	bottom := m.helpViewAt(30, max)
	for _, want := range []string{":export", ":filter", ":q"} {
		if !strings.Contains(bottom, want) {
			t.Errorf("scrolled to the end, but %q is still not visible", want)
		}
	}
}

func TestHelpScrollClampsAtBothEnds(t *testing.T) {
	m := Model{height: 30, width: 100}
	max := m.maxHelpScroll(30)

	if got := m.helpViewAt(30, -5); got != m.helpViewAt(30, 0) {
		t.Error("a negative scroll should render the same as the top")
	}
	if got := m.helpViewAt(30, max+50); got != m.helpViewAt(30, max) {
		t.Error("scrolling past the end should render the same as the end")
	}
	// The first line of the body must be visible at offset 0.
	if !strings.Contains(m.helpViewAt(30, 0), "keyboard shortcuts") {
		t.Error("the title is not visible at the top of the overlay")
	}
}

func TestHelpKeysScroll(t *testing.T) {
	m := Model{height: 30, width: 100, showHelp: true}

	next, _ := m.onHelpKey(key("j"))
	if next.(Model).helpScroll != 1 {
		t.Errorf("j left helpScroll = %d, want 1", next.(Model).helpScroll)
	}
	next, _ = m.onHelpKey(key("G"))
	if got, want := next.(Model).helpScroll, m.maxHelpScroll(30); got != want {
		t.Errorf("G left helpScroll = %d, want %d", got, want)
	}
	// k at the top must not go negative.
	next, _ = m.onHelpKey(key("k"))
	if next.(Model).helpScroll != 0 {
		t.Errorf("k at the top left helpScroll = %d, want 0", next.(Model).helpScroll)
	}
	// esc still closes rather than scrolling.
	next, _ = m.onHelpKey(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).showHelp {
		t.Error("esc did not close the overlay")
	}
}
