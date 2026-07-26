package export

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func httpFlow() *capture.Flow {
	f := capture.NewFlow("c", "api.example.com:443")
	f.Protocol = capture.ProtoHTTP1
	f.Secure = true
	f.Request = &capture.Message{
		Headers: http.Header{"Content-Type": {"application/json"}, "Host": {"api.example.com"}},
		Body:    []byte(`{"a":1}`),
		Meta:    map[string]string{"method": "POST", "path": "/v1/x"},
	}
	f.Response = &capture.Message{
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"ok":true}`),
		Meta:    map[string]string{"status": "200 OK"},
	}
	return f
}

func TestCurl(t *testing.T) {
	got, err := Curl(httpFlow())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"curl -X POST 'https://api.example.com/v1/x'", "-H 'Content-Type: application/json'", `--data-binary '{"a":1}'`} {
		if !strings.Contains(got, want) {
			t.Errorf("curl output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-H 'Host:") {
		t.Error("Host header should be omitted from curl")
	}
}

func TestCurlRejectsNonHTTP(t *testing.T) {
	f := capture.NewFlow("c", "x:1")
	f.Protocol = capture.ProtoWebSocket
	f.Request = &capture.Message{Meta: map[string]string{}}
	if _, err := Curl(f); err == nil {
		t.Error("expected websocket curl export to be rejected")
	}
}

func TestFlowText(t *testing.T) {
	out := FlowText(httpFlow())
	for _, want := range []string{"POST", "/v1/x", "--- REQUEST ---", "--- RESPONSE ---", "application/json", `"a": 1`, `"ok": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("FlowText missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("text export must not contain ANSI color codes")
	}
}

func TestFlowsText(t *testing.T) {
	out := FlowsText([]*capture.Flow{httpFlow(), httpFlow()})
	if strings.Count(out, "--- REQUEST ---") != 2 {
		t.Errorf("both flows should be present:\n%s", out)
	}
	if !strings.Contains(out, "====") {
		t.Error("flows should be separated by a rule")
	}
}

func TestHAR(t *testing.T) {
	ws := capture.NewFlow("c", "x:1") // non-HTTP, must be skipped
	ws.Protocol = capture.ProtoWebSocket

	data, err := HAR([]*capture.Flow{httpFlow(), ws})
	if err != nil {
		t.Fatal(err)
	}
	var arc map[string]any
	if err := json.Unmarshal(data, &arc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	entries := arc["log"].(map[string]any)["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want 1 HAR entry (websocket skipped), got %d", len(entries))
	}
	req := entries[0].(map[string]any)["request"].(map[string]any)
	if req["method"] != "POST" || req["url"] != "https://api.example.com/v1/x" {
		t.Errorf("HAR request wrong: %v", req)
	}
	resp := entries[0].(map[string]any)["response"].(map[string]any)
	if resp["status"].(float64) != 200 {
		t.Errorf("HAR response status = %v, want 200", resp["status"])
	}
}
