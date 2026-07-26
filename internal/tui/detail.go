package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// wrapDetail renders a flow's detail and hard-wraps it to width. Hardwrap is
// ANSI-aware, so the JSON syntax colors are preserved across wrap points and
// long lines (big token strings, URLs) no longer clip off the right edge.
func wrapDetail(f *capture.Flow, width int) string {
	if width < 1 {
		width = 80
	}
	return ansi.Hardwrap(renderFlowDetail(f, width), width, false)
}

// renderFlowDetail formats a full view of one flow: metadata, request and
// response (headers + body), and the ordered message list for streaming flows.
func renderFlowDetail(f *capture.Flow, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", f.Protocol, f.Title())
	fmt.Fprintf(&b, "server %s   sni %s   secure %v   status %s\n",
		f.ServerAddr, dash(f.SNI), f.Secure, f.Status)
	if f.Err != nil {
		fmt.Fprintf(&b, "error: %v\n", f.Err)
	}

	if f.Request != nil {
		b.WriteString("\n" + sectionStyle.Render("── REQUEST ──") + "\n")
		b.WriteString(renderMessage(f.Request, width))
	}
	if f.Response != nil {
		b.WriteString("\n" + sectionStyle.Render("── RESPONSE ──") + "\n")
		b.WriteString(renderMessage(f.Response, width))
	}
	if len(f.Messages) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionStyle.Render(fmt.Sprintf("── MESSAGES (%d) ──", len(f.Messages))))
		for i, m := range f.Messages {
			if i >= 300 {
				fmt.Fprintf(&b, "… %d more\n", len(f.Messages)-i)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", m.Direction, m.Summary)
			if len(m.Body) > 0 {
				b.WriteString(indent(bodyPreview(m.Body), "    ") + "\n")
			}
		}
	}
	return b.String()
}

func renderMessage(m *capture.Message, width int) string {
	var b strings.Builder
	for _, k := range sortedKeys(m.Headers) {
		for _, v := range m.Headers[k] {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	if len(m.Meta) > 0 {
		for _, k := range sortedKeys2(m.Meta) {
			if k == "method" || k == "path" || k == "status" {
				continue // already in the summary/title
			}
			fmt.Fprintf(&b, "· %s: %s\n", k, m.Meta[k])
		}
	}
	if len(m.Body) > 0 {
		b.WriteString("\n" + bodyPreview(m.Body) + "\n")
	}
	return b.String()
}

// bodyPreview shows JSON pretty-printed and syntax-highlighted, other textual
// bodies as-is, and binary bodies as a hex dump — all capped so a large body
// can't flood the pane.
func bodyPreview(body []byte) string {
	// JSON gets pretty-printed and colored (the detail view is scrollable, so a
	// larger cap is fine here than for the plain/hex fallbacks).
	if len(body) <= 256*1024 {
		if pretty, ok := prettyJSON(body); ok {
			return pretty
		}
	}

	const max = 8192
	b := body
	truncated := false
	if len(b) > max {
		b, truncated = b[:max], true
	}
	var s string
	if isMostlyPrintable(b) {
		s = string(b)
	} else {
		s = hexDump(b)
	}
	if truncated {
		s += fmt.Sprintf("\n… (%d bytes total)", len(body))
	}
	return s
}

// prettyJSON reports whether body is JSON and, if so, returns it indented and
// syntax-highlighted. json.Indent both validates and pretty-prints; the
// colorizer then runs over the canonical output.
func prettyJSON(body []byte) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false // only object/array bodies, to avoid coloring bare strings/numbers
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return "", false
	}
	return colorizeJSON(buf.Bytes()), true
}

// colorizeJSON walks canonical (already-indented) JSON and applies syntax
// colors: object keys, string values, numbers, literals, and punctuation. A
// string is a key when the next non-space character after it is a colon.
func colorizeJSON(s []byte) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == '"' {
					j++
					break
				}
				j++
			}
			str := string(s[i:j])
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			if k < len(s) && s[k] == ':' {
				b.WriteString(jsonKeyStyle.Render(str))
			} else {
				b.WriteString(jsonStrStyle.Render(str))
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(s) && (s[j] == '-' || s[j] == '+' || s[j] == '.' || s[j] == 'e' || s[j] == 'E' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			b.WriteString(jsonNumStyle.Render(string(s[i:j])))
			i = j
		case c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
				j++
			}
			b.WriteString(jsonLitStyle.Render(string(s[i:j])))
			i = j
		case c == '{' || c == '}' || c == '[' || c == ']' || c == ':' || c == ',':
			b.WriteString(jsonPunctStyle.Render(string(c)))
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	printable := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*100/len(b) >= 85
}

func hexDump(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); i += 16 {
		end := i + 16
		if end > len(b) {
			end = len(b)
		}
		fmt.Fprintf(&out, "%08x  ", i)
		for j := i; j < i+16; j++ {
			if j < end {
				fmt.Fprintf(&out, "%02x ", b[j])
			} else {
				out.WriteString("   ")
			}
		}
		out.WriteString(" |")
		for j := i; j < end; j++ {
			c := b[j]
			if c < 0x20 || c >= 0x7f {
				c = '.'
			}
			out.WriteByte(c)
		}
		out.WriteString("|\n")
	}
	return out.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedKeys(h map[string][]string) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys2(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
