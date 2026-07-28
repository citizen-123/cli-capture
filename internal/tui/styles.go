package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/citizen-123/cli-capture/internal/theme"
)

// Styles are package-level because every render helper in this package reaches
// for them; ApplyTheme resolves them once at startup from the loaded theme.
// They are not meant to change while the UI is running.
var (
	focused   lipgloss.TerminalColor
	unfocused lipgloss.TerminalColor

	titleStyle    lipgloss.Style
	selectedStyle lipgloss.Style
	pendingStyle  lipgloss.Style
	flagStyle     lipgloss.Style
	keyCapStyle   lipgloss.Style
	dimStyle      lipgloss.Style

	// HTTP status-class colors for the flow-list status column.
	code2xxStyle lipgloss.Style
	code3xxStyle lipgloss.Style
	code4xxStyle lipgloss.Style
	code5xxStyle lipgloss.Style
	sectionStyle lipgloss.Style

	// JSON syntax highlighting for pretty-printed bodies.
	jsonKeyStyle   lipgloss.Style
	jsonStrStyle   lipgloss.Style
	jsonNumStyle   lipgloss.Style
	jsonLitStyle   lipgloss.Style
	jsonPunctStyle lipgloss.Style

	// Status bar: the mode chip and the message beside it.
	statusModeStyle lipgloss.Style
	statusTextStyle lipgloss.Style
)

func init() {
	t, _ := theme.Builtin(theme.Default)
	ApplyTheme(t)
}

// color turns a theme color into a lipgloss color, mapping "" to no color at
// all rather than to a color literally named "".
func color(c string) lipgloss.TerminalColor {
	if c == "" {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color(c)
}

// fg builds a foreground style, leaving the color off entirely when the theme
// doesn't set one — that's what makes NO_COLOR and the "none" theme render
// clean rather than emitting default-colored escapes.
func fg(c string) lipgloss.Style {
	s := lipgloss.NewStyle()
	if c != "" {
		s = s.Foreground(lipgloss.Color(c))
	}
	return s
}

// ApplyTheme resolves every style from t. Call once, before the program starts.
func ApplyTheme(t theme.Theme) {
	focused, unfocused = color(t.Focused), color(t.Unfocused)

	titleStyle = fg(t.Title).Bold(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	pendingStyle = fg(t.Pending).Bold(true)
	flagStyle = fg(t.Flag)
	keyCapStyle = fg(t.KeyCap).Bold(true)
	dimStyle = fg(t.Dim)

	code2xxStyle = fg(t.Status2xx)
	code3xxStyle = fg(t.Status3xx)
	code4xxStyle = fg(t.Status4xx)
	code5xxStyle = fg(t.Status5xx)
	sectionStyle = fg(t.Section).Bold(true)

	jsonKeyStyle = fg(t.JSONKey)
	jsonStrStyle = fg(t.JSONString)
	jsonNumStyle = fg(t.JSONNumber)
	jsonLitStyle = fg(t.JSONLiteral)
	jsonPunctStyle = fg(t.JSONPunct)

	statusModeStyle = fg(t.StatusMode).Bold(true)
	statusTextStyle = fg(t.StatusText)
}

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
	left := statusModeStyle.Render(" " + mode + " ")
	bar := statusTextStyle.Render(" " + msg)
	return lipgloss.NewStyle().Width(width).Render(left + bar)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
