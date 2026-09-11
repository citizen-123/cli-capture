package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// jsonExpandStyle marks content this view has decoded from a JSON string —
// an embedded JSON payload or escaped-newline text — rather than what was
// literally on the wire. It is deliberately its own style, not a reuse of
// jsonKeyStyle/jsonStrStyle/etc., because those label JSON token *kinds*
// while this one labels a render-time transform: the reader must never
// confuse "this is what the string contained, unescaped" with "this is what
// the string looked like on the wire," since verbatim capture is the whole
// point of this tool.
var jsonExpandStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)

// maxEmbedDepth bounds how many levels of JSON-embedded-in-a-JSON-string this
// view will unwrap (tool-call args containing tool-call args containing MCP
// payloads, and so on). Captured traffic is adversarial by construction —
// this is a MITM proxy sitting on a target process's own bytes — so a
// malicious or malformed payload could otherwise nest escaped JSON arbitrarily
// deep and blow up render time/stack depth for no operator benefit. Six
// levels comfortably covers real agent/tool-call nesting seen in practice
// while keeping a hard ceiling.
const maxEmbedDepth = 6

// wrapDetail wraps the chosen display representation without changing capture bytes.
func wrapDetail(f *capture.Flow, width int, expand bool) string {
	if width < 1 {
		width = 80
	}
	return ansi.Hardwrap(renderFlowDetail(f, width, expand), width, false)
}

// renderFlowDetail is a bounded inspection preview, independent of full exports.
func renderFlowDetail(f *capture.Flow, width int, expand bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", selectedFlowSummary(f))
	fmt.Fprintf(&b, "server %s   sni %s   secure %v   lifecycle %s\n",
		sanitizeExpandedText(f.ServerAddr), sanitizeExpandedText(dash(f.SNI)), f.Secure, f.Status)
	if f.Response != nil {
		status := responseStatus(f.Response)
		if status != "" {
			fmt.Fprintf(&b, "HTTP response: %s\n", sanitizeExpandedText(status))
		}
	}
	mode := "raw captured representation"
	if expand {
		mode = "expanded display"
	}
	fmt.Fprintf(&b, "mode: %s\n", mode)
	if f.Err != nil {
		fmt.Fprintf(&b, "error: %s\n", sanitizeExpandedText(f.Err.Error()))
	}

	if f.Request != nil {
		b.WriteString("\n" + sectionStyle.Render("── REQUEST ──") + "\n")
		b.WriteString(detailMessage(f.Request, f.Protocol, width, expand))
	}
	if f.Response != nil {
		b.WriteString("\n" + sectionStyle.Render("── RESPONSE ──") + "\n")
		b.WriteString(detailMessage(f.Response, f.Protocol, width, expand))
	}
	if len(f.Messages) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionStyle.Render(fmt.Sprintf("── MESSAGES (%d) ──", len(f.Messages))))
		for i, m := range f.Messages {
			if i >= 300 {
				fmt.Fprintf(&b, "… %d messages omitted (%d total); export full stored content\n", len(f.Messages)-i, len(f.Messages))
				break
			}
			summary := strings.TrimSpace(m.Summary)
			// Older captures include the direction in Summary; Direction remains
			// authoritative, so strip that prefix before rendering it once.
			for strings.HasPrefix(summary, "→") || strings.HasPrefix(summary, "←") {
				summary = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(summary, "→"), "←"))
			}
			provenance := ""
			if m.Meta["injected"] == "true" {
				provenance = " [injected]"
			}
			fmt.Fprintf(&b, "%s %s%s\n", m.Direction, sanitizeExpandedText(summary), provenance)
			if len(m.Body) > 0 || (!expand && len(m.Raw) > 0) {
				contentWidth := max(1, width-4)
				var content string
				if expand {
					content = bodyPreview(m.Body, contentWidth, true)
				} else {
					content = rawMessage(m, f.Protocol, contentWidth)
				}
				b.WriteString(indent(content, "    ") + "\n")
			}
		}
	}
	return b.String()
}

func responseStatus(m *capture.Message) string {
	if status := strings.TrimSpace(m.Meta["status"]); status != "" {
		return status
	}
	summary := strings.TrimSpace(m.Summary)
	if strings.HasPrefix(summary, "HTTP/") {
		if _, rest, ok := strings.Cut(summary, " "); ok {
			summary = strings.TrimSpace(rest)
		}
	}
	if len(summary) >= 3 && summary[0] >= '1' && summary[0] <= '5' &&
		summary[1] >= '0' && summary[1] <= '9' && summary[2] >= '0' && summary[2] <= '9' {
		return summary
	}
	return ""
}

