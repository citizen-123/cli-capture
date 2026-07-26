package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestLeaderKeyTypeMatchesControlByte pins the assumption ParseLeader relies on:
// across the ctrl+<letter> range a bubbletea KeyType is numerically the C0 byte
// that chord transmits. If bubbletea ever renumbers, the literal-passthrough
// byte would silently start sending the wrong character to the target.
func TestLeaderKeyTypeMatchesControlByte(t *testing.T) {
	for i, want := range map[tea.KeyType]byte{
		tea.KeyCtrlA:  0x01,
		tea.KeyCtrlB:  0x02,
		tea.KeyCtrlZ:  0x1a,
		tea.KeyCtrlAt: 0x00,
	} {
		if got := byte(i); got != want {
			t.Errorf("KeyType %d as byte = %#x, want %#x", i, got, want)
		}
	}
}

func TestParseLeader(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		wantKey  tea.KeyType
		wantByte byte
		wantName string
	}{
		{"default on empty", "", tea.KeyCtrlA, 0x01, "Ctrl+A"},
		{"ctrl+a", "ctrl+a", tea.KeyCtrlA, 0x01, "Ctrl+A"},
		{"ctrl+b", "ctrl+b", tea.KeyCtrlB, 0x02, "Ctrl+B"},
		{"ctrl+z", "ctrl+z", tea.KeyCtrlZ, 0x1a, "Ctrl+Z"},
		{"ctrl+space", "ctrl+space", tea.KeyCtrlAt, 0x00, "Ctrl+Space"},
		{"ctrl+@ is the same byte", "ctrl+@", tea.KeyCtrlAt, 0x00, "Ctrl+@"},
		{"case insensitive", "CTRL+A", tea.KeyCtrlA, 0x01, "Ctrl+A"},
		{"surrounding space tolerated", "  ctrl+space  ", tea.KeyCtrlAt, 0x00, "Ctrl+Space"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLeader(tc.spec)
			if err != nil {
				t.Fatalf("ParseLeader(%q) errored: %v", tc.spec, err)
			}
			if got.Key != tc.wantKey || got.Byte != tc.wantByte || got.Name != tc.wantName {
				t.Errorf("ParseLeader(%q) = %+v, want {Key:%d Byte:%#x Name:%s}",
					tc.spec, got, tc.wantKey, tc.wantByte, tc.wantName)
			}
		})
	}
}

func TestParseLeaderRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"ctrl+1", "alt+a", "a", "ctrl+", "ctrl+ab", "escape", "ctrl++"} {
		if got, err := ParseLeader(spec); err == nil {
			t.Errorf("ParseLeader(%q) should have failed, got %+v", spec, got)
		}
	}
}

// TestHelpAdvertisesTheConfiguredLeader guards the failure that makes a rebind
// useless: the shortcut table still telling the operator to press Ctrl+A.
func TestHelpAdvertisesTheConfiguredLeader(t *testing.T) {
	for _, sec := range helpSections("Ctrl+Space") {
		if sec.head == "Global — press Ctrl+A (leader), then:" {
			t.Fatal("help still hardcodes Ctrl+A in its heading")
		}
	}
	secs := helpSections("Ctrl+Space")
	if secs[0].head != "Global — press Ctrl+Space (leader), then:" {
		t.Errorf("heading = %q, want the configured leader", secs[0].head)
	}
	if last := secs[0].rows[len(secs[0].rows)-1]; last.key != "Ctrl+Space" {
		t.Errorf("literal-passthrough row key = %q, want Ctrl+Space", last.key)
	}
}

// TestLeaderToggleMouseCaptureIsReentrant pins the escape hatch for mouse
// capture: it must round-trip (off, then back on) without wedging the model,
// each transition must return the matching bubbletea mouse command, and the
// status line must name whatever leader is actually configured — never a
// hardcoded "Ctrl+A" — so the operator knows how to get back.
func TestLeaderToggleMouseCaptureIsReentrant(t *testing.T) {
	m := Model{leader: Leader{Name: "Ctrl+Q"}, mouseCapture: true}
	toggle := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")}

	offModel, offCmd := m.leaderCommand(toggle)
	off := offModel.(Model)
	if off.mouseCapture {
		t.Fatal("first toggle should turn mouse capture off")
	}
	if offCmd == nil {
		t.Fatal("toggling off should return a command")
	}
	if got, want := reflect.TypeOf(offCmd()), reflect.TypeOf(tea.DisableMouse()); got != want {
		t.Errorf("toggle-off command produced %v, want the type DisableMouse produces (%v)", got, want)
	}
	if !strings.Contains(off.status, "OFF") || !strings.Contains(off.status, "Ctrl+Q") {
		t.Errorf("status should report OFF and name the configured leader, got %q", off.status)
	}

	onModel, onCmd := off.leaderCommand(toggle)
	on := onModel.(Model)
	if !on.mouseCapture {
		t.Fatal("second toggle should turn mouse capture back on (re-entrant)")
	}
	if onCmd == nil {
		t.Fatal("toggling on should return a command")
	}
	if got, want := reflect.TypeOf(onCmd()), reflect.TypeOf(tea.EnableMouseCellMotion()); got != want {
		t.Errorf("toggle-on command produced %v, want the type EnableMouseCellMotion produces (%v)", got, want)
	}
	if !strings.Contains(on.status, "ON") || !strings.Contains(on.status, "Ctrl+Q") {
		t.Errorf("status should report ON and name the configured leader, got %q", on.status)
	}
}
