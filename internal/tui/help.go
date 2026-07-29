package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// vimHelpHeading opens the part of the overlay that is not keymap-derived:
// motions (prefixes and two-key sequences the keymap cannot express) and the
// ':' commands (deliberately available whether or not the equivalent action
// still has a key). Tests about keymap-driven rows cut the output here.
const vimHelpHeading = "Traffic pane — vim motions"

// helpBody renders the help text itself, unbordered and unwindowed. Every row
// in the key sections comes from the live keymap rather than a second
// hand-maintained list, so rebinding a key updates the help, and an unbound
// action disappears from it.
func (m Model) helpBody() string {
	km := m.km()

	var b strings.Builder
	b.WriteString(titleStyle.Render("cli-capture — keyboard shortcuts") + "\n\n")

	wrote := false
	for _, ctx := range contexts {
		rows := make([][2]string, 0, len(defaults[ctx]))
		for _, bind := range defaults[ctx] {
			keys := km.keysFor(ctx, bind.action)
			if len(keys) == 0 {
				continue // unbound by the user
			}
			rows = append(rows, [2]string{strings.Join(prettyKeys(keys), " / "), bind.desc})
		}
		if len(rows) == 0 {
			continue
		}

		// Space before every section except the first one actually rendered, so
		// a fully-unbound leading context doesn't open the overlay with a blank.
		if wrote {
			b.WriteString("\n")
		}
		wrote = true
		head := contextTitle[ctx]
		if ctx == ctxLeader {
			head = "Global — press " + km.LeaderName + " (leader), then:"
		}
		b.WriteString(sectionStyle.Render(head) + "\n")
		for _, r := range rows {
			writeHelpRow(&b, r[0], r[1])
		}
	}

	// Motions are prefixes and two-key sequences, so they cannot be keymap
	// entries the way every row above is — hence the hand-written section.
	b.WriteString("\n" + sectionStyle.Render(vimHelpHeading) + "\n")
	writeHelpRow(&b, "{count}", "repeats the next motion: 5j, 3k, 2}")
	writeHelpRow(&b, "gg / G", "first / last flow")
	writeHelpRow(&b, "{count}G", "jump to that row — 12G, and 5gg for row 5")
	writeHelpRow(&b, "esc", "discard a half-typed count")

	b.WriteString("\n" + sectionStyle.Render("Traffic pane — : commands") + "\n")
	names := make([]string, len(commands))
	cmdWidth := 0
	for i, c := range commands {
		names[i] = ":" + c.name
		if c.args != "" {
			names[i] += " " + c.args
		}
		if n := len([]rune(names[i])); n > cmdWidth {
			cmdWidth = n
		}
	}
	for i, c := range commands {
		writeHelpRowW(&b, names[i], c.desc, cmdWidth+2)
	}

	b.WriteString("\n" + sectionStyle.Render("Terminal pane (left)") + "\n")
	writeHelpRow(&b, "any key", "forwarded to the target app; only "+km.LeaderName+" is caught")
	writeHelpRow(&b, km.LeaderName, "pressed twice, sends a literal "+km.LeaderName+" to the target")

	b.WriteString("\n" + dimStyle.Render("Workflow: arm interception with "+km.LeaderName+" i (scope with -scope); matching") + "\n")
	b.WriteString(dimStyle.Render("requests PAUSE so you can e-dit, f-orward, or d-rop them.") + "\n")
	return b.String()
}

// helpChrome is the number of lines frameHelp adds around the content: one
// border and one padding row at the top and at the bottom.
const helpChrome = 4

// frameHelp puts the border and padding around a block of help content.
func frameHelp(content string) string {
	return lipgloss.NewStyle().
		Border(paneBorder).
		BorderForeground(focused).
		Padding(1, 3).
		Render(content)
}

// helpView renders the whole overlay unscrolled. Tests and any caller that does
// not know the terminal height use this.
func (m Model) helpView() string {
	return frameHelp(m.helpBody() + "\n" + helpFooter(false, false))
}

// helpViewAt renders the overlay windowed to a terminal of h rows, starting at
// line `scroll`. The body is well over 80 lines, so on any real terminal some of
// it is off-screen — clipping alone would mean the ':' commands, which sit near
// the bottom, could never be read. Scrolling is what makes the table that
// documents itself actually reachable.
func (m Model) helpViewAt(h, scroll int) string {
	body := m.helpBody()
	if h <= 0 {
		return frameHelp(body + "\n" + helpFooter(false, false))
	}
	lines := strings.Split(body, "\n")
	avail := h - helpChrome - 1 // -1 for the footer
	if avail < 1 {
		avail = 1
	}
	if len(lines) <= avail {
		return frameHelp(body + "\n" + helpFooter(false, false))
	}
	scroll = clampIndex(scroll, len(lines)-avail+1)
	window := lines[scroll : scroll+avail]
	return frameHelp(strings.Join(window, "\n") + "\n" +
		helpFooter(scroll > 0, scroll+avail < len(lines)))
}

// maxHelpScroll is the largest useful scroll offset for a terminal of h rows.
func (m Model) maxHelpScroll(h int) int {
	avail := h - helpChrome - 1
	if avail < 1 {
		avail = 1
	}
	n := len(strings.Split(m.helpBody(), "\n")) - avail
	if n < 0 {
		n = 0
	}
	return n
}

func helpFooter(more, below bool) string {
	hint := "press ? or esc to close"
	switch {
	case more && below:
		hint = "j / k to scroll · ↑ more above · ↓ more below · ? or esc to close"
	case below:
		hint = "j / k to scroll · more below ↓ · ? or esc to close"
	case more:
		hint = "j / k to scroll · more above ↑ · ? or esc to close"
	}
	return lipgloss.NewStyle().Faint(true).Render(hint)
}

func writeHelpRow(b *strings.Builder, keys, desc string) {
	writeHelpRowW(b, keys, desc, 11)
}

// writeHelpRowW writes one row with an explicit key-column width, so a section
// of long names (the ':' commands) stays aligned within itself instead of
// forcing every other section to the same wide column.
func writeHelpRowW(b *strings.Builder, keys, desc string, width int) {
	pad := width - len([]rune(keys))
	if pad < 1 {
		pad = 1
	}
	b.WriteString("  " + keyCapStyle.Render(keys) + strings.Repeat(" ", pad) + desc + "\n")
}

// prettyKeys makes bindings readable: the space key is invisible otherwise.
func prettyKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if k == " " {
			k = "space"
		}
		out[i] = k
	}
	return out
}
