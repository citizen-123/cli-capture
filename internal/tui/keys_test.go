package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/terminal"
)

func TestKeyBytesEncodesEveryControlByte(t *testing.T) {
	for value := range 32 {
		t.Run(fmt.Sprintf("0x%02x", value), func(t *testing.T) {
			msg := tea.KeyMsg{Type: tea.KeyType(value)}
			if got, want := keyBytes(msg), []byte{byte(value)}; !bytes.Equal(got, want) {
				t.Errorf("keyBytes(%s) = %q, want %q", msg.String(), got, want)
			}

			msg.Alt = true
			if got, want := keyBytes(msg), []byte{0x1b, byte(value)}; !bytes.Equal(got, want) {
				t.Errorf("keyBytes(alt+%s) = %q, want %q", msg.String(), got, want)
			}
		})
	}
}

func TestKeyBytesEncodesRunesAndEverySpecialKey(t *testing.T) {
	tests := []struct {
		name    string
		msg     tea.KeyMsg
		want    string
		wantAlt string
	}{
		{"runes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("aλ界")}, "aλ界", ""},
		{"alt-rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'λ'}}, "λ", "\x1bλ"},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("one\n二"), Paste: true}, "one\n二", ""},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f", "\x1b\x7f"},
		{"space", tea.KeyMsg{Type: tea.KeySpace}, " ", "\x1b "},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A", "\x1b[1;3A"},
		{"down", tea.KeyMsg{Type: tea.KeyDown}, "\x1b[B", "\x1b[1;3B"},
		{"right", tea.KeyMsg{Type: tea.KeyRight}, "\x1b[C", "\x1b[1;3C"},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, "\x1b[D", "\x1b[1;3D"},
		{"shift-tab", tea.KeyMsg{Type: tea.KeyShiftTab}, "\x1b[Z", "\x1b\x1b[Z"},
		{"home", tea.KeyMsg{Type: tea.KeyHome}, "\x1b[H", "\x1b[1;3H"},
		{"end", tea.KeyMsg{Type: tea.KeyEnd}, "\x1b[F", "\x1b[1;3F"},
		{"page-up", tea.KeyMsg{Type: tea.KeyPgUp}, "\x1b[5~", "\x1b[5;3~"},
		{"page-down", tea.KeyMsg{Type: tea.KeyPgDown}, "\x1b[6~", "\x1b[6;3~"},
		{"ctrl-page-up", tea.KeyMsg{Type: tea.KeyCtrlPgUp}, "\x1b[5;5~", "\x1b[5;7~"},
		{"ctrl-page-down", tea.KeyMsg{Type: tea.KeyCtrlPgDown}, "\x1b[6;5~", "\x1b[6;7~"},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~", "\x1b[3;3~"},
		{"insert", tea.KeyMsg{Type: tea.KeyInsert}, "\x1b[2~", "\x1b[2;3~"},
		{"ctrl-up", tea.KeyMsg{Type: tea.KeyCtrlUp}, "\x1b[1;5A", "\x1b[1;7A"},
		{"ctrl-down", tea.KeyMsg{Type: tea.KeyCtrlDown}, "\x1b[1;5B", "\x1b[1;7B"},
		{"ctrl-right", tea.KeyMsg{Type: tea.KeyCtrlRight}, "\x1b[1;5C", "\x1b[1;7C"},
		{"ctrl-left", tea.KeyMsg{Type: tea.KeyCtrlLeft}, "\x1b[1;5D", "\x1b[1;7D"},
		{"ctrl-home", tea.KeyMsg{Type: tea.KeyCtrlHome}, "\x1b[1;5H", "\x1b[1;7H"},
		{"ctrl-end", tea.KeyMsg{Type: tea.KeyCtrlEnd}, "\x1b[1;5F", "\x1b[1;7F"},
		{"shift-up", tea.KeyMsg{Type: tea.KeyShiftUp}, "\x1b[1;2A", "\x1b[1;4A"},
		{"shift-down", tea.KeyMsg{Type: tea.KeyShiftDown}, "\x1b[1;2B", "\x1b[1;4B"},
		{"shift-right", tea.KeyMsg{Type: tea.KeyShiftRight}, "\x1b[1;2C", "\x1b[1;4C"},
		{"shift-left", tea.KeyMsg{Type: tea.KeyShiftLeft}, "\x1b[1;2D", "\x1b[1;4D"},
		{"shift-home", tea.KeyMsg{Type: tea.KeyShiftHome}, "\x1b[1;2H", "\x1b[1;4H"},
		{"shift-end", tea.KeyMsg{Type: tea.KeyShiftEnd}, "\x1b[1;2F", "\x1b[1;4F"},
		{"ctrl-shift-up", tea.KeyMsg{Type: tea.KeyCtrlShiftUp}, "\x1b[1;6A", "\x1b[1;8A"},
		{"ctrl-shift-down", tea.KeyMsg{Type: tea.KeyCtrlShiftDown}, "\x1b[1;6B", "\x1b[1;8B"},
		{"ctrl-shift-right", tea.KeyMsg{Type: tea.KeyCtrlShiftRight}, "\x1b[1;6C", "\x1b[1;8C"},
		{"ctrl-shift-left", tea.KeyMsg{Type: tea.KeyCtrlShiftLeft}, "\x1b[1;6D", "\x1b[1;8D"},
		{"ctrl-shift-home", tea.KeyMsg{Type: tea.KeyCtrlShiftHome}, "\x1b[1;6H", "\x1b[1;8H"},
		{"ctrl-shift-end", tea.KeyMsg{Type: tea.KeyCtrlShiftEnd}, "\x1b[1;6F", "\x1b[1;8F"},
		{"f1", tea.KeyMsg{Type: tea.KeyF1}, "\x1bOP", "\x1b[1;3P"},
		{"f2", tea.KeyMsg{Type: tea.KeyF2}, "\x1bOQ", "\x1b[1;3Q"},
		{"f3", tea.KeyMsg{Type: tea.KeyF3}, "\x1bOR", "\x1b[1;3R"},
		{"f4", tea.KeyMsg{Type: tea.KeyF4}, "\x1bOS", "\x1b[1;3S"},
		{"f5", tea.KeyMsg{Type: tea.KeyF5}, "\x1b[15~", "\x1b[15;3~"},
		{"f6", tea.KeyMsg{Type: tea.KeyF6}, "\x1b[17~", "\x1b[17;3~"},
		{"f7", tea.KeyMsg{Type: tea.KeyF7}, "\x1b[18~", "\x1b[18;3~"},
		{"f8", tea.KeyMsg{Type: tea.KeyF8}, "\x1b[19~", "\x1b[19;3~"},
		{"f9", tea.KeyMsg{Type: tea.KeyF9}, "\x1b[20~", "\x1b[20;3~"},
		{"f10", tea.KeyMsg{Type: tea.KeyF10}, "\x1b[21~", "\x1b[21;3~"},
		{"f11", tea.KeyMsg{Type: tea.KeyF11}, "\x1b[23~", "\x1b[23;3~"},
		{"f12", tea.KeyMsg{Type: tea.KeyF12}, "\x1b[24~", "\x1b[24;3~"},
		{"f13", tea.KeyMsg{Type: tea.KeyF13}, "\x1b[1;2P", "\x1b[1;4P"},
		{"f14", tea.KeyMsg{Type: tea.KeyF14}, "\x1b[1;2Q", "\x1b[1;4Q"},
		{"f15", tea.KeyMsg{Type: tea.KeyF15}, "\x1b[1;2R", "\x1b[1;4R"},
		{"f16", tea.KeyMsg{Type: tea.KeyF16}, "\x1b[1;2S", "\x1b[1;4S"},
		{"f17", tea.KeyMsg{Type: tea.KeyF17}, "\x1b[15;2~", "\x1b[15;4~"},
		{"f18", tea.KeyMsg{Type: tea.KeyF18}, "\x1b[17;2~", "\x1b[17;4~"},
		{"f19", tea.KeyMsg{Type: tea.KeyF19}, "\x1b[18;2~", "\x1b[18;4~"},
		{"f20", tea.KeyMsg{Type: tea.KeyF20}, "\x1b[19;2~", "\x1b[19;4~"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := keyBytes(test.msg); !bytes.Equal(got, []byte(test.want)) {
				t.Errorf("keyBytes(%s) = %q, want %q", test.msg.String(), got, test.want)
			}
			if test.wantAlt == "" {
				return
			}

			test.msg.Alt = true
			if got := keyBytes(test.msg); !bytes.Equal(got, []byte(test.wantAlt)) {
				t.Errorf("keyBytes(%s) = %q, want %q", test.msg.String(), got, test.wantAlt)
			}
		})
	}
}

func TestKeyBytesRejectsOnlyUnrepresentableEvents(t *testing.T) {
	tests := []tea.KeyMsg{
		{Type: tea.KeyRunes},
		{Type: tea.KeyType(32)},
		{Type: tea.KeyType(-999)},
	}
	for _, msg := range tests {
		if got := keyBytes(msg); got != nil {
			t.Errorf("keyBytes(%v) = %q, want nil", msg, got)
		}
	}
}

func TestOnKeyForwardsTerminalKeyBytesToPTY(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	km, err := NewKeyMap("ctrl+b", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := modelWithFlows(1).WithKeys(km)
	m.focus = focusTerminal
	m.target = &runner.Target{Pty: writeEnd}

	tests := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"ctrl-w", tea.KeyMsg{Type: tea.KeyCtrlW}, "\x17"},
		{"ctrl-r", tea.KeyMsg{Type: tea.KeyCtrlR}, "\x12"},
		{"ctrl-k", tea.KeyMsg{Type: tea.KeyCtrlK}, "\x0b"},
		{"ctrl-z", tea.KeyMsg{Type: tea.KeyCtrlZ}, "\x1a"},
		{"nonleader-ctrl-a", tea.KeyMsg{Type: tea.KeyCtrlA}, "\x01"},
		{"ctrl-space", tea.KeyMsg{Type: tea.KeyCtrlAt}, "\x00"},
		{"alt-letter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true}, "\x1bx"},
		{"alt-configured-leader", tea.KeyMsg{Type: tea.KeyCtrlB, Alt: true}, "\x1b\x02"},
		{"alt-up", tea.KeyMsg{Type: tea.KeyUp, Alt: true}, "\x1b[1;3A"},
		{"alt-ctrl-left", tea.KeyMsg{Type: tea.KeyCtrlLeft, Alt: true}, "\x1b[1;7D"},
		{"alt-shift-home", tea.KeyMsg{Type: tea.KeyShiftHome, Alt: true}, "\x1b[1;4H"},
		{"alt-f1", tea.KeyMsg{Type: tea.KeyF1, Alt: true}, "\x1b[1;3P"},
		{"alt-f12", tea.KeyMsg{Type: tea.KeyF12, Alt: true}, "\x1b[24;3~"},
		{"multiple-runes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("λ界")}, "λ界"},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("paste\n二"), Paste: true}, "paste\n二"},
		{"insert", tea.KeyMsg{Type: tea.KeyInsert}, "\x1b[2~"},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~"},
		{"page-up", tea.KeyMsg{Type: tea.KeyPgUp}, "\x1b[5~"},
		{"page-down", tea.KeyMsg{Type: tea.KeyPgDown}, "\x1b[6~"},
		{"shift-tab", tea.KeyMsg{Type: tea.KeyShiftTab}, "\x1b[Z"},
		{"f1", tea.KeyMsg{Type: tea.KeyF1}, "\x1bOP"},
		{"f12", tea.KeyMsg{Type: tea.KeyF12}, "\x1b[24~"},
		{"f20", tea.KeyMsg{Type: tea.KeyF20}, "\x1b[19;2~"},
		{"ctrl-left", tea.KeyMsg{Type: tea.KeyCtrlLeft}, "\x1b[1;5D"},
		{"shift-home", tea.KeyMsg{Type: tea.KeyShiftHome}, "\x1b[1;2H"},
		{"ctrl-shift-end", tea.KeyMsg{Type: tea.KeyCtrlShiftEnd}, "\x1b[1;6F"},
	}

	var want []byte
	for _, test := range tests {
		next, _ := m.onKey(test.msg)
		m = next.(Model)
		want = append(want, test.want...)
	}

	// Alt+leader after a plain leader is data, not the double-leader escape.
	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB, Alt: true})
	m = next.(Model)
	want = append(want, "\x1b\x02"...)
	if m.pendingLeader {
		t.Fatal("alt+leader left the model waiting for a leader command")
	}

	// An exact, unmodified double leader still sends one literal leader byte.
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)
	want = append(want, '\x02')
	if m.pendingLeader {
		t.Fatal("double leader left the model waiting for a command")
	}

	// The configured leader remains UI input and must not reach the PTY.
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)
	if !m.pendingLeader {
		t.Fatal("leader was not consumed by the model")
	}

	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("terminal received %q, want %q", got, want)
	}
}

