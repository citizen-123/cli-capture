package terminal

import (
	"strings"
	"testing"
)

func TestNewlineAndCarriageReturn(t *testing.T) {
	s := New()
	s.Write([]byte("first\nsecond"))
	out := s.Render(80, 10)
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("lines lost: %q", out)
	}

	// \r should reset the current line (progress-bar style overwrite).
	s2 := New()
	s2.Write([]byte("50%\r100%"))
	if got := s2.Render(80, 1); got != "100%" {
		t.Fatalf("carriage return not handled: %q", got)
	}
}

func TestSGRColorsPreservedButCursorDropped(t *testing.T) {
	s := New()
	s.Write([]byte("\x1b[31mred\x1b[0m\x1b[2Jtext"))
	out := s.Render(80, 1)
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf("SGR color escape should be preserved: %q", out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("clear-screen escape should be dropped: %q", out)
	}
	if !strings.Contains(out, "red") || !strings.Contains(out, "text") {
		t.Errorf("visible text lost: %q", out)
	}
}
