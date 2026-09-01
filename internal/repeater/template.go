package repeater

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

// Template is a parameterized HTTP request. {{name}} markers in the URL, header
// values, or body are filled in at render time.
type Template struct {
	Method   string
	URL      string
	Header   http.Header
	Body     []byte
	Messages []protocol.GRPCMessage
	Protocol capture.Protocol
	Secure   bool
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

	body, err := protocol.CapturedRequestBody(f)
	if err != nil {
		return nil, fmt.Errorf("repeater: reconstruct request body: %w", err)
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
		Method:   method,
		URL:      scheme + "://" + f.ServerAddr + f.Request.Meta["path"],
		Header:   cloneHeader(f.Request.Headers),
		Body:     body.Body,
		Messages: body.Messages,
		Protocol: f.Protocol,
		Secure:   f.Secure,
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
	for _, message := range t.Messages {
		if !message.Compressed {
			add(string(message.Data))
		}
	}
	return out
}

// Render substitutes vars and builds a concrete *http.Request. Substitution is
// applied to logical HTTP bodies and uncompressed gRPC payloads before their
// final wire encoding. Host and Content-Length are always left to the client.
func (t *Template) Render(vars map[string]string) (*http.Request, error) {
	req, _, _, err := t.render(vars)
	return req, err
}

func (t *Template) render(vars map[string]string) (*http.Request, protocol.RequestBody, capture.Protocol, error) {
	proto := t.Protocol
	if proto == "" {
		proto = capture.ProtoHTTP1
	}

	header := make(http.Header, len(t.Header))
	for k, vals := range t.Header {
		for _, value := range vals {
			header.Add(k, Substitute(value, vars))
		}
	}

	logical := protocol.RequestBody{
		Body:     []byte(Substitute(string(t.Body), vars)),
		Messages: make([]protocol.GRPCMessage, len(t.Messages)),
	}
	for i, message := range t.Messages {
		logical.Messages[i] = protocol.GRPCMessage{
			Compressed: message.Compressed,
			Data:       append([]byte(nil), message.Data...),
		}
		if !message.Compressed {
			logical.Messages[i].Data = []byte(Substitute(string(message.Data), vars))
		}
	}

	wire, err := protocol.EncodeRequestBody(proto, header, logical)
	if err != nil {
		return nil, protocol.RequestBody{}, proto, fmt.Errorf("repeater: reconstruct request body: %w", err)
	}
	req, err := http.NewRequest(t.Method, Substitute(t.URL, vars), bytes.NewReader(wire))
	if err != nil {
		return nil, protocol.RequestBody{}, proto, err
	}
	for k, vals := range header {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, value := range vals {
			req.Header.Add(k, value)
		}
	}
	return req, logical, proto, nil
}