func detailMessage(m *capture.Message, protocol capture.Protocol, width int, expand bool) string {
	if !expand {
		if protocol == capture.ProtoWebSocket {
			// Request/Response hold the HTTP upgrade, not WebSocket frames.
			protocol = capture.ProtoHTTP1
		}
		return rawMessage(m, protocol, width) + "\n"
	}
	return renderMessage(m, width, true)
}

func rawMessage(m *capture.Message, protocol capture.Protocol, width int) string {
	data := m.Raw
	label := "Captured Raw (" + string(protocol) + ")"
	if protocol == capture.ProtoHTTP1 {
		label = "Captured serialized HTTP/1.1"
	} else if protocol == capture.ProtoWebSocket {
		label = "Captured WebSocket payload (not frame bytes)"
	}
	if len(data) == 0 {
		data = m.Body
		label = "Captured body fallback (" + string(protocol) + "; serialized bytes unavailable)"
	}
	return label + "\n" + capturedPreview(data, width)
}

func renderMessage(m *capture.Message, width int, expand bool) string {
	var b strings.Builder
	for _, k := range sortedKeys(m.Headers) {
		for _, v := range m.Headers[k] {
			fmt.Fprintf(&b, "%s: %s\n", sanitizeExpandedText(k), sanitizeExpandedText(v))
		}
	}
	if len(m.Meta) > 0 {
		for _, k := range sortedKeys2(m.Meta) {
			if k == "method" || k == "path" || k == "status" {
				continue // already in the summary/title
			}
			fmt.Fprintf(&b, "· %s: %s\n", sanitizeExpandedText(k), sanitizeExpandedText(m.Meta[k]))
		}
	}
	if len(m.Body) > 0 {
		b.WriteString("\nCaptured body · expanded display\n" + bodyPreview(m.Body, width, expand) + "\n")
	}
	return b.String()
}

// bodyPreview pretty-prints bounded JSON; other bodies use a safe text/hex preview.
func bodyPreview(body []byte, width int, expand bool) string {
	// JSON gets pretty-printed and colored (the detail view is scrollable, so a
	// larger cap is fine here than for the plain/hex fallbacks).
	if len(body) <= 256*1024 {
		if pretty, ok := prettyJSON(body, expand); ok {
			return pretty
		}
	}

	return capturedPreview(body, width)
}

func capturedPreview(body []byte, width int) string {
	const limit = 8192
	shown := body
	if len(shown) > limit {
		shown = shown[:limit]
	}
	var s string
	if isMostlyPrintable(shown) {
		s = sanitizeExpandedText(string(shown))
	} else {
		s = hexDump(shown, width)
	}
	if len(shown) < len(body) {
		s += fmt.Sprintf("\n… %d bytes omitted (%d bytes total); export full stored content", len(body)-len(shown), len(body))
	}
	return s
}

// prettyJSON reports whether body is JSON and, if so, returns it indented and
// syntax-highlighted. json.Indent both validates and pretty-prints; the
// colorizer then runs over the canonical output. When expand is true, string
// values that are themselves embedded JSON or escaped-newline text get
// unwrapped inline (see colorizeJSON/expandString) — a pure parameter, not a
// package-level flag, so this stays safe to call from anywhere without
// touching shared state and a future toggle can flip it per render.
func prettyJSON(body []byte, expand bool) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false // only object/array bodies, to avoid coloring bare strings/numbers
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return "", false
	}
	return colorizeJSON(buf.Bytes(), expand, 0), true
}