func statefulTerminalKeyHarness(t *testing.T) (Model, *os.File, *terminal.VT) {
	t.Helper()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	screen := terminal.NewVT(80, 24, writeEnd)
	t.Cleanup(func() {
		if err := screen.Close(); err != nil {
			t.Errorf("close terminal: %v", err)
		}
		readEnd.Close()
		writeEnd.Close()
	})

	m := modelWithFlows(1)
	m.focus = focusTerminal
	m.target = &runner.Target{Pty: writeEnd}
	m.screen = screen
	return m, readEnd, screen
}

func readExactPTY(t *testing.T, r *os.File, want string) {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("read terminal bytes: %v", err)
	}
	if err := r.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("terminal received %q, want %q", got, want)
	}
}

func TestOnKeyUsesChildApplicationCursorMode(t *testing.T) {
	m, readEnd, screen := statefulTerminalKeyHarness(t)

	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	readExactPTY(t, readEnd, "\x1b[A")

	if _, err := screen.Write([]byte("\x1b[?1h")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		msg  tea.KeyMsg
		want string
	}{
		{tea.KeyMsg{Type: tea.KeyUp}, "\x1bOA"},
		{tea.KeyMsg{Type: tea.KeyDown}, "\x1bOB"},
		{tea.KeyMsg{Type: tea.KeyRight}, "\x1bOC"},
		{tea.KeyMsg{Type: tea.KeyLeft}, "\x1bOD"},
	} {
		next, _ = m.onKey(test.msg)
		m = next.(Model)
		readExactPTY(t, readEnd, test.want)
	}

	if _, err := screen.Write([]byte("\x1b[?1l")); err != nil {
		t.Fatal(err)
	}
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(Model)
	readExactPTY(t, readEnd, "\x1b[D")
}

