package tui

import tea "github.com/charmbracelet/bubbletea"

// keyBytes converts a bubbletea key event into the raw bytes a terminal would
// send, so the left pane can forward keystrokes to the child PTY transparently.
// It covers the common set; exotic keys can be added as needed.
func keyBytes(k tea.KeyMsg) []byte {
	switch k.Type {
	case tea.KeyRunes:
		return []byte(string(k.Runes))
	case tea.KeySpace:
		return []byte(" ")
	case tea.KeyEnter:
		return []byte("\r")
	case tea.KeyTab:
		return []byte("\t")
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyCtrlC:
		return []byte{0x03}
	case tea.KeyCtrlD:
		return []byte{0x04}
	case tea.KeyCtrlU:
		return []byte{0x15}
	case tea.KeyCtrlL:
		return []byte{0x0c}
	default:
		// Fall back to the printable string form for anything unmapped.
		if s := k.String(); len(s) == 1 {
			return []byte(s)
		}
		return nil
	}
}
