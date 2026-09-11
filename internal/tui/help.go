package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// vimHelpHeading opens the part of the overlay for motion prefixes and two-key
// sequences the keymap cannot express directly. Their help still derives from
// the effective traffic bindings.
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
			head = "Global — press " + displayKey(km.LeaderName) + " (leader), then:"
		}
		b.WriteString(sectionStyle.Render(head) + "\n")
		for _, r := range rows {
			writeHelpRow(&b, r[0], r[1])
		}
	}

	// Motions are prefixes and two-key sequences, so they cannot be keymap
	// entries the way every row above is. Their available prefixes still follow
	// the live keymap, though.
	b.WriteString("\n" + sectionStyle.Render(vimHelpHeading) + "\n")
	for _, row := range motionHelpRows(km) {
		writeHelpRow(&b, row[0], row[1])
	}

	if len(km.keysFor(ctxTraffic, ActCommand)) > 0 {
		b.WriteString("\n" + sectionStyle.Render("Traffic pane — : commands") + "\n")
		names := make([]string, len(commands))
		cmdWidth := 0
		for i, c := range commands {
			names[i] = commandHelpName(c)
			if n := len([]rune(names[i])); n > cmdWidth {
				cmdWidth = n
			}
		}
		for i, c := range commands {
			writeHelpRowW(&b, names[i], c.desc, cmdWidth+2)
		}
	}

	b.WriteString("\n" + sectionStyle.Render("Terminal pane (left)") + "\n")
	writeHelpRow(&b, "any key", "forwarded to the target app; only "+displayKey(km.LeaderName)+" is caught")
	writeHelpRow(&b, displayKey(km.LeaderName), "pressed twice in terminal focus, sends its literal byte")

	b.WriteString("\n" + dimStyle.Render("Workflow: "+m.leaderHint(ActInterceptRequests)+" arms requests (-scope limits matching).") + "\n")
	b.WriteString(dimStyle.Render(m.leaderHint(ActPauseInspect)+" inspects a hold without discarding the current draft.") + "\n")
	return b.String()
}

// motionHelpRows lists the built-in motion capabilities whose prefixes remain
// available after traffic-pane bindings claim their keys.
func motionHelpRows(km KeyMap) [][2]string {
	digitsFree := true
	for key := '0'; key <= '9'; key++ {
		if km.claims(ctxTraffic, string(key)) {
			digitsFree = false
			break
		}
	}

	var rows [][2]string
	repeatable := []struct {
		action Action
		count  string
	}{
		{ActFlowPrev, "5"},
		{ActFlowNext, "5"},
		{ActHostPrev, "2"},
		{ActHostNext, "2"},
	}
	var examples []string
	for _, motion := range repeatable {
		keys := km.keysFor(ctxTraffic, motion.action)
		if len(keys) > 0 {
			examples = append(examples, motion.count+prettyKeys(keys)[0])
		}
	}
	if digitsFree && len(examples) > 0 {
		rows = append(rows, [2]string{
			"{count}",
			"repeat a bound motion — " + strings.Join(examples, ", "),
		})
	}
	if !km.claims(ctxTraffic, "g") {
		rows = append(rows, [2]string{"gg", "first flow"})
		if digitsFree {
			rows = append(rows, [2]string{"{count}gg", "jump to that row — 5gg goes to row 5"})
		}
	}
	if !km.claims(ctxTraffic, "G") {
		rows = append(rows, [2]string{"G", "last flow"})
		if digitsFree {
			rows = append(rows, [2]string{"{count}G", "jump to that row — 12G goes to row 12"})
		}
	}
	rows = append(rows, [2]string{"esc", "discard a half-typed motion"})
	return rows
}

// commandHelpName renders a command's canonical spelling, aliases, and
// arguments together so every way to invoke it is discoverable in the overlay.
func commandHelpName(c command) string {
	name := ":" + c.name
	if len(c.aliases) > 0 {
		aliases := make([]string, len(c.aliases))
		for i, alias := range c.aliases {
			aliases[i] = ":" + alias
		}
		name += " (" + strings.Join(aliases, ", ") + ")"
	}
	if c.args != "" {
		name += " " + c.args
	}
	return name
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
	body := strings.Join(m.helpLines(), "\n")
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
		truncate(helpFooter(scroll > 0, scroll+avail < len(lines)), max(1, m.width-8)))
}

// clampHelpScroll bounds helpScroll to the overlay's scrollable range. Both
// input paths into the overlay end here — onHelpKey for j/k/G/gg, onHelpMouse
// for the wheel — so the two cannot drift into disagreeing about where the
// bottom is.
func (m Model) clampHelpScroll() Model {
	if max := m.maxHelpScroll(m.height - m.chromeHeight()); m.helpScroll > max {
		m.helpScroll = max
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
	return m
}

// maxHelpScroll is the largest useful scroll offset for a terminal of h rows.
func (m Model) maxHelpScroll(h int) int {
	avail := h - helpChrome - 1
	if avail < 1 {
		avail = 1
	}
	n := len(m.helpLines()) - avail
	if n < 0 {
		n = 0
	}
	return n
}

func (m Model) helpLines() []string {
	body := m.helpBody()
	if m.width > 8 {
		body = ansi.Hardwrap(body, m.width-8, false)
	}
	return strings.Split(body, "\n")
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