func TestOnKeyUsesChildBracketedPasteMode(t *testing.T) {
	m, readEnd, screen := statefulTerminalKeyHarness(t)

	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("plain\n"), Paste: true})
	m = next.(Model)
	readExactPTY(t, readEnd, "plain\n")

	if _, err := screen.Write([]byte("\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("guarded\n"), Paste: true})
	m = next.(Model)
	readExactPTY(t, readEnd, "\x1b[200~guarded\n\x1b[201~")

	if _, err := screen.Write([]byte("\x1b[?2004l")); err != nil {
		t.Fatal(err)
	}
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("plain again\n"), Paste: true})
	m = next.(Model)
	readExactPTY(t, readEnd, "plain again\n")
}

func TestOnKeyPreservesMixedInputOrderBeyondReplyCapacity(t *testing.T) {
	m, readEnd, screen := statefulTerminalKeyHarness(t)
	if _, err := screen.Write([]byte("\x1b[?1h\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}

	const bursts = 100 // deliberately exceeds terminal.replyFIFOSize (64)
	var want []byte
	for range bursts {
		for _, msg := range []tea.KeyMsg{
			{Type: tea.KeyUp},
			{Type: tea.KeyRunes, Runes: []rune{'x'}},
			{Type: tea.KeyRunes, Runes: []rune{'p'}, Paste: true},
			{Type: tea.KeyEnter},
		} {
			next, _ := m.onKey(msg)
			m = next.(Model)
		}
		want = append(want, "\x1bOAx\x1b[200~p\x1b[201~\r"...)
	}

	// Read only after the full burst: asynchronous/droppable input paths can
	// neither hide reordering behind per-event reads nor survive this volume.
	readExactPTY(t, readEnd, string(want))
}
