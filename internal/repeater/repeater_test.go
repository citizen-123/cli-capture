package repeater

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func templFlow() *capture.Flow {
	f := capture.NewFlow("c", "api.example.com:443")
	f.Protocol = capture.ProtoHTTP1
	f.Secure = true
	f.Request = &capture.Message{
		Headers: http.Header{"Authorization": {"Bearer {{token}}"}, "Host": {"api.example.com"}},
		Body:    []byte(`{"q":"{{query}}"}`),
		Meta:    map[string]string{"method": "POST", "path": "/search?p={{query}}"},
	}
	return f
}

func TestFromFlowAndVariables(t *testing.T) {
	tmpl, err := FromFlow(templFlow())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Method != "POST" || tmpl.URL != "https://api.example.com:443/search?p={{query}}" {
		t.Errorf("template = %+v", tmpl)
	}
	got := tmpl.Variables()
	// url has {{query}}, header has {{token}}, body has {{query}} → distinct union
	want := map[string]bool{"query": true, "token": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("Variables = %v, want {query, token}", got)
	}
}

func TestFromFlowRejectsNonHTTP(t *testing.T) {
	f := capture.NewFlow("c", "x:1")
	f.Protocol = capture.ProtoWebSocket
	f.Request = &capture.Message{Meta: map[string]string{}}
	if _, err := FromFlow(f); err == nil {
		t.Error("expected websocket flow to be rejected")
	}
}

func TestRenderSubstitutes(t *testing.T) {
	tmpl, _ := FromFlow(templFlow())
	req, err := tmpl.Render(map[string]string{"token": "SECRET", "query": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.RawQuery != "p=hello" {
		t.Errorf("url query not substituted: %q", req.URL.RawQuery)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer SECRET" {
		t.Errorf("header not substituted: %q", got)
	}
	if req.Header.Get("Host") != "" {
		t.Error("Host header should be dropped (left to the client)")
	}
	body, _ := io.ReadAll(func() io.ReadCloser { rc, _ := req.GetBody(); return rc }())
	if string(body) != `{"q":"hello"}` {
		t.Errorf("body not substituted: %q", body)
	}
}

// TestSendEndToEnd renders a template against a real server and asserts the
// substituted values arrived and the response was captured as a flow.
func TestSendEndToEnd(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://") // host:port
	tmpl := &Template{
		Method: "POST",
		URL:    "http://" + addr + "/u/{{id}}",
		Header: http.Header{"Authorization": {"Bearer {{tok}}"}},
		Body:   []byte(`{"n":{{n}}}`),
	}

	flow, err := Send(tmpl, map[string]string{"id": "42", "tok": "T", "n": "7"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/u/42" || gotAuth != "Bearer T" || gotBody != `{"n":7}` {
		t.Errorf("server saw path=%q auth=%q body=%q", gotPath, gotAuth, gotBody)
	}
	if flow.Response == nil || string(flow.Response.Body) != "ok" {
		t.Errorf("response not captured: %+v", flow.Response)
	}
	if flow.Request == nil || flow.Request.Meta["path"] != "/u/42" {
		t.Errorf("request not captured with substituted path: %+v", flow.Request)
	}
}
