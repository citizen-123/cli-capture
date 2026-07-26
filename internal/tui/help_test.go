package tui

import (
	"strings"
	"testing"
)

func TestHelpViewCoversShortcuts(t *testing.T) {
	out := (Model{}).helpView()
	for _, want := range []string{
		"keyboard shortcuts",
		"switch pane",
		"intercept",
		"flag",
		"filter",
		"detail view",
		"inject",
		"Content-Length",
		"close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help view missing %q", want)
		}
	}
}