// colorizeJSON walks canonical (already-indented) JSON and applies syntax
// colors: object keys, string values, numbers, literals, and punctuation. A
// string is a key when the next non-space character after it is a colon.
//
// When expand is true, string VALUES (never keys) are checked for embedded
// JSON or escaped-newline text and unwrapped inline via expandString;
// embedDepth tracks how many such unwraps deep we already are so the
// recursion can be capped at maxEmbedDepth. This never mutates s — it only
// changes what gets rendered to the screen, so the underlying captured bytes
// (and every export path, which never calls this) stay verbatim.
func colorizeJSON(s []byte, expand bool, embedDepth int) string {
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
			safeStr := sanitizeExpandedText(str)
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			switch {
			case k < len(s) && s[k] == ':':
				b.WriteString(jsonKeyStyle.Render(safeStr))
			case expand:
				if exp, ok := expandString(str, lineIndent(s, i), embedDepth); ok {
					b.WriteString(exp)
				} else {
					b.WriteString(jsonStrStyle.Render(safeStr))
				}
			default:
				b.WriteString(jsonStrStyle.Render(safeStr))
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

// lineIndent returns the leading whitespace of the line containing byte
// offset pos in s. json.Indent's output uses exactly this indentation to mark
// nesting depth, so it's what we reuse to align an expanded value under the
// key that introduced it.
func lineIndent(s []byte, pos int) string {
	start := pos
	for start > 0 && s[start-1] != '\n' {
		start--
	}
	end := start
	for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
		end++
	}
	return string(s[start:end])
}

// expandString checks whether a JSON string VALUE token (raw, exactly as it
// appears in the source — quotes and escapes included) decodes to embedded
// JSON or to text containing escaped newlines, and if so returns it expanded
// and visibly marked. ok is false for anything else (plain text, truncated or
// merely JSON-shaped-but-invalid content), telling the caller to fall back to
// normal string coloring. This never panics: captured bodies are frequently
// truncated mid-string, and a string that merely starts with '{' is common
// (ordinary prose) and must not be treated as JSON.
func expandString(raw, prefix string, embedDepth int) (string, bool) {
	var decoded string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return "", false // not a well-formed JSON string literal (e.g. truncated capture)
	}

	if embedDepth < maxEmbedDepth {
		trimmed := strings.TrimSpace(decoded)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
			// Indent with an empty prefix so nesting is relative to zero; we add
			// our own prefix+marker to every resulting line below, uniformly.
			var buf bytes.Buffer
			if err := json.Indent(&buf, []byte(trimmed), "", "  "); err == nil {
				inner := colorizeJSON(buf.Bytes(), true, embedDepth+1)
				return markExpanded("↳ expanded JSON", inner, prefix), true
			}
		}
	}

	if strings.Contains(decoded, "\n") {
		// The decoded text is untrusted capture data. Preserve line breaks for
		// readability, but never pass other terminal control characters through
		// to the renderer. Normalize CRLF first so Windows line endings keep
		// their intended layout; a lone carriage return is rendered visibly.
		decoded = sanitizeExpandedText(strings.ReplaceAll(decoded, "\r\n", "\n"))
		lines := strings.Split(decoded, "\n")
		for i, ln := range lines {
			lines[i] = jsonStrStyle.Render(ln)
		}
		return markExpanded("↳ expanded (escaped \\n)", strings.Join(lines, "\n"), prefix), true
	}

	return "", false
}

// sanitizeExpandedText keeps actual newlines for layout while rendering
// terminal controls visibly. It runs before styling so it cannot alter the
// ANSI sequences produced by our own styles.
func sanitizeExpandedText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r <= 0x1f || r == 0x7f:
			fmt.Fprintf(&b, `\x%02X`, r)
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markExpanded prefixes every line of content with prefix plus a distinctly
// styled bar, and labels the block, so decoded/render-time content can never
// be mistaken for what was literally on the wire — even if the view is
// scrolled to a point where the label itself isn't visible, the bar on every
// line still carries the signal.
func markExpanded(label, content, prefix string) string {
	bar := prefix + jsonExpandStyle.Render("┃ ")
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		lines[i] = bar + ln
	}
	return jsonExpandStyle.Render(label) + "\n" + strings.Join(lines, "\n")
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if !utf8.Valid(b) {
		return false
	}
	printable := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*100/len(b) >= 85
}

func hexDump(b []byte, width int) string {
	var out strings.Builder
	// Offset, hex, and ASCII columns take 13 + 4*n cells. For widths
	// too small for those columns, omit offsets/ASCII rather than wrapping them.
	perRow := min(16, (width-13)/4)
	columns := perRow >= 1
	if !columns {
		perRow = max(1, width/3)
	}
	for i := 0; i < len(b); i += perRow {
		end := min(i+perRow, len(b))
		if columns {
			fmt.Fprintf(&out, "%08x  ", i)
		}
		for j := i; j < i+perRow; j++ {
			if j < end {
				fmt.Fprintf(&out, "%02x", b[j])
			} else if columns {
				out.WriteString("  ")
			}
			if columns || j+1 < end {
				out.WriteByte(' ')
			}
		}
		if !columns {
			out.WriteByte('\n')
			continue
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
