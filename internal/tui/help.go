package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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
	pad := 11 - len(keys)
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
