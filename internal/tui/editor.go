package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/runeutil"
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
	ta.CharLimit = 0
	ta.MaxHeight = 0
	return ta
}

// messageEditBuffer owns bytes which a textarea cannot represent. Only header
// display is normalized; body replacement is a separate, explicit operation.
type messageEditBuffer struct {
	original       string
	text           string
	body           string
	displayedBody  string
	headerLines    []string
	displayedLines []string
	httpMessage    bool
	hasBoundary    bool
	preservedBody  bool
	preservedAll   bool
	headerNotice   bool
	replacedBody   bool
}

func newMessageEditBuffer(raw []byte, httpMessage bool) messageEditBuffer {
	b := messageEditBuffer{original: string(raw), httpMessage: httpMessage}
	head := ""
	b.body = b.original
	if httpMessage {
		head, b.body, b.hasBoundary = splitEditMessage(b.original)
		b.headerLines = strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
		b.displayedLines = make([]string, len(b.headerLines))
		// Preserve unchanged header values even when tabs or other unsupported
		// characters need a safe display. A bare CR must not create a separator.
		sanitizer := runeutil.NewSanitizer(runeutil.ReplaceNewlines("\\r"))
		for i, line := range b.headerLines {
			b.displayedLines[i] = string(sanitizer.Sanitize([]rune(line)))
			if b.displayedLines[i] == "" && line != "" {
				b.displayedLines[i] = "[preserved header]"
			}
			b.headerNotice = b.headerNotice || b.displayedLines[i] != line
		}
		head = strings.Join(b.displayedLines, "\n")
	}
	b.displayedBody = b.body
	// Bubbles has a fixed 10,000-line insertion limit, independent of
	// CharLimit/MaxHeight. Keep oversized bodies out of that lossy path too.
	lines := strings.Count(head, "\n") + strings.Count(b.body, "\n") + 1
	if b.hasBoundary {
		lines += 2
	}
	if !editorTextSafe(b.body) || lines > 10000 {
		b.preservedBody = true
		b.displayedBody = fmt.Sprintf("[preserved body: %d bytes; use replace body to edit]", len(b.body))
	}
	b.text = b.displayedBody
	if httpMessage {
		b.text = head
		if b.hasBoundary {
			b.text += "\n\n" + b.displayedBody
		}
	}
	// Oversized headers cannot be made editable by hiding only the body.
	if strings.Count(b.text, "\n") >= 10000 {
		b.preservedAll = true
		b.text = fmt.Sprintf("[preserved message: %d bytes; headers exceed editor capacity]", len(raw))
	}
	return b
}

func (b *messageEditBuffer) Text() string { return b.text }

func (b *messageEditBuffer) Notice() string {
	switch {
	case b.preservedAll:
		return "Message preserved; headers exceed editor capacity"
	case b.preservedBody:
		return "Body bytes preserved; use replace body for text editing"
	case b.headerNotice:
		return "Unchanged headers retain original bytes; edited lines send displayed text"
	default:
		return ""
	}
}

// Bytes reconstructs the operator's draft without implicitly repairing framing.
func (b *messageEditBuffer) Bytes(text string) ([]byte, error) {
	if text == b.text && !b.replacedBody {
		return []byte(b.original), nil
	}
	if b.preservedAll {
		return nil, fmt.Errorf("message headers exceed editor capacity; original bytes are preserved")
	}
	head, body, boundary := "", text, false
	if b.httpMessage {
		head, body, boundary = splitEditMessage(text)
		if b.hasBoundary && !boundary {
			return nil, fmt.Errorf("keep a blank line between HTTP headers and body")
		}
	}
	if b.preservedBody && body != b.displayedBody {
		return nil, fmt.Errorf("body bytes are preserved; use replace body before editing them")
	}
	if body == b.displayedBody && !b.replacedBody {
		body = b.body
	}
	if !b.httpMessage {
		return []byte(body), nil
	}
	// Match unchanged lines by value rather than position so adding/removing a
	// header does not sanitize its neighbors. Consume duplicates in order.
	originals := make(map[string][]string, len(b.headerLines))
	for i, line := range b.displayedLines {
		originals[line] = append(originals[line], b.headerLines[i])
	}
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if values := originals[line]; len(values) > 0 {
			lines[i] = values[0]
			originals[line] = values[1:]
		}
	}
	out := strings.Join(lines, "\r\n")
	if boundary {
		out += "\r\n\r\n" + body
	}
	return []byte(out), nil
}

// ReplaceBody clears the displayed body and records explicit replacement intent,
// including when the new text happens to equal the old placeholder.
func (b *messageEditBuffer) ReplaceBody(text string) (string, error) {
	if b.preservedAll {
		return "", fmt.Errorf("message headers exceed editor capacity; original bytes are preserved")
	}
	replacement := ""
	if b.httpMessage {
		head, _, boundary := splitEditMessage(text)
		if !boundary {
			return "", fmt.Errorf("keep a blank line between HTTP headers and body")
		}
		replacement = head + "\n\n"
	}
	b.preservedBody = false
	b.replacedBody = true
	return replacement, nil
}

// FixLength repairs one effective Content-Length using reconstructed body bytes.
// Ordinary Bytes calls deliberately retain even incorrect length headers.
func (b *messageEditBuffer) FixLength(text string) (string, error) {
	if !b.httpMessage {
		return "", fmt.Errorf("Content-Length repair requires an HTTP message")
	}
	raw, err := b.Bytes(text)
	if err != nil {
		return "", err
	}
	head, body, boundary := splitEditMessage(string(raw))
	if !boundary {
		return "", fmt.Errorf("keep a blank line between HTTP headers and body")
	}
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	out := lines[:0]
	found := false
	for i, line := range lines {
		name, _, ok := strings.Cut(line, ":")
		if i > 0 && ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if found {
				continue
			}
			line = fmt.Sprintf("Content-Length: %d", len(body))
			found = true
		}
		out = append(out, line)
	}
	if !found {
		out = append(out, fmt.Sprintf("Content-Length: %d", len(body)))
	}
	*b = newMessageEditBuffer([]byte(strings.Join(out, "\r\n")+"\r\n\r\n"+body), true)
	return b.Text(), nil
}

func splitEditMessage(raw string) (head, body string, boundary bool) {
	crlf, lf := strings.Index(raw, "\r\n\r\n"), strings.Index(raw, "\n\n")
	switch {
	case crlf >= 0 && (lf < 0 || crlf <= lf):
		return raw[:crlf], raw[crlf+4:], true
	case lf >= 0:
		return raw[:lf], raw[lf+2:], true
	default:
		return raw, "", false
	}
}

func editorTextSafe(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if r == utf8.RuneError || (r != '\n' && unicode.IsControl(r)) {
			return false
		}
	}
	return true
}
