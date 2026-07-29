package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// clipToHeight bounds a rendered block to h lines. lipgloss.Place centres but
// does not clip, so on a short terminal an over-tall help overlay scrolls its
// own title off the top and leaves no hint that anything is missing.
func clipToHeight(s string, h int) string {
	if h <= 0 {
		return s // unknown height (tests, first frame): leave it alone
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= h {
		return s
	}
	if h < 2 {
		return strings.Join(lines[:h], "\n")
	}
	hidden := len(lines) - (h - 1)
	return strings.Join(lines[:h-1], "\n") + "\n" +
		dimStyle.Render(fmt.Sprintf("  … %d more lines — resize taller to see them", hidden))
}

// vimHelpHeading opens the part of the overlay that is not keymap-derived:
// motions (prefixes and two-key sequences the keymap cannot express) and the
// ':' commands (deliberately available whether or not the equivalent action
// still has a key). Tests about keymap-driven rows cut the output here.
const vimHelpHeading = "Traffic pane — vim motions"

// helpView renders the floating help box. Every row comes from the live keymap
// rather than a second hand-maintained list, so rebinding a key updates the
// help, and an unbound action disappears from it.
func (m Model) helpView() string {
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
	writeHelpRow(&b, "gg / G", "first / last flow; 12G jumps to row 12")
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
	b.WriteString("\n" + lipgloss.NewStyle().Faint(true).Render("press ? or esc to close"))

	return lipgloss.NewStyle().
		Border(paneBorder).
		BorderForeground(focused).
		Padding(1, 3).
		Render(b.String())
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
