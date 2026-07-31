package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// wrapDetail renders a flow's detail and hard-wraps it to width. Hardwrap is
// ANSI-aware, so the JSON syntax colors are preserved across wrap points and
// long lines (big token strings, URLs) no longer clip off the right edge.
// Expansion of embedded JSON/escaped-newline strings is always on here; a
// future UI toggle can call renderFlowDetail directly with expand=false.
func wrapDetail(f *capture.Flow, width int) string {
	if width < 1 {
		width = 80
	}
	return ansi.Hardwrap(renderFlowDetail(f, width, true), width, false)
}

// renderFlowDetail formats a full view of one flow: metadata, request and
// response (headers + body), and the ordered message list for streaming flows.
// expand controls whether JSON-string bodies get their embedded
// JSON/escaped-newline values unwrapped (see prettyJSON) — a pure parameter
// rather than a package-level flag so a caller can render either way without
// touching shared state.
func renderFlowDetail(f *capture.Flow, width int, expand bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", f.Protocol, f.Title())
	fmt.Fprintf(&b, "server %s   sni %s   secure %v   status %s\n",
		f.ServerAddr, dash(f.SNI), f.Secure, f.Status)
	if f.Err != nil {
		fmt.Fprintf(&b, "error: %v\n", f.Err)
	}

	if f.Request != nil {
		b.WriteString("\n" + sectionStyle.Render("── REQUEST ──") + "\n")
		b.WriteString(renderMessage(f.Request, width, expand))
	}
	if f.Response != nil {
		b.WriteString("\n" + sectionStyle.Render("── RESPONSE ──") + "\n")
		b.WriteString(renderMessage(f.Response, width, expand))
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
				b.WriteString(indent(bodyPreview(m.Body, expand), "    ") + "\n")
			}
		}
	}
	return b.String()
}

func renderMessage(m *capture.Message, width int, expand bool) string {
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
		b.WriteString("\n" + bodyPreview(m.Body, expand) + "\n")
	}
	return b.String()
}

// bodyPreview shows JSON pretty-printed and syntax-highlighted, other textual
// bodies as-is, and binary bodies as a hex dump — all capped so a large body
// can't flood the pane. expand is threaded straight through to prettyJSON;
// this function itself never inspects string contents.
func bodyPreview(body []byte, expand bool) string {
	// JSON gets pretty-printed and colored (the detail view is scrollable, so a
	// larger cap is fine here than for the plain/hex fallbacks).
	if len(body) <= 256*1024 {
		if pretty, ok := prettyJSON(body, expand); ok {
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
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			switch {
			case k < len(s) && s[k] == ':':
				b.WriteString(jsonKeyStyle.Render(str))
			case expand:
				if exp, ok := expandString(str, lineIndent(s, i), embedDepth); ok {
					b.WriteString(exp)
				} else {
					b.WriteString(jsonStrStyle.Render(str))
				}
			default:
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
