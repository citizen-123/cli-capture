package repeater

import (
	"net/http"
	"strings"
	"testing"
)

func TestRawRoundTrip(t *testing.T) {
	tmpl := &Template{
		Method: "POST",
		URL:    "https://api.example.com:443/search?p={{q}}",
		Header: http.Header{"Authorization": {"Bearer {{tok}}"}},
		Body:   []byte(`{"q":"{{q}}"}`),
		Secure: true,
	}
	raw := tmpl.Raw()
	if !strings.Contains(raw, "POST /search?p={{q}} HTTP/1.1") {
		t.Errorf("raw request line wrong:\n%s", raw)
	}
	if !strings.Contains(raw, "Host: api.example.com:443") {
		t.Errorf("raw missing Host:\n%s", raw)
	}

	got, err := ParseRaw(raw, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" || got.URL != "https://api.example.com:443/search?p={{q}}" {
		t.Errorf("round-trip URL/method wrong: %+v", got)
	}
	if got.Header.Get("Authorization") != "Bearer {{tok}}" {
		t.Errorf("round-trip header wrong: %q", got.Header.Get("Authorization"))
	}
	if string(got.Body) != `{"q":"{{q}}"}` {
		t.Errorf("round-trip body wrong: %q", got.Body)
	}
}

// TestParseRawIgnoresStaleContentLength proves the body is taken by the blank-
// line boundary, not by a Content-Length that editing left stale.
func TestParseRawIgnoresStaleContentLength(t *testing.T) {
	base := &Template{URL: "http://h/x"}
	raw := "POST /x HTTP/1.1\nHost: h\nContent-Length: 3\n\nthis body is much longer than three bytes"
	got, err := ParseRaw(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != "this body is much longer than three bytes" {
		t.Errorf("body should follow the blank line, got %q", got.Body)
	}
	if got.Header.Get("Host") != "" {
		t.Error("Host should be dropped (target host comes from base)")
	}
}

// TestParseRawPreservesMultilineBody: a body with newlines must survive
// byte-for-byte (not be rewritten to CRLF).
func TestParseRawPreservesMultilineBody(t *testing.T) {
	base := &Template{URL: "http://h/x"}
	raw := "POST /x HTTP/1.1\nHost: h\n\nline1\nline2\nline3"
	got, err := ParseRaw(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != "line1\nline2\nline3" {
		t.Errorf("body should be preserved verbatim, got %q", got.Body)
	}
}

func TestParsePayloads(t *testing.T) {
	m := ParsePayloads("user = alice, bob , carol\n# a comment\ntok=X\n\nempty=")
	if len(m["user"]) != 3 || m["user"][1] != "bob" || m["user"][2] != "carol" {
		t.Errorf("user list wrong: %v", m["user"])
	}
	if len(m["tok"]) != 1 || m["tok"][0] != "X" {
		t.Errorf("tok wrong: %v", m["tok"])
	}
	if _, ok := m["# a comment"]; ok {
		t.Error("comment line should be ignored")
	}
}

func TestParseRawRejectsMalformedTemplates(t *testing.T) {
	base := &Template{URL: "http://origin.example/seed"}
	for name, raw := range map[string]string{
		"missing version":              "GET /path\n\n",
		"extra request token":          "GET /path HTTP/1.1 extra\n\n",
		"method":                       "G@T /path HTTP/1.1\n\n",
		"version":                      "GET /path not-http\n\n",
		"header without colon":         "GET /path HTTP/1.1\nAuthorization secret\n\n",
		"header name whitespace":       "GET /path HTTP/1.1\nBad Name: value\n\n",
		"space before header colon":    "GET /path HTTP/1.1\nX-Test : value\n\n",
		"header control value":         "GET /path HTTP/1.1\nX-Test: a\x00b\n\n",
		"header bare carriage return":  "GET /path HTTP/1.1\nX-Test: a\rb\n\n",
		"relative target":              "GET path HTTP/1.1\n\n",
		"target fragment":              "GET /path#fragment HTTP/1.1\n\n",
		"invalid percent escape":       "GET /%ZZ HTTP/1.1\n\n",
		"non HTTP absolute target":     "GET ftp://other.example/path HTTP/1.1\n\n",
		"absolute target without host": "GET http:///path HTTP/1.1\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if parsed, err := ParseRaw(raw, base); err == nil || parsed != nil {
				t.Fatalf("malformed request accepted: template=%+v, error=%v", parsed, err)
			}
		})
	}
}

func TestParseRawKeepsFixedOriginAndFuzzableTarget(t *testing.T) {
	base := &Template{URL: "https://origin.example:443/seed", Secure: true}
	for _, target := range []string{
		"/{{id}}/%{{escaped}}?q={{query}}",
		"http://other.example/{{id}}/%{{escaped}}?q={{query}}",
	} {
		t.Run(target, func(t *testing.T) {
			raw := "POST " + target + " HTTP/1.1\nX-Value: {{header}}\nContent-Length: 999\n\nbody\r\nbytes"
			parsed, err := ParseRaw(raw, base)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.URL != "https://origin.example:443/{{id}}/%{{escaped}}?q={{query}}" {
				t.Fatalf("template target lost insertion points or changed origin: %q", parsed.URL)
			}
			req, err := parsed.Render(map[string]string{"id": "user", "escaped": "41", "query": "%2F", "header": "雪"})
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.Host != "origin.example:443" || req.URL.Scheme != "https" || req.URL.RequestURI() != "/user/%41?q=%2F" {
				t.Fatalf("rendered target = %s", req.URL)
			}
			if req.Header.Get("X-Value") != "雪" || string(parsed.Body) != "body\r\nbytes" || parsed.Header.Get("Content-Length") != "999" {
				t.Fatal("target validation changed valid fuzzed values, body bytes, or explicit length")
			}
		})
	}
}

func TestParseRawRejectsInvalidOrigin(t *testing.T) {
	for _, origin := range []string{"http://%zz/path", "http:///path", "ftp://origin.example/path"} {
		if parsed, err := ParseRaw("GET /path HTTP/1.1\n\n", &Template{URL: origin}); err == nil || parsed != nil {
			t.Fatalf("invalid origin %q accepted: %+v, %v", origin, parsed, err)
		}
	}
}
