package tui

import (
	"encoding/json"
	"errors"
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

	out := renderFlowDetail(f, 80, true)
	for _, want := range []string{"REQUEST", "RESPONSE", "Authorization: Bearer xyz", "hello", "world", "api.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestBodyPreviewHexForBinary(t *testing.T) {
	binary := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x80}
	out := bodyPreview(binary, true)
	if !strings.Contains(out, "00 01 02 ff fe 80") {
		t.Errorf("binary body should hex-dump: %q", out)
	}

	text := []byte("plain readable text")
	if got := bodyPreview(text, true); got != "plain readable text" {
		t.Errorf("text body should render as-is, got %q", got)
	}
}

func TestBodyPreviewEmpty(t *testing.T) {
	if got := bodyPreview([]byte{}, true); got != "" {
		t.Errorf("empty body should render as empty, got %q", got)
	}
	if got := bodyPreview(nil, false); got != "" {
		t.Errorf("nil body should render as empty, got %q", got)
	}
}

func TestPrettyJSON(t *testing.T) {
	body := []byte(`{"name":"ada","age":36,"admin":true,"tags":["x","y"]}`)
	out, ok := prettyJSON(body, true)
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
	if strings.Contains(out, "expanded") {
		t.Errorf("plain JSON with no embedded strings should show no expansion marker:\n%s", out)
	}
}

func TestPrettyJSONRejectsNonJSON(t *testing.T) {
	if _, ok := prettyJSON([]byte("hello world"), true); ok {
		t.Error("plain text should not be treated as JSON")
	}
	if _, ok := prettyJSON([]byte(`{"bad": }`), true); ok {
		t.Error("invalid JSON should be rejected")
	}
	if _, ok := prettyJSON([]byte(`"just a string"`), true); ok {
		t.Error("a bare JSON string should not be prettified")
	}
}

func TestPrettyJSONTruncatedOrMalformed(t *testing.T) {
	// Captured traffic is frequently truncated mid-body or mid-string; this
	// must degrade to "not JSON" rather than panic.
	cases := [][]byte{
		[]byte(`{"a": `),
		[]byte(`{"a": "unterminated`),
		[]byte(`{"a": "esc\`),
		[]byte(`[1, 2,`),
		[]byte(`{`),
	}
	for _, body := range cases {
		if out, ok := prettyJSON(body, true); ok {
			t.Errorf("truncated body %q should not be treated as valid JSON, got %q", body, out)
		}
	}
}

func TestPrettyJSONExpandsEmbeddedJSONString(t *testing.T) {
	// The tool-call-arguments shape: a string value that is itself a JSON
	// object, escaped one level.
	body := []byte(`{"input": "{\"command\":\"ls -la\",\"timeout\":30}"}`)
	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("prettyJSON should accept the outer object")
	}
	for _, want := range []string{"input", "command", "ls -la", "timeout", "30", "expanded"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded output missing %q:\n%s", want, out)
		}
	}
	// The literal escaped form must not remain — it should have been unwrapped.
	if strings.Contains(out, `\"command\"`) {
		t.Errorf("embedded JSON should be unwrapped, not left escaped:\n%s", out)
	}
}

func TestPrettyJSONExpandsDoublyNestedJSONString(t *testing.T) {
	// A payload containing an embedded JSON string that itself contains
	// another embedded JSON string (MCP-style nesting). Built via json.Marshal
	// so the escaping is correct by construction rather than hand-typed.
	leaf := []byte(`{"leaf":true}`)
	leafStr, err := json.Marshal(string(leaf))
	if err != nil {
		t.Fatalf("marshal leaf: %v", err)
	}
	inner := []byte(`{"innerKey":` + string(leafStr) + `}`)
	innerStr, err := json.Marshal(string(inner))
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	body := []byte(`{"outer":` + string(innerStr) + `}`)

	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("prettyJSON should accept the outer object")
	}
	for _, want := range []string{"outer", "innerKey", "leaf", "true"} {
		if !strings.Contains(out, want) {
			t.Errorf("doubly-nested expansion missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "expanded"); n < 2 {
		t.Errorf("expected two expansion markers (one per nesting level), got %d:\n%s", n, out)
	}
}

func TestPrettyJSONExpandsEscapedNewlines(t *testing.T) {
	body := []byte(`{"text": "line one\nline two\nline three"}`)
	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("prettyJSON should accept the outer object")
	}
	for _, want := range []string{"line one", "line two", "line three", "expanded"} {
		if !strings.Contains(out, want) {
			t.Errorf("newline-expanded output missing %q:\n%s", want, out)
		}
	}
	// The escaped form ("line one\nline two" as one literal run) must be gone.
	if strings.Contains(out, `line one\nline two`) {
		t.Errorf("escaped newline should have been expanded, not left literal:\n%s", out)
	}
}

func TestExpandStringRendersTerminalControlsVisibly(t *testing.T) {
	// json.Unmarshal turns the JSON escapes below into live terminal control
	// characters. Expanded text must render every control as a harmless visible
	// escape, including its untrusted line break.
	raw, err := json.Marshal("first line\nsecond \x1b[31mred\a\u009b1;2R")
	if err != nil {
		t.Fatalf("marshal expanded text: %v", err)
	}

	out, ok := expandString(string(raw), "", 0)
	if !ok {
		t.Fatal("escaped-newline text should expand")
	}
	for _, want := range []string{`first line\x0Asecond \x1B[31mred\x07\u009B1;2R`} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded output missing safe text %q: %q", want, out)
		}
	}
	for _, control := range []string{"\x1b", "\a", "\u009b"} {
		if strings.Contains(out, control) {
			t.Errorf("expanded output emitted terminal control %q: %q", control, out)
		}
	}
}

func TestPrettyJSONExpandedEmbeddedJSONRendersTerminalControlsVisibly(t *testing.T) {
	inner, err := json.Marshal(map[string]string{
		"text": "first line\nsecond \x1b[31mred\a\u009b1;2R",
	})
	if err != nil {
		t.Fatalf("marshal embedded JSON: %v", err)
	}
	body, err := json.Marshal(map[string]string{"input": string(inner)})
	if err != nil {
		t.Fatalf("marshal outer JSON: %v", err)
	}

	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("outer JSON should render")
	}
	if !strings.Contains(out, "second \\x1B[31mred\\x07\\u009B1;2R") {
		t.Errorf("embedded JSON expansion did not render controls visibly: %q", out)
	}
	for _, control := range []string{"\x1b", "\a", "\u009b"} {
		if strings.Contains(out, control) {
			t.Errorf("embedded JSON expansion emitted terminal control %q: %q", control, out)
		}
	}
}

