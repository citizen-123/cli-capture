package repeater

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

// Raw renders the template as an editable raw HTTP request: request line,
// headers, blank line, body. {{markers}} in the template survive verbatim so the
// user can move or add them while editing.
func (t *Template) Raw() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", t.Method, requestURI(t.URL))
	if host := hostOf(t.URL); host != "" && t.Header.Get("Host") == "" {
		fmt.Fprintf(&b, "Host: %s\r\n", host)
	}
	keys := make([]string, 0, len(t.Header))
	for k := range t.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range t.Header[k] {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	b.Write(t.editableBody())
	return b.String()
}

// ParseRaw parses an edited raw request back into a Template, keeping the target
// scheme/host/secure from base (the origin we're repeating against). The body is
// taken as everything after the blank line — not by Content-Length — so an edit
// that leaves a stale Content-Length still sends the bytes the user sees.
func ParseRaw(raw string, base *Template) (*Template, error) {
	head, body := splitHeadBody(raw)

	// Normalize only the header block for parsing; the body is kept byte-for-byte
	// so a multi-line body isn't silently rewritten to CRLF.
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("repeater: empty request")
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 3 {
		return nil, fmt.Errorf("repeater: malformed request line %q", lines[0])
	}
	method, target := fields[0], fields[1]
	if !httpguts.ValidHeaderFieldName(method) {
		return nil, fmt.Errorf("repeater: invalid request method %q", method)
	}
	if _, _, ok := http.ParseHTTPVersion(fields[2]); !ok {
		return nil, fmt.Errorf("repeater: invalid HTTP version %q", fields[2])
	}
	target, err := parseTemplateTarget(target)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok || !httpguts.ValidHeaderFieldName(k) {
			return nil, fmt.Errorf("repeater: malformed request header %q", ln)
		}
		v = strings.Trim(v, " \t")
		if !httpguts.ValidHeaderFieldValue(v) {
			return nil, fmt.Errorf("repeater: invalid value for request header %q", k)
		}
		header.Add(k, v)
	}
	header.Del("Host") // the target host comes from base; don't send an edited Host twice

	scheme, host, secure := "http", "", false
	var proto capture.Protocol
	var messages []protocol.GRPCMessage
	if base != nil {
		secure = base.Secure
		proto = base.Protocol
		messages = cloneGRPCMessages(base.Messages)
		u, err := url.Parse(base.URL)
		if err != nil {
			return nil, fmt.Errorf("repeater: invalid base target: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("repeater: base target needs an HTTP or HTTPS origin")
		}
		scheme, host = u.Scheme, u.Host
	}

	templateBody := []byte(body)
	if proto == capture.ProtoGRPC {
		templateBody = nil
		original := ""
		if base != nil {
			original = string(base.editableBody())
		}
		if body != original {
			switch len(messages) {
			case 0:
				messages = []protocol.GRPCMessage{{Data: []byte(body)}}
			case 1:
				if messages[0].Compressed {
					return nil, fmt.Errorf("repeater: compressed gRPC payload cannot be edited as text")
				}
				messages[0].Data = []byte(body)
			default:
				return nil, fmt.Errorf("repeater: cannot map one edited body to %d gRPC messages", len(messages))
			}
		}
	}

	return &Template{
		Method:   method,
		URL:      scheme + "://" + host + target,
		Header:   header,
		Body:     templateBody,
		Messages: messages,
		Protocol: proto,
		Secure:   secure,
	}, nil
}

// Validate the target's shape without interpreting insertion points as URL
// escapes. Actual substituted values are validated when the template renders.
func parseTemplateTarget(target string) (string, error) {
	values := make(map[string]string)
	for _, name := range Variables(target) {
		values[name] = "00"
	}
	parsed, err := url.ParseRequestURI(Substitute(target, values))
	if err != nil || strings.Contains(target, "#") {
		return "", fmt.Errorf("repeater: invalid request target %q", target)
	}
	if strings.HasPrefix(target, "/") {
		return target, nil
	}
	if !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("repeater: request target must be a path or absolute HTTP URL")
	}
	// Keep the original path/query so {{markers}} and explicit escaping survive,
	// but discard the edited authority as required by the fixed-origin contract.
	authority := strings.Index(target, "://") + 3
	offset := strings.IndexAny(target[authority:], "/?")
	if offset < 0 {
		return "/", nil
	}
	path := target[authority+offset:]
	if path[0] == '?' {
		path = "/" + path
	}
	return path, nil
}

func (t *Template) editableBody() []byte {
	if t.Protocol == capture.ProtoGRPC && len(t.Messages) == 1 && !t.Messages[0].Compressed {
		return t.Messages[0].Data
	}
	return t.Body
}

func cloneGRPCMessages(messages []protocol.GRPCMessage) []protocol.GRPCMessage {
	out := make([]protocol.GRPCMessage, len(messages))
	for i, message := range messages {
		out[i] = protocol.GRPCMessage{
			Compressed: message.Compressed,
			Data:       append([]byte(nil), message.Data...),
		}
	}
	return out
}

// ParsePayloads parses "name = a, b, c" lines into name → payload list. Blank
// lines and #-comments are ignored; a value with no commas is a one-item list.
func ParsePayloads(text string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var vals []string
		for _, v := range strings.Split(rest, ",") {
			vals = append(vals, strings.TrimSpace(v))
		}
		out[name] = vals
	}
	return out
}

func requestURI(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.RequestURI()
	}
	return rawURL
}

func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Host
	}
	return ""
}

// splitHeadBody splits a raw request at the first blank line, tolerating LF or
// CRLF endings, and returns the body unchanged (not re-normalized).
func splitHeadBody(raw string) (head, body string) {
	crlf := strings.Index(raw, "\r\n\r\n")
	lf := strings.Index(raw, "\n\n")
	switch {
	case crlf < 0 && lf < 0:
		return raw, ""
	case crlf >= 0 && (lf < 0 || crlf <= lf):
		return raw[:crlf], raw[crlf+4:]
	default:
		return raw[:lf], raw[lf+2:]
	}
}
