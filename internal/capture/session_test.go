package capture

import (
	"bytes"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	f := NewFlow("client:1", "api.example.com:443")
	f.Protocol = ProtoHTTP1
	f.SNI = "api.example.com"
	f.Secure = true
	f.Status = StatusComplete
	f.Flagged = true
	f.Request = &Message{
		Direction: ClientToServer,
		Summary:   "GET /v1 HTTP/1.1",
		Body:      []byte("hello"),
		Raw:       []byte("GET /v1 HTTP/1.1\r\n\r\nhello"),
		Meta:      map[string]string{"method": "GET", "path": "/v1"},
	}
	f.Response = &Message{Direction: ServerToClient, Summary: "200 OK", Body: []byte("world")}

	var buf bytes.Buffer
	if err := Save(&buf, []*Flow{f}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 flow, got %d", len(got))
	}
	g := got[0]
	if g.ID != f.ID || g.SNI != f.SNI || g.Protocol != f.Protocol || !g.Secure || !g.Flagged {
		t.Errorf("flow scalars not preserved: %+v", g)
	}
	if g.Request == nil || string(g.Request.Body) != "hello" || g.Request.Meta["method"] != "GET" {
		t.Errorf("request not preserved: %+v", g.Request)
	}
	if g.Response == nil || string(g.Response.Body) != "world" {
		t.Errorf("response not preserved: %+v", g.Response)
	}
}