func TestPrettyJSONExpandedEmbeddedJSONRendersC1InKeyVisibly(t *testing.T) {
	inner := `{"key` + "\u009d" + `":"value"}`
	body, err := json.Marshal(map[string]string{"input": inner})
	if err != nil {
		t.Fatalf("marshal outer JSON: %v", err)
	}

	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("outer JSON should render")
	}
	if !strings.Contains(out, `"key\u009D"`) {
		t.Errorf("embedded JSON key did not render C1 visibly: %q", out)
	}
	if strings.Contains(out, "\u009d") {
		t.Errorf("embedded JSON key emitted a C1 control: %q", out)
	}
}

func TestPrettyJSONExpandedEmbeddedJSONRendersC1InSingleLineValueVisibly(t *testing.T) {
	inner := `{"key":"value` + "\u009b" + `1;2R"}`
	body, err := json.Marshal(map[string]string{"input": inner})
	if err != nil {
		t.Fatalf("marshal outer JSON: %v", err)
	}

	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("outer JSON should render")
	}
	if !strings.Contains(out, `"value\u009B1;2R"`) {
		t.Errorf("embedded JSON value did not render C1 visibly: %q", out)
	}
	if strings.Contains(out, "\u009b") {
		t.Errorf("embedded JSON value emitted a C1 control: %q", out)
	}
}

func TestPrettyJSONLeavesJSONShapedPrefixAlone(t *testing.T) {
	// A string that merely starts with '{' but isn't valid JSON must stay a
	// plain string — this is ordinary prose ("{not really json"), not a bug.
	body := []byte(`{"note": "{not really json"}`)
	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("prettyJSON should accept the outer object")
	}
	if !strings.Contains(out, "{not really json") {
		t.Errorf("non-JSON string starting with '{' should render literally:\n%s", out)
	}
	if strings.Contains(out, "expanded") {
		t.Errorf("non-JSON string should not be marked as expanded:\n%s", out)
	}
}

func TestPrettyJSONExpandFalseLeavesLiteralEscapes(t *testing.T) {
	body := []byte(`{"input": "{\"command\":\"ls -la\"}"}`)
	out, ok := prettyJSON(body, false)
	if !ok {
		t.Fatal("prettyJSON should accept the outer object")
	}
	if strings.Contains(out, "expanded") {
		t.Errorf("expand=false must not expand embedded JSON:\n%s", out)
	}
	if !strings.Contains(out, `\"command\"`) {
		t.Errorf("expand=false should leave the escaped literal intact:\n%s", out)
	}
}

