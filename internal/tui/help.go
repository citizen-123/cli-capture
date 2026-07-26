package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type helpRow struct{ key, desc string }

type helpSection struct {
	head string
	rows []helpRow
}

// helpSections builds the shortcut table for the configured leader, so the
// help never advertises a chord the operator has rebound away from.
func helpSections(leader string) []helpSection {
	return []helpSection{
		{"Global — press " + leader + " (leader), then:", []helpRow{
			{"w", "switch pane (terminal ⇄ traffic)"},
			{"i / r", "toggle intercept: requests / responses"},
			{"s", "save session to JSON"},
			{"h", "export session as HAR"},
			{"f", "export flagged flows → flagged.txt"},
			{"?", "toggle this help"},
			{"q", "quit"},
			{leader, "send a literal " + leader + " to the target"},
		}},
		{"Terminal pane (left)", []helpRow{
			{"any key", "forwarded to the target app; only " + leader + " is caught"},
		}},
		{"Traffic pane (right)", []helpRow{
			{"j / k", "move selection (list scrolls to follow)"},
			{"space", "flag / unflag the selected flow"},
			{"F", "show flagged only"},
			{"/", "filter by host / method / path / status"},
			{"enter", "open detail view"},
			{"e / f / d", "PAUSED flow: edit / forward / drop"},
			{"x", "resend the selected flow to its origin"},
			{"c", "export the selected flow as a curl command"},
			{"n / N", "inject a WebSocket frame (client→server / server→client)"},
			{"?", "toggle this help"},
		}},
		{"Detail view (enter)", []helpRow{
			{"j / k", "scroll"},
			{"s", "save this flow to a .txt file"},
			{"esc / q", "back to the list"},
		}},
		{"Editor (while intercepting)", []helpRow{
			{"Ctrl+S", "forward / send the edited bytes"},
			{"Ctrl+L", "fix Content-Length to match the body"},
			{"esc", "cancel"},
		}},
	}
}

// helpView renders the floating help box: every shortcut, grouped by context.
func (m Model) helpView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("cli-capture — keyboard shortcuts") + "\n\n")

	for i, sec := range helpSections(m.leader.Name) {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(sectionStyle.Render(sec.head) + "\n")
		for _, r := range sec.rows {
			pad := 11 - len(r.key)
			if pad < 1 {
				pad = 1
			}
			b.WriteString("  " + keyCapStyle.Render(r.key) + strings.Repeat(" ", pad) + r.desc + "\n")
		}
	}

	b.WriteString("\n" + dimStyle.Render("Workflow: arm interception with "+m.leader.Name+" i (scope with -scope); matching") + "\n")
	b.WriteString(dimStyle.Render("requests PAUSE so you can e-dit, f-orward, or d-rop them.") + "\n")
	b.WriteString("\n" + lipgloss.NewStyle().Faint(true).Render("press ? or esc to close"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("13")).
		Padding(1, 3).
		Render(b.String())
}
