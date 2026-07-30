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

func TestGotoMotionsCanBeDisabled(t *testing.T) {
	km, err := NewKeyMap("", map[string]map[string]string{
		ctxTraffic: {
			"g": string(Unbind),
			"G": string(Unbind),
		},
	})
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	m := modelWithFlows(8).WithKeys(km)
	m.selected = 4

	m = press(t, m, "G")
	if m.selected != 4 {
		t.Errorf("disabled G moved selection to %d, want 4", m.selected)
	}
	m = press(t, m, "g", "g")
	if m.selected != 4 {
		t.Errorf("disabled gg moved selection to %d, want 4", m.selected)
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

// A count belongs to the traffic list. If it survived a leader sequence or a
// spell in the terminal pane it would silently multiply some much later motion.
func TestCountDiscardedWhenFocusLeavesTheList(t *testing.T) {
	t.Run("leader sequence", func(t *testing.T) {
		m := modelWithFlows(10)
		m = press(t, m, "5")
		next, _ := m.onKey(tea.KeyMsg{Type: m.km().Leader})
		m = next.(Model)
		if m.count != 0 {
			t.Fatalf("count survived the leader key: %d", m.count)
		}
		m.pendingLeader = false
		if m = press(t, m, "j"); m.selected != 1 {
			t.Errorf("selected = %d after a discarded count, want 1", m.selected)
		}
	})

	t.Run("terminal pane", func(t *testing.T) {
		m := modelWithFlows(10)
		m = press(t, m, "5")
		m.focus = focusTerminal
		next, _ := m.onKey(key("a")) // typed at the target, not the list
		m = next.(Model)
		if m.count != 0 {
			t.Fatalf("count survived time in the terminal pane: %d", m.count)
		}
		m.focus = focusTraffic
		if m = press(t, m, "j"); m.selected != 1 {
			t.Errorf("selected = %d after a discarded count, want 1", m.selected)
		}
	})

	t.Run("lone g", func(t *testing.T) {
		m := modelWithFlows(10)
		m.selected = 4
		m = press(t, m, "g")
		if !m.pendingG {
			t.Fatal("g did not arm")
		}
		next, _ := m.onKey(tea.KeyMsg{Type: m.km().Leader})
		m = next.(Model)
		m.pendingLeader = false
		if m.pendingG {
			t.Fatal("a lone g survived the leader key")
		}
		// The next g must arm afresh rather than completing a stale gg.
		if m = press(t, m, "g"); m.selected != 4 {
			t.Errorf("a stale g completed a gg: selected = %d, want 4", m.selected)
		}
	})
}

func TestCountedGotoFirstRow(t *testing.T) {
	m := modelWithFlows(10)
	m.selected = 7
	if m = press(t, m, "5", "g", "g"); m.selected != 4 { // vim: 5gg is row 5
		t.Errorf("5gg: selected = %d, want 4", m.selected)
	}
	if m = press(t, m, "g", "g"); m.selected != 0 {
		t.Errorf("bare gg: selected = %d, want 0", m.selected)
	}
}

// ':filter' and '/' must resolve an out-of-range selection identically, or the
// documented equivalence between a command and its key is a lie.
func TestFilterCommandMatchesTheFilterKey(t *testing.T) {
	m := modelWithHosts("a", "a", "b", "b", "b")
	m.selected = 4

	viaCmd, _ := m.runCommand("filter a")
	viaKey := m
	viaKey.fi.SetValue("a")
	next, _ := viaKey.onFilterKey(key("a"))
	viaKey = next.(Model)

	if got, want := viaCmd.(Model).selected, viaKey.selected; got != want {
		t.Errorf(":filter left selected=%d but / left selected=%d", got, want)
	}
}

func TestCommandLineQuitsOnCtrlQ(t *testing.T) {
	m := modelWithFlows(1)
	m.cmdline = true
	_, cmd := m.onCmdKey(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil {
		t.Fatal("ctrl+q in the command line did nothing")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+q in the command line did not quit")
	}
}

// ':flagged' is the view toggle, not the file write — the destructive one is
// spelled ':export flagged' so it is never the shorter thing to type.
func TestFlaggedCommandTogglesTheViewRatherThanWriting(t *testing.T) {
	m := modelWithFlows(3)
	next, _ := m.runCommand("flagged")
	m = next.(Model)
	if !m.flaggedOnly {
		t.Error(":flagged did not toggle the flagged-only view")
	}
	if !strings.Contains(m.status, "flagged only") {
		t.Errorf("status = %q, want it to report the view change", m.status)
	}
}

func TestFlagCommandMatchesSpaceInFlaggedOnlyView(t *testing.T) {
	base := modelWithFlows(2)
	for _, f := range base.flows {
		f.Flagged = true
	}
	base.flaggedOnly = true
	base.selected = 1

	viaKey := base
	viaKey.flows = append([]*capture.Flow(nil), base.flows...)
	keyFlow := *base.flows[1]
	viaKey.flows[1] = &keyFlow

	viaCommand := base
	viaCommand.flows = append([]*capture.Flow(nil), base.flows...)
	commandFlow := *base.flows[1]
	viaCommand.flows[1] = &commandFlow

	next, _ := viaKey.onKey(key(" "))
	viaKey = next.(Model)
	next, _ = viaCommand.runCommand("flag")
	viaCommand = next.(Model)

	if viaKey.selected != 0 || viaKey.selectedFlow() == nil {
		t.Fatalf("Space left selected=%d with selectedFlow=%v; want row 0 selected",
			viaKey.selected, viaKey.selectedFlow())
	}
	if viaCommand.selected != viaKey.selected {
		t.Errorf(":flag selected=%d, Space selected=%d", viaCommand.selected, viaKey.selected)
	}
	if viaCommand.status != viaKey.status {
		t.Errorf(":flag status=%q, Space status=%q", viaCommand.status, viaKey.status)
	}
}

func TestOpenCommandLine(t *testing.T) {
	m := modelWithFlows(1)
	m = press(t, m, ":")
	if !m.cmdline {
		t.Error(": did not open the command line")
	}
}
