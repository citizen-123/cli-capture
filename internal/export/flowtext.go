package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// FlowText renders a single flow as a plain-text (no color) dump: metadata,
// request and response headers and bodies, and any streaming messages. JSON
// bodies are pretty-printed. Suitable for writing to a .txt file.
func FlowText(f *capture.Flow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", f.Protocol, f.Title())
	fmt.Fprintf(&b, "server=%s sni=%s secure=%v status=%s\n", f.ServerAddr, dash(f.SNI), f.Secure, f.Status)

	if f.Request != nil {
		b.WriteString("\n--- REQUEST ---\n")
		if line := requestLine(f); line != "" {
			b.WriteString(line + "\n")
		}
		writeMessageText(&b, f.Request)
	}
	if f.Response != nil {
		b.WriteString("\n--- RESPONSE ---\n")
		if st := f.Response.Meta["status"]; st != "" {
			fmt.Fprintf(&b, "HTTP %s\n", st)
		}
		writeMessageText(&b, f.Response)
	}
	if len(f.Messages) > 0 {
		fmt.Fprintf(&b, "\n--- MESSAGES (%d) ---\n", len(f.Messages))
		for _, m := range f.Messages {
			fmt.Fprintf(&b, "%s %s\n", m.Direction, m.Summary)
			if len(m.Body) > 0 {
				b.WriteString(indentText(bodyText(m.Body), "  "))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// FlowsText concatenates several flows into one document, separated by rules —
// used to export all flagged flows for a run.
func FlowsText(flows []*capture.Flow) string {
	var b strings.Builder
	for i, f := range flows {
		if i > 0 {
			b.WriteString("\n" + strings.Repeat("=", 72) + "\n\n")
		}
		b.WriteString(FlowText(f))
	}
	return b.String()
}

// requestLine reconstructs "METHOD path" from the request metadata.
func requestLine(f *capture.Flow) string {
	return strings.TrimSpace(f.Request.Meta["method"] + " " + f.Request.Meta["path"])
}

func writeMessageText(b *strings.Builder, m *capture.Message) {
	for _, k := range sortedKeys(m.Headers) {
		for _, v := range m.Headers[k] {
			fmt.Fprintf(b, "%s: %s\n", k, v)
		}
	}
	if len(m.Body) > 0 {
		b.WriteString("\n")
		b.WriteString(bodyText(m.Body))
		b.WriteString("\n")
	}
}

// bodyText renders a body as text: pretty-printed if JSON, verbatim if textual,
// or a size note if binary (a .txt file is no place for a hex dump).
func bodyText(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var buf bytes.Buffer
		if json.Indent(&buf, trimmed, "", "  ") == nil {
			return buf.String()
		}
	}
	if isTextual(body) {
		return string(body)
	}
	return fmt.Sprintf("[%d bytes binary]", len(body))
}

func isTextual(b []byte) bool {
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

func indentText(s, prefix string) string {
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