func TestColorizeJSONKeyVsStringValue(t *testing.T) {
	// A colon after a string marks it a key; distinguish key from value coloring
	// by structure (both render, we just verify content survives and it doesn't panic).
	out := colorizeJSON([]byte("{\n  \"k\": \"v\"\n}"), true, 0)
	if !strings.Contains(out, "k") || !strings.Contains(out, "v") {
		t.Errorf("colorize dropped content: %q", out)
	}
}

func TestBodyPreviewTruncates(t *testing.T) {
	big := make([]byte, 20000)
	for i := range big {
		big[i] = 'a'
	}
	out := bodyPreview(big, true)
	if !strings.Contains(out, "20000 bytes total") {
		t.Errorf("large body should note truncation: tail=%q", out[len(out)-40:])
	}
}

func TestPrettyJSONLargeBodyWithManyEmbeddedStrings(t *testing.T) {
	// Confidence check that expansion over a large, deeply-populated body
	// (many sibling embedded-JSON strings, not deep nesting) completes and
	// doesn't panic or corrupt content.
	type item struct {
		ID    int    `json:"id"`
		Input string `json:"input"`
	}
	items := make([]item, 500)
	for i := range items {
		args, err := json.Marshal(map[string]any{"command": "ls -la", "n": i})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		items[i] = item{ID: i, Input: string(args)}
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}

	out, ok := prettyJSON(body, true)
	if !ok {
		t.Fatal("prettyJSON should accept a large valid array")
	}
	for _, want := range []string{"command", "ls -la", "expanded"} {
		if !strings.Contains(out, want) {
			t.Errorf("large body expansion missing %q", want)
		}
	}
}

func TestSanitizeCaptureTextEscapesEveryC0AndC1Control(t *testing.T) {
	var input strings.Builder
	for r := rune(0); r <= 0x1f; r++ {
		input.WriteRune(r)
	}
	input.WriteRune(0x7f)
	for r := rune(0x80); r <= 0x9f; r++ {
		input.WriteRune(r)
	}

	got := sanitizeCaptureText(input.String())
	for _, r := range got {
		if r <= 0x1f || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("sanitized text retained control %U in %q", r, got)
		}
	}
	for _, want := range []string{`\x00`, `\x1F`, `\x7F`, `\u0080`, `\u009F`} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitized text missing %q: %q", want, got)
		}
	}
}

func TestCaptureEditorSeedEscapesControlsAndKeepsLines(t *testing.T) {
	unsafe := "\x1b]8;;https://attacker.example\x07"
	m := Model{
		ta:        newEditor(),
		pausedMsg: &capture.Message{Raw: []byte("GET / HTTP/1.1\r\nX-Test: " + unsafe + "\r\n\r\nbody")},
	}
	m.enterEdit()
	if got := m.ta.Value(); strings.Contains(got, unsafe) {
		t.Fatalf("editor seed emitted unsafe control sequence %q", got)
	}
	if got := m.ta.Value(); !strings.Contains(got, `\x1B]8;;https://attacker.example\x07`) {
		t.Errorf("editor seed did not escape terminal control: %q", got)
	}
	if got := m.ta.Value(); got != strings.ReplaceAll(got, "\r", "") {
		t.Errorf("editor seed retained carriage return: %q", got)
	}
	if got := m.ta.Value(); !strings.Contains(got, "\nX-Test:") {
		t.Errorf("editor seed lost request line boundaries: %q", got)
	}
}

func TestRenderFlowDetailEscapesCapturedTerminalControls(t *testing.T) {
	osc := "\x1b]8;;https://attacker.example\x07"
	c1 := "\u009b1;2R"
	f := capture.NewFlow("client", "server"+osc)
	f.Protocol = capture.Protocol("http" + c1)
	f.SNI = "sni" + osc
	f.Err = errors.New("error" + c1)
	f.Request = &capture.Message{
		Direction: capture.ClientToServer,
		Summary:   "GET " + osc,
		Headers:   map[string][]string{"X" + c1: {"value" + osc}},
		Meta:      map[string]string{"meta" + osc: "value" + c1},
		Body:      []byte(strings.Repeat("text ", 24) + "\n" + osc + c1),
	}
	f.Response = &capture.Message{Summary: "response" + c1}
	f.Messages = []*capture.Message{{Direction: capture.ServerToClient, Summary: "stream" + osc, Body: []byte("body" + c1)}}

	out := renderFlowDetail(f, 80, true)
	for _, unsafe := range []string{osc, c1} {
		if strings.Contains(out, unsafe) {
			t.Errorf("detail emitted untrusted terminal sequence %q: %q", unsafe, out)
		}
	}
	for _, safe := range []string{`\x1B]8;;https://attacker.example\x07`, `\u009B1;2R`, `\x0A`} {
		if !strings.Contains(out, safe) {
			t.Errorf("detail missing escaped capture text %q: %q", safe, out)
		}
	}
}
