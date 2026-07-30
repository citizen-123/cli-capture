package repeater

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// Template is a parameterized HTTP request. {{name}} markers in the URL, header
// values, or body are filled in at render time.
type Template struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
	Secure bool
}

// FromFlow builds a template from a captured flow's request. Only
// request/response protocols make sense to repeat.
func FromFlow(f *capture.Flow) (*Template, error) {
	if f.Request == nil {
		return nil, fmt.Errorf("repeater: flow has no request")
	}
	switch f.Protocol {
	case capture.ProtoHTTP1, capture.ProtoHTTP2, capture.ProtoGRPC:
	default:
		return nil, fmt.Errorf("repeater: %s flows are not repeatable", f.Protocol)
	}
	scheme := "http"
	if f.Secure {
		scheme = "https"
	}
	method := f.Request.Meta["method"]
	if method == "" {
		method = http.MethodGet
	}
	return &Template{
		Method: method,
		URL:    scheme + "://" + f.ServerAddr + f.Request.Meta["path"],
		Header: cloneHeader(f.Request.Headers),
		Body:   append([]byte(nil), f.Request.Body...),
		Secure: f.Secure,
	}, nil
}

// cloneHeader copies a captured message's headers (a plain map) into an
// http.Header we own.
func cloneHeader(h map[string][]string) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Variables returns every distinct variable referenced across the URL, header
// values, and body — the set a caller must supply values for.
func (t *Template) Variables() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		for _, v := range Variables(s) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	add(t.URL)
	for _, vals := range t.Header {
		for _, v := range vals {
			add(v)
		}
	}
	add(string(t.Body))
	return out
}

// Render substitutes vars and builds a concrete *http.Request. Host and
// Content-Length are left to the client so a stale captured value can't desync
// the resend. The returned request's GetBody is set, so the rendered body can
// be re-read (Send uses this to capture what it sent).
func (t *Template) Render(vars map[string]string) (*http.Request, error) {
	url := Substitute(t.URL, vars)
	body := []byte(Substitute(string(t.Body), vars))
	req, err := http.NewRequest(t.Method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vals := range t.Header {
		if k == "Host" || k == "Content-Length" {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, Substitute(v, vars))
		}
	}
	return req, nil
}
