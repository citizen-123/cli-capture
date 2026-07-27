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
