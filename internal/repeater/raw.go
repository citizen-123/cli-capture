package repeater

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
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
	b.Write(t.Body)
	return b.String()
}

// ParseRaw parses an edited raw request back into a Template, keeping the target
// scheme/host/secure from base (the origin we're repeating against). The body is
// taken as everything after the blank line — not by Content-Length — so an edit
// that leaves a stale Content-Length still sends the bytes the user sees.
func ParseRaw(raw string, base *Template) (*Template, error) {
	text := normalizeCRLF(raw)
	head, body, _ := strings.Cut(text, "\r\n\r\n")

	lines := strings.Split(head, "\r\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("repeater: empty request")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		return nil, fmt.Errorf("repeater: malformed request line %q", lines[0])
	}
	method, target := fields[0], fields[1]

	header := http.Header{}
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	header.Del("Host") // the target host comes from base; don't send an edited Host twice

	scheme, host, secure := "http", "", false
	if base != nil {
		secure = base.Secure
		if u, err := url.Parse(base.URL); err == nil {
			if u.Scheme != "" {
				scheme = u.Scheme
			}
			host = u.Host
		}
	}
	// If the edited target is absolute, honor its path/query (but keep base host).
	if u, err := url.Parse(target); err == nil && u.IsAbs() {
		target = u.RequestURI()
	}

	return &Template{
		Method: method,
		URL:    scheme + "://" + host + target,
		Header: header,
		Body:   []byte(body),
		Secure: secure,
	}, nil
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

// normalizeCRLF makes line endings CRLF regardless of what the editor produced.
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
