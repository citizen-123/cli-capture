package tui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestRenderFlowDetailHTTP(t *testing.T) {
	f := capture.NewFlow("c", "api.example.com:443")
	f.Protocol = capture.ProtoHTTP1
	f.SNI = "api.example.com"
	f.Secure = true
	f.Status = capture.StatusComplete
	f.Request = &capture.Message{
		Direction: capture.ClientToServer,
		Summary:   "GET /v1/users HTTP/1.1",
		Headers:   http.Header{"Authorization": {"Bearer xyz"}},
		Body:      []byte("hello"),
		Meta:      map[string]string{"method": "GET", "path": "/v1/users"},
	}
	f.Response = &capture.Message{Direction: capture.ServerToClient, Summary: "200 OK", Body: []byte("world")}

	out := renderFlowDetail(f, 80)
	for _, want := range []string{"REQUEST", "RESPONSE", "Authorization: Bearer xyz", "hello", "world", "api.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestBodyPreviewHexForBinary(t *testing.T) {
	binary := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x80}
	out := bodyPreview(binary)
	if !strings.Contains(out, "00 01 02 ff fe 80") {
		t.Errorf("binary body should hex-dump: %q", out)
	}

	text := []byte("plain readable text")
	if got := bodyPreview(text); got != "plain readable text" {
		t.Errorf("text body should render as-is, got %q", got)
	}
}

func TestPrettyJSON(t *testing.T) {
	body := []byte(`{"name":"ada","age":36,"admin":true,"tags":["x","y"]}`)
	out, ok := prettyJSON(body)
	if !ok {
		t.Fatal("prettyJSON should detect a JSON object")
	}
	if !strings.Contains(out, "\n  ") {
		t.Errorf("output should be indented:\n%s", out)
	}
	for _, want := range []string{"name", "ada", "36", "true", "tags"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty JSON lost %q:\n%s", want, out)
		}
	}
}

func TestPrettyJSONRejectsNonJSON(t *testing.T) {
	if _, ok := prettyJSON([]byte("hello world")); ok {
		t.Error("plain text should not be treated as JSON")
	}
	if _, ok := prettyJSON([]byte(`{"bad": }`)); ok {
		t.Error("invalid JSON should be rejected")
	}
	if _, ok := prettyJSON([]byte(`"just a string"`)); ok {
		t.Error("a bare JSON string should not be prettified")
	}
}

func TestColorizeJSONKeyVsStringValue(t *testing.T) {
	// A colon after a string marks it a key; distinguish key from value coloring
	// by structure (both render, we just verify content survives and it doesn't panic).
	out := colorizeJSON([]byte("{\n  \"k\": \"v\"\n}"))
	if !strings.Contains(out, "k") || !strings.Contains(out, "v") {
		t.Errorf("colorize dropped content: %q", out)
	}
}

func TestBodyPreviewTruncates(t *testing.T) {
	big := make([]byte, 20000)
	for i := range big {
		big[i] = 'a'
	}
	out := bodyPreview(big)
	if !strings.Contains(out, "20000 bytes total") {
		t.Errorf("large body should note truncation: tail=%q", out[len(out)-40:])
	}
}
