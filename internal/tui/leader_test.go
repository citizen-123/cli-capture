package tui

import (
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
