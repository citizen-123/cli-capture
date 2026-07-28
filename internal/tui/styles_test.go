package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/citizen-123/cli-capture/internal/theme"
)

// restoreDefaultTheme puts the package styles back after a test changes them —
// they are process-wide, and the other tests in this package render with them.
func restoreDefaultTheme(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		dark, _ := theme.Builtin(theme.Default)
		ApplyTheme(dark)
	})
}

func TestApplyThemeReachesTheStyles(t *testing.T) {
	restoreDefaultTheme(t)

	ApplyTheme(theme.Theme{
		Name:      "test",
		Focused:   "1",
		Status2xx: "#00ff00",
		JSONKey:   "42",
	})

	if got := code2xxStyle.GetForeground(); got != lipgloss.Color("#00ff00") {
		t.Errorf("status2xx foreground = %v, want #00ff00", got)
	}
	if got := jsonKeyStyle.GetForeground(); got != lipgloss.Color("42") {
		t.Errorf("json.key foreground = %v, want 42", got)
	}
	if got := focused; got != lipgloss.Color("1") {
		t.Errorf("focused border color = %v, want 1", got)
	}
}

func TestColorlessThemeSetsNoColor(t *testing.T) {
	restoreDefaultTheme(t)

	dark, _ := theme.Builtin(theme.Default)
	ApplyTheme(dark.Colorless())

	// An unset color must leave the style with no foreground at all, rather
	// than a color named "" — that's what keeps NO_COLOR output clean.
	for name, s := range map[string]lipgloss.Style{
		"title":     titleStyle,
		"status2xx": code2xxStyle,
		"json.key":  jsonKeyStyle,
		"statusbar": statusModeStyle,
	} {
		if got := s.GetForeground(); got != (lipgloss.NoColor{}) {
			t.Errorf("%s foreground = %v, want NoColor", name, got)
		}
	}
	if focused != (lipgloss.NoColor{}) {
		t.Errorf("focused = %v, want NoColor", focused)
	}

	// Attributes that aren't colors survive: the UI still reads as structured.
	if !titleStyle.GetBold() {
		t.Error("title should stay bold without color")
	}
	if !selectedStyle.GetReverse() {
		t.Error("selection should stay reversed without color")
	}
}

func TestStatusBarUsesThemeStyles(t *testing.T) {
	restoreDefaultTheme(t)

	dark, _ := theme.Builtin(theme.Default)
	ApplyTheme(dark)
	got := statusBar(60, "hello", true, false)
	if got == "" {
		t.Fatal("statusBar rendered nothing")
	}
	// The mode chip reflects intercept state either way.
	if want := "req:on resp:off"; !strings.Contains(got, want) {
		t.Errorf("statusBar = %q, want it to contain %q", got, want)
	}
	if !strings.Contains(statusBar(60, "hi", false, false), "monitor") {
		t.Error("statusBar should show monitor when nothing is armed")
	}
}
