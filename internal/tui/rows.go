package tui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

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
	for _, r := range responseStatus(f.Response) {
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

// rowTitle is the flow's title, plus the payload for attack results so a Sniper
// run reads as "same request, varying payload, varying status/size".
func rowTitle(f *capture.Flow) string {
	t := f.Title()
	if f.Request != nil {
		if pl := f.Request.Meta["payload"]; pl != "" {
			t += " [" + pl + "]"
		}
	}
	return inlineCaptureText(t)
}

// selectedFlowSummary retains identity that compact comparison rows may elide.
// The caller owns its display bounds; detail renders this without truncation.
func selectedFlowSummary(f *capture.Flow) string {
	host := f.SNI
	if host == "" {
		host = f.ServerAddr
	}
	if f.Request != nil {
		if requestHost := http.Header(f.Request.Headers).Get("Host"); requestHost != "" {
			host = requestHost
		}
	}
	return inlineCaptureText(string(f.Protocol)) + "  host " + inlineCaptureText(host) + "  " + rowTitle(f)
}

func inlineCaptureText(s string) string {
	return strings.ReplaceAll(sanitizeExpandedText(s), "\n", `\n`)
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

// renderFlowRow reserves status, response size, and actual capture protocol
// before spending remaining cells on the request target and attack payload.
func (m Model) renderFlowRow(f *capture.Flow, selected bool, w int) string {
	if w < 1 {
		return ""
	}
	mark := flagMark(f.Flagged)
	code := fmt.Sprintf("%-4s", codeText(f))
	size := humanSize(respSize(f))
	if size == "" {
		size = "—"
	}
	size = strings.Repeat(" ", max(0, 8-ansi.StringWidth(size))) + size
	protocol := inlineCaptureText(string(f.Protocol))
	protocol += strings.Repeat(" ", max(0, 9-ansi.StringWidth(protocol)))
	prefix := mark + " " + code + " " + size + " " + protocol
	titleWidth := w - ansi.StringWidth(prefix) - 1
	title := ""
	if titleWidth > 0 {
		title = " " + ansi.Truncate(rowTitle(f), titleWidth, "…")
	}
	if selected {
		// Keep the reverse highlight intact; nested SGR resets would end it.
		return selectedStyle.Render(ansi.Truncate(prefix+title, w, "…"))
	}
	if f.Flagged {
		mark = flagStyle.Render(mark)
	}
	row := mark + " " + codeStyle(f).Render(code) + " " + dimStyle.Render(size) + " " + protocol + title
	return ansi.Truncate(row, w, "…")
}
