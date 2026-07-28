package tui

import "github.com/charmbracelet/lipgloss"

var (
	focused   = lipgloss.Color("13")
	unfocused = lipgloss.Color("240")

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	pendingStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	flagStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange for flagged rows
	keyCapStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// HTTP status-class colors for the flow-list status column.
	code2xxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	code3xxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	code4xxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	code5xxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

	// JSON syntax highlighting for pretty-printed bodies.
	jsonKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	jsonStrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	jsonNumStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	jsonLitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // magenta (true/false/null)
	jsonPunctStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func paneStyle(active bool) lipgloss.Style {
	c := unfocused
	if active {
		c = focused
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c)
}

func statusBar(width int, msg string, reqOn, respOn bool) string {
	mode := "monitor"
	if reqOn || respOn {
		mode = "req:" + onOff(reqOn) + " resp:" + onOff(respOn)
	}
	left := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Render(" " + mode + " ")
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(" " + msg)
	return lipgloss.NewStyle().Width(width).Render(left + bar)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
