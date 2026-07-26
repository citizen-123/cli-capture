package replay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestResendHTTP(t *testing.T) {
	var gotMethod, gotPath, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotHeader = r.Method, r.URL.Path, r.Header.Get("X-Marker")
		w.WriteHeader(201)
		w.Write([]byte("replayed-body"))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://") // host:port
	f := capture.NewFlow("c", addr)
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{
		Headers: http.Header{"X-Marker": {"orig"}},
		Meta:    map[string]string{"method": "POST", "path": "/replay/me"},
		Body:    []byte("payload"),
	}

	nf, err := Resend(f)
	if err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/replay/me" || gotHeader != "orig" {
		t.Errorf("server saw method=%s path=%s marker=%s", gotMethod, gotPath, gotHeader)
	}
	if nf.Response == nil || string(nf.Response.Body) != "replayed-body" {
		t.Errorf("resend response not captured: %+v", nf.Response)
	}
	if nf.Response.Meta["status"] != "201 Created" {
		t.Errorf("status = %q", nf.Response.Meta["status"])
	}
}

func TestResendRejectsNonHTTP(t *testing.T) {
	f := capture.NewFlow("c", "x:1")
	f.Protocol = capture.ProtoRawTCP
	f.Request = &capture.Message{Meta: map[string]string{}}
	if _, err := Resend(f); err == nil {
		t.Error("expected raw-TCP resend to be rejected")
	}
}
