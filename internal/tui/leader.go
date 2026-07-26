package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Leader is the tmux-style prefix key: press it, then a command key. Only this
// one chord is taken from the target app, so everything else in the terminal
// pane passes through untouched — which is why it is worth letting the operator
// move it off a key their target app needs.
type Leader struct {
	Key  tea.KeyType // what Update matches against
	Byte byte        // sent to the target when the leader is pressed twice
	Name string      // display form for help and the status bar, e.g. "Ctrl+A"
}

// DefaultLeader preserves the original hardcoded binding.
var DefaultLeader = Leader{Key: tea.KeyCtrlA, Byte: 0x01, Name: "Ctrl+A"}

// leaderAliases are the spellings that do not follow the ctrl+<letter> pattern.
// Ctrl+Space and Ctrl+@ are the same C0 byte (NUL) — terminals emit 0x00 for
// both — so they resolve to one binding under the name the operator asked for.
var leaderAliases = map[string]Leader{
	"ctrl+space": {Key: tea.KeyCtrlAt, Byte: 0x00, Name: "Ctrl+Space"},
	"ctrl+@":     {Key: tea.KeyCtrlAt, Byte: 0x00, Name: "Ctrl+@"},
}

// ParseLeader turns a spec like "ctrl+a" or "ctrl+space" into a Leader.
//
// For the ctrl+<letter> range the bubbletea KeyType value is numerically the
// C0 control byte it represents (tea.KeyCtrlA == 1 == the byte ctrl+a sends),
// so one conversion covers both the match and the literal passthrough. The
// TestLeaderKeyTypeMatchesControlByte test pins that relationship.
func ParseLeader(spec string) (Leader, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return DefaultLeader, nil
	}
	if l, ok := leaderAliases[s]; ok {
		return l, nil
	}
	if letter, ok := strings.CutPrefix(s, "ctrl+"); ok && len(letter) == 1 {
		if c := letter[0]; c >= 'a' && c <= 'z' {
			key := tea.KeyCtrlA + tea.KeyType(c-'a')
			return Leader{
				Key:  key,
				Byte: byte(key),
				Name: "Ctrl+" + strings.ToUpper(letter),
			}, nil
		}
	}
	return Leader{}, fmt.Errorf("unrecognized leader %q: want ctrl+a … ctrl+z, or one of %s",
		spec, strings.Join(aliasNames(), ", "))
}

func aliasNames() []string {
	names := make([]string, 0, len(leaderAliases))
	for k := range leaderAliases {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
