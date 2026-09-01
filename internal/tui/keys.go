package tui

import tea "github.com/charmbracelet/bubbletea"

// keyBytes converts a Bubble Tea key event back into the bytes an xterm-style
// terminal sends to its foreground process.
func keyBytes(k tea.KeyMsg) []byte {
	// Bubble Tea deliberately gives C0 controls their byte values as KeyTypes.
	// Keep this ahead of the named-key switch so every control, including NUL,
	// is forwarded without an incomplete list of aliases.
	if k.Type >= tea.KeyNull && k.Type <= tea.KeyCtrlUnderscore {
		return altPrefix(k.Alt, []byte{byte(k.Type)})
	}

	switch k.Type {
	case tea.KeyRunes:
		if len(k.Runes) == 0 {
			return nil
		}
		return altPrefix(k.Alt, []byte(string(k.Runes)))
	case tea.KeyBackspace:
		return altPrefix(k.Alt, []byte{0x7f})
	case tea.KeySpace:
		return altPrefix(k.Alt, []byte{' '})
	case tea.KeyUp:
		return xtermSequence(k.Alt, "\x1b[A", "\x1b[1;3A")
	case tea.KeyDown:
		return xtermSequence(k.Alt, "\x1b[B", "\x1b[1;3B")
	case tea.KeyRight:
		return xtermSequence(k.Alt, "\x1b[C", "\x1b[1;3C")
	case tea.KeyLeft:
		return xtermSequence(k.Alt, "\x1b[D", "\x1b[1;3D")
	case tea.KeyShiftTab:
		// Xterm has no parameterized Alt+Shift+Tab sequence; Bubble Tea
		// represents the terminal's ESC-prefixed CSI Z form.
		return xtermSequence(k.Alt, "\x1b[Z", "\x1b\x1b[Z")
	case tea.KeyHome:
		return xtermSequence(k.Alt, "\x1b[H", "\x1b[1;3H")
	case tea.KeyEnd:
		return xtermSequence(k.Alt, "\x1b[F", "\x1b[1;3F")
	case tea.KeyPgUp:
		return xtermSequence(k.Alt, "\x1b[5~", "\x1b[5;3~")
	case tea.KeyPgDown:
		return xtermSequence(k.Alt, "\x1b[6~", "\x1b[6;3~")
	case tea.KeyCtrlPgUp:
		return xtermSequence(k.Alt, "\x1b[5;5~", "\x1b[5;7~")
	case tea.KeyCtrlPgDown:
		return xtermSequence(k.Alt, "\x1b[6;5~", "\x1b[6;7~")
	case tea.KeyDelete:
		return xtermSequence(k.Alt, "\x1b[3~", "\x1b[3;3~")
	case tea.KeyInsert:
		return xtermSequence(k.Alt, "\x1b[2~", "\x1b[2;3~")
	case tea.KeyCtrlUp:
		return xtermSequence(k.Alt, "\x1b[1;5A", "\x1b[1;7A")
	case tea.KeyCtrlDown:
		return xtermSequence(k.Alt, "\x1b[1;5B", "\x1b[1;7B")
	case tea.KeyCtrlRight:
		return xtermSequence(k.Alt, "\x1b[1;5C", "\x1b[1;7C")
	case tea.KeyCtrlLeft:
		return xtermSequence(k.Alt, "\x1b[1;5D", "\x1b[1;7D")
	case tea.KeyCtrlHome:
		return xtermSequence(k.Alt, "\x1b[1;5H", "\x1b[1;7H")
	case tea.KeyCtrlEnd:
		return xtermSequence(k.Alt, "\x1b[1;5F", "\x1b[1;7F")
	case tea.KeyShiftUp:
		return xtermSequence(k.Alt, "\x1b[1;2A", "\x1b[1;4A")
	case tea.KeyShiftDown:
		return xtermSequence(k.Alt, "\x1b[1;2B", "\x1b[1;4B")
	case tea.KeyShiftRight:
		return xtermSequence(k.Alt, "\x1b[1;2C", "\x1b[1;4C")
	case tea.KeyShiftLeft:
		return xtermSequence(k.Alt, "\x1b[1;2D", "\x1b[1;4D")
	case tea.KeyShiftHome:
		return xtermSequence(k.Alt, "\x1b[1;2H", "\x1b[1;4H")
	case tea.KeyShiftEnd:
		return xtermSequence(k.Alt, "\x1b[1;2F", "\x1b[1;4F")
	case tea.KeyCtrlShiftUp:
		return xtermSequence(k.Alt, "\x1b[1;6A", "\x1b[1;8A")
	case tea.KeyCtrlShiftDown:
		return xtermSequence(k.Alt, "\x1b[1;6B", "\x1b[1;8B")
	case tea.KeyCtrlShiftRight:
		return xtermSequence(k.Alt, "\x1b[1;6C", "\x1b[1;8C")
	case tea.KeyCtrlShiftLeft:
		return xtermSequence(k.Alt, "\x1b[1;6D", "\x1b[1;8D")
	case tea.KeyCtrlShiftHome:
		return xtermSequence(k.Alt, "\x1b[1;6H", "\x1b[1;8H")
	case tea.KeyCtrlShiftEnd:
		return xtermSequence(k.Alt, "\x1b[1;6F", "\x1b[1;8F")
	case tea.KeyF1:
		return xtermSequence(k.Alt, "\x1bOP", "\x1b[1;3P")
	case tea.KeyF2:
		return xtermSequence(k.Alt, "\x1bOQ", "\x1b[1;3Q")
	case tea.KeyF3:
		return xtermSequence(k.Alt, "\x1bOR", "\x1b[1;3R")
	case tea.KeyF4:
		return xtermSequence(k.Alt, "\x1bOS", "\x1b[1;3S")
	case tea.KeyF5:
		return xtermSequence(k.Alt, "\x1b[15~", "\x1b[15;3~")
	case tea.KeyF6:
		return xtermSequence(k.Alt, "\x1b[17~", "\x1b[17;3~")
	case tea.KeyF7:
		return xtermSequence(k.Alt, "\x1b[18~", "\x1b[18;3~")
	case tea.KeyF8:
		return xtermSequence(k.Alt, "\x1b[19~", "\x1b[19;3~")
	case tea.KeyF9:
		return xtermSequence(k.Alt, "\x1b[20~", "\x1b[20;3~")
	case tea.KeyF10:
		return xtermSequence(k.Alt, "\x1b[21~", "\x1b[21;3~")
	case tea.KeyF11:
		return xtermSequence(k.Alt, "\x1b[23~", "\x1b[23;3~")
	case tea.KeyF12:
		return xtermSequence(k.Alt, "\x1b[24~", "\x1b[24;3~")
	case tea.KeyF13:
		return xtermSequence(k.Alt, "\x1b[1;2P", "\x1b[1;4P")
	case tea.KeyF14:
		return xtermSequence(k.Alt, "\x1b[1;2Q", "\x1b[1;4Q")
	case tea.KeyF15:
		return xtermSequence(k.Alt, "\x1b[1;2R", "\x1b[1;4R")
	case tea.KeyF16:
		return xtermSequence(k.Alt, "\x1b[1;2S", "\x1b[1;4S")
	case tea.KeyF17:
		return xtermSequence(k.Alt, "\x1b[15;2~", "\x1b[15;4~")
	case tea.KeyF18:
		return xtermSequence(k.Alt, "\x1b[17;2~", "\x1b[17;4~")
	case tea.KeyF19:
		return xtermSequence(k.Alt, "\x1b[18;2~", "\x1b[18;4~")
	case tea.KeyF20:
		return xtermSequence(k.Alt, "\x1b[19;2~", "\x1b[19;4~")
	default:
		return nil
	}
}

func altPrefix(alt bool, b []byte) []byte {
	if !alt {
		return b
	}
	prefixed := make([]byte, len(b)+1)
	prefixed[0] = 0x1b
	copy(prefixed[1:], b)
	return prefixed
}

func xtermSequence(alt bool, plain, altModified string) []byte {
	if alt {
		return []byte(altModified)
	}
	return []byte(plain)
}
