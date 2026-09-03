package tui

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// sortMode orders the flow list. Off by default (insertion order); toggled for
// analyzing attack results, where sorting by status or size surfaces the outlier.
type sortMode int

const (
	sortNone sortMode = iota
	sortStatus
	sortSize
)

func (s sortMode) String() string {
	switch s {
	case sortStatus:
		return "status"
	case sortSize:
		return "size"
	default:
		return ""
	}
}

// rowCode returns the numeric HTTP status of a flow's response, or -1 if there
// is no response yet.
func rowCode(f *capture.Flow) int {
	if f.Response == nil {
		return -1
	}
	n := 0
	for _, r := range f.Response.Meta["status"] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return -1
	}
	return n
}

// codeText is the compact status column: the HTTP status code once a response
// arrives, otherwise a lifecycle marker (paused / error / in-flight).
func codeText(f *capture.Flow) string {
	if c := rowCode(f); c > 0 {
		return strconv.Itoa(c)
	}
	switch f.Status {
	case capture.StatusPending:
		return "PAUS"
	case capture.StatusError:
		return "ERR"
	case capture.StatusActive:
		return "···"
	default:
		return "·"
	}
}

// codeStyle colors the status column by HTTP class (2xx green, 3xx cyan, 4xx
// yellow, 5xx red) or by lifecycle state before a response exists.
func codeStyle(f *capture.Flow) lipgloss.Style {
	if c := rowCode(f); c > 0 {
		switch {
		case c < 300:
			return code2xxStyle
		case c < 400:
			return code3xxStyle
		case c < 500:
			return code4xxStyle
		default:
			return code5xxStyle
		}
	}
	switch f.Status {
	case capture.StatusPending:
		return pendingStyle
	case capture.StatusError:
		return code5xxStyle
	default:
		return dimStyle
	}
}

func respSize(f *capture.Flow) int {
	if f.Response == nil {
		return -1
	}
	return len(f.Response.Body)
}

func humanSize(n int) string {
	switch {
	case n < 0:
		return ""
	case n < 1024:
		return strconv.Itoa(n) + " B"
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// rowTitle is terminal-safe before it reaches either the list or a modal
// title. Flow titles and repeater payload labels both originate in captured
// traffic and can therefore contain terminal control sequences.
func rowTitle(f *capture.Flow) string {
	t := f.Title()
	if f.Request != nil {
		if pl := f.Request.Meta["payload"]; pl != "" {
			t += " [" + pl + "]"
		}
	}
	return sanitizeCaptureText(t)
}

// flagMark returns the one-cell marker for a row. The flag glyph is themeable
// and may be emptied ("remove the marker") — falling back to a space keeps the
// column, so flagged rows stay aligned with unflagged ones instead of shifting
// left. A themed glyph is expected to be a single-width marker.
func flagMark(flagged bool) string {
	if !flagged || glyphFlag == "" {
		return " "
	}
	return glyphFlag
}

// renderFlowRow formats one traffic-list row: flag mark, colored status code,
// title (+ payload), and response size.
func (m Model) renderFlowRow(f *capture.Flow, selected bool, w int) string {
	mark := flagMark(f.Flagged)
	code := fmt.Sprintf("%-4s", codeText(f))
	title := rowTitle(f)
	size := humanSize(respSize(f))

	if selected {
		// Plain text under a reverse highlight: embedding SGR colors here would
		// terminate the reverse at the first reset and break the highlight.
		plain := mark + " " + code + " " + title
		if size != "" {
			plain += "  " + size
		}
		return selectedStyle.Render(truncate(plain, w))
	}

	mk := mark
	if f.Flagged {
		mk = flagStyle.Render(mark)
	}
	seg := mk + " " + codeStyle(f).Render(code) + " " + title
	if size != "" {
		seg += "  " + dimStyle.Render(size)
	}
	return ansi.Truncate(seg, w, "…")
}
