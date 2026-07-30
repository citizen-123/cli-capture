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

func TestVimHelpFollowsClaimedKeys(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]map[string]string
		present   []string
		absent    []string
	}{
		{
			name: "G disabled leaves gg available",
			overrides: map[string]map[string]string{
				ctxTraffic: {"G": string(Unbind)},
			},
			present: []string{"gg"},
			absent:  []string{"gg / G", "{count}G"},
		},
		{
			name: "g rebound leaves G available",
			overrides: map[string]map[string]string{
				ctxTraffic: {"g": string(ActFlowPrev)},
			},
			present: []string{"G"},
			absent:  []string{"gg / G"},
		},
		{
			name: "digit claimed hides counted motions",
			overrides: map[string]map[string]string{
				ctxTraffic: {"5": string(ActFlowNext)},
			},
			present: []string{"gg", "G"},
			absent:  []string{"{count}", "{count}G"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			km, err := NewKeyMap("", tc.overrides)
			if err != nil {
				t.Fatal(err)
			}
			help := Model{}.WithKeys(km).helpBody()
			_, motionSection, _ := strings.Cut(help, vimHelpHeading)
			motionSection, _, _ = strings.Cut(motionSection, "Traffic pane — : commands")
			for _, want := range tc.present {
				if !strings.Contains(motionSection, want) {
					t.Errorf("motion help missing %q", want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(motionSection, unwanted) {
					t.Errorf("motion help unexpectedly contains %q", unwanted)
				}
			}
		})
	}
}

func TestCommandHelpIncludesAliases(t *testing.T) {
	out := (Model{}).helpBody()
	for _, want := range []string{
		":filter (:f)",
		":curl (:y)",
		":resend (:x)",
		":flagged (:only)",
		":w (:write, :save)",
		":q (:quit)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("command help missing canonical/alias display %q", want)
		}
	}
}

func TestCommandHelpDescribesCurlAsAFileExport(t *testing.T) {
	out := (Model{}).helpBody()
	if strings.Contains(out, "copy the selected flow") {
		t.Error(":curl help promises a clipboard copy, but the action writes a file")
	}
	if !strings.Contains(out, "write the selected flow to a .curl file") {
		t.Error("command help must describe :curl as writing a .curl file")
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
