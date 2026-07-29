package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// modelWithFlows builds a traffic-pane model with n flows. The text inputs must
// come from their constructors: a zero textinput.Model has no blink context, so
// Focus() panics on it.
func modelWithFlows(n int) Model {
	m := Model{focus: focusTraffic, fi: newFilter(), ci: newCommandLine()}
	for i := 0; i < n; i++ {
		m.flows = append(m.flows, capture.NewFlow("c", "api.example.com:443"))
	}
	return m
}

// key builds a KeyMsg the way bubbletea delivers a printable keystroke, so
// k.String() matches what dispatch reads.
func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, s := range keys {
		next, _ := m.onKey(key(s))
		m = next.(Model)
	}
	return m
}

func TestCommandFilterAndSort(t *testing.T) {
	m := modelWithFlows(3)

	m2, _ := m.runCommand("filter path:/api")
	m = m2.(Model)
	if m.fi.Value() != "path:/api" {
		t.Errorf("filter value = %q, want path:/api", m.fi.Value())
	}

	m2, _ = m.runCommand("sort size")
	m = m2.(Model)
	if m.sort != sortSize {
		t.Errorf("sort = %v, want sortSize", m.sort)
	}

	m2, _ = m.runCommand("sort bogus")
	m = m2.(Model)
	if !strings.Contains(m.status, "none") {
		t.Errorf("bad sort arg status = %q, want it to list valid values", m.status)
	}
}

func TestCommandQuit(t *testing.T) {
	m := modelWithFlows(1)
	_, cmd := m.runCommand("q")
	if cmd == nil {
		t.Fatal(":q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf(":q did not produce a quit")
	}
}

func TestCommandUnknown(t *testing.T) {
	m := modelWithFlows(1)
	m2, _ := m.runCommand("frobnicate")
	if !strings.Contains(m2.(Model).status, "unknown command") {
		t.Errorf("unknown command status = %q", m2.(Model).status)
	}
}

func TestCountedMotion(t *testing.T) {
	m := modelWithFlows(10)

	// 5j moves down five rows in one go.
	m = press(t, m, "5", "j")
	if m.selected != 5 {
		t.Errorf("after 5j, selected = %d, want 5", m.selected)
	}
	if m.count != 0 {
		t.Errorf("count should reset after the motion, got %d", m.count)
	}

	// 2k moves back up two.
	m = press(t, m, "2", "k")
	if m.selected != 3 {
		t.Errorf("after 2k, selected = %d, want 3", m.selected)
	}

	// A count past the end clamps to the last row.
	m = press(t, m, "9", "9", "j")
	if m.selected != 9 {
		t.Errorf("overshooting count, selected = %d, want 9 (last)", m.selected)
	}
}

func TestGotoMotions(t *testing.T) {
	m := modelWithFlows(8)
	m.selected = 4

	m = press(t, m, "G") // bottom
	if m.selected != 7 {
		t.Errorf("G: selected = %d, want 7", m.selected)
	}
	m = press(t, m, "g", "g") // top
	if m.selected != 0 {
		t.Errorf("gg: selected = %d, want 0", m.selected)
	}
	m = press(t, m, "3", "G") // to line 3 (1-indexed)
	if m.selected != 2 {
		t.Errorf("3G: selected = %d, want 2", m.selected)
	}
}

// modelWithHosts builds a flow list from a sequence of host names, so a run of
// equal names is one "paragraph" for the } and { motions.
func modelWithHosts(hosts ...string) Model {
	m := Model{focus: focusTraffic, fi: newFilter(), ci: newCommandLine()}
	for _, h := range hosts {
		m.flows = append(m.flows, capture.NewFlow("c", h))
	}
	return m
}

func TestHostMotions(t *testing.T) {
	// indexes: 0 1 2 = a, 3 4 = b, 5 = c
	hosts := []string{"a", "a", "a", "b", "b", "c"}

	tests := []struct {
		name          string
		from, dir, ct int
		want          int
	}{
		{"} from mid-group goes to the next host", 1, +1, 1, 3},
		{"} from the last of a group still advances", 2, +1, 1, 3},
		{"2} skips two hosts", 0, +1, 2, 5},
		{"} at the last host stays put", 5, +1, 1, 5},
		{"} past the end clamps rather than wrapping", 0, +1, 9, 5},
		{"{ from mid-group goes to that group's first flow", 4, -1, 1, 3},
		{"{ when already at the first flow steps back a host", 3, -1, 1, 0},
		{"{ at the top stays put", 0, -1, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := modelWithHosts(hosts...)
			if got := hostJump(m.visible(), tc.from, tc.dir, tc.ct); got != tc.want {
				t.Errorf("hostJump(from=%d, dir=%d, count=%d) = %d, want %d",
					tc.from, tc.dir, tc.ct, got, tc.want)
			}
		})
	}
}

func TestHostMotionEmptyList(t *testing.T) {
	m := modelWithHosts()
	if got := hostJump(m.visible(), 0, +1, 1); got != 0 {
		t.Errorf("hostJump on an empty list = %d, want 0", got)
	}
}

// The command table is both the dispatcher and the help overlay's source, so
// these check the table itself rather than executing handlers — running them all
// would fire a real request for :resend and write files for :har.
func TestCommandTableIsWellFormed(t *testing.T) {
	seen := map[string]string{} // name or alias -> the command that claimed it
	for _, c := range commands {
		if c.run == nil {
			t.Errorf(":%s has no handler", c.name)
		}
		if c.desc == "" {
			t.Errorf(":%s has no description, so it renders blank in the help", c.name)
		}
		for _, n := range append([]string{c.name}, c.aliases...) {
			if prev, dup := seen[n]; dup {
				t.Errorf("%q is claimed by both :%s and :%s", n, prev, c.name)
			}
			seen[n] = c.name
		}
	}
}

func TestCommandAliasesResolve(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{
		{"filter", "filter"}, {"f", "filter"},
		{"w", "w"}, {"write", "w"}, {"save", "w"},
		{"curl", "curl"}, {"y", "curl"},
		{"resend", "resend"}, {"x", "resend"},
		{"q", "q"}, {"quit", "q"},
		{"help", "help"}, {"h", "help"},
	} {
		c, ok := lookupCommand(tc.typed)
		if !ok {
			t.Errorf(":%s did not resolve", tc.typed)
			continue
		}
		if c.name != tc.want {
			t.Errorf(":%s resolved to :%s, want :%s", tc.typed, c.name, tc.want)
		}
	}
	if _, ok := lookupCommand("frobnicate"); ok {
		t.Error("an unknown name resolved to a command")
	}
}

func TestCountCancelledByEsc(t *testing.T) {
	m := modelWithFlows(10)
	m = press(t, m, "4")
	if m.count != 4 {
		t.Fatalf("count = %d, want 4", m.count)
	}
	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.count != 0 {
		t.Errorf("esc left count = %d, want it discarded", m.count)
	}
	// The discarded count must not silently apply to the next motion.
	m = press(t, m, "j")
	if m.selected != 1 {
		t.Errorf("after esc then j, selected = %d, want 1", m.selected)
	}
}

func TestOpenCommandLine(t *testing.T) {
	m := modelWithFlows(1)
	m = press(t, m, ":")
	if !m.cmdline {
		t.Error(": did not open the command line")
	}
}
