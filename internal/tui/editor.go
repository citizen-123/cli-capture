package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
)

// newFilter builds the single-line input used to filter the flow list.
func newFilter() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter host/method/path/status"
	return ti
}

// newEditor builds the textarea used to edit a paused message before forwarding.
func newEditor() textarea.Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // no limit; requests can be large
	return ta
}

// fixContentLength recomputes the Content-Length header of a raw HTTP request to
// match its actual body length. It is bound to a key in the editor rather than
// applied automatically, because sending a deliberately wrong Content-Length is
// a legitimate thing to want to test. Returns the input unchanged if there is no
// header/body boundary.
func fixContentLength(raw string) string {
	sep := "\r\n\r\n"
	i := strings.Index(raw, sep)
	if i < 0 {
		// Tolerate bare-LF line endings that hand-editing may introduce.
		sep = "\n\n"
		i = strings.Index(raw, sep)
		if i < 0 {
			return raw
		}
	}
	head := raw[:i]
	body := raw[i+len(sep):]

	eol := "\r\n"
	if !strings.Contains(head, "\r\n") {
		eol = "\n"
	}
	lines := strings.Split(head, eol)
	found := false
	for idx, ln := range lines {
		if strings.HasPrefix(strings.ToLower(ln), "content-length:") {
			lines[idx] = fmt.Sprintf("Content-Length: %d", len(body))
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("Content-Length: %d", len(body)))
	}
	return strings.Join(lines, eol) + sep + body
}
