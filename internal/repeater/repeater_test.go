package repeater

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
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

func TestSendReencodesCapturedGzipBodyAfterSubstitution(t *testing.T) {
	var got []byte
	var gotLength int64
	var decodeErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLength = r.ContentLength
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			decodeErr = err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer reader.Close()
		got, decodeErr = io.ReadAll(reader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	logical := []byte(`{"message":"{{value}}"}`)
	compressed := repeaterGzipBody(t, logical)
	raw := append(
		[]byte(fmt.Sprintf("POST /gzip HTTP/1.1\r\nHost: %s\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", addr, len(compressed))),
		compressed...,
	)
	f := capture.NewFlow("c", addr)
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{
		Headers: http.Header{"Content-Encoding": {"gzip"}, "Content-Length": {"1"}},
		Body:    logical,
		Raw:     raw,
		Meta:    map[string]string{"method": "POST", "path": "/gzip"},
	}
	base, err := FromFlow(f)
	if err != nil {
		t.Fatalf("FromFlow: %v", err)
	}
	edited, err := ParseRaw(base.Raw(), base)
	if err != nil {
		t.Fatalf("ParseRaw: %v", err)
	}

	repeated, err := Send(edited, map[string]string{"value": "substituted"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := []byte(`{"message":"substituted"}`)
	if decodeErr != nil {
		t.Fatalf("origin could not decode gzip request: %v", decodeErr)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded request = %q, want %q", got, want)
	}
	if gotLength <= 1 {
		t.Errorf("Content-Length = %d, want recalculated compressed length", gotLength)
	}
	if !bytes.Equal(repeated.Request.Body, want) {
		t.Errorf("captured repeated logical body = %q, want %q", repeated.Request.Body, want)
	}
}

func TestSendFramesCapturedGRPCMessages(t *testing.T) {
	compressed := repeaterGzipBody(t, []byte("second"))
	tests := []struct {
		name       string
		encoding   string
		vars       map[string]string
		messages   []*capture.Message
		wantFrames []byte
	}{
		{
			name: "unary",
			vars: map[string]string{"value": "rendered"},
			messages: []*capture.Message{
				repeaterGRPCMessage(capture.ClientToServer, false, []byte("{{value}}")),
			},
			wantFrames: repeaterGRPCFrame(false, []byte("rendered")),
		},
		{
			name:     "multi-message",
			encoding: "gzip",
			vars:     map[string]string{"value": "rendered"},
			messages: []*capture.Message{
				repeaterGRPCMessage(capture.ClientToServer, false, []byte("first-{{value}}")),
				repeaterGRPCMessage(capture.ServerToClient, false, []byte("ignored response")),
				repeaterGRPCMessage(capture.ClientToServer, true, compressed),
			},
			wantFrames: append(repeaterGRPCFrame(false, []byte("first-rendered")), repeaterGRPCFrame(true, compressed)...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []byte
			var gotLength int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotLength = r.ContentLength
				got, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			header := http.Header{"Content-Type": {"application/grpc"}}
			if tt.encoding != "" {
				header.Set("Grpc-Encoding", tt.encoding)
			}
			f := capture.NewFlow("c", strings.TrimPrefix(srv.URL, "http://"))
			f.Protocol = capture.ProtoGRPC
			f.Request = &capture.Message{
				Headers: header,
				Meta:    map[string]string{"method": "POST", "path": "/service/call"},
			}
			f.Messages = tt.messages
			base, err := FromFlow(f)
			if err != nil {
				t.Fatalf("FromFlow: %v", err)
			}
			edited, err := ParseRaw(base.Raw(), base)
			if err != nil {
				t.Fatalf("ParseRaw: %v", err)
			}

			repeated, err := Send(edited, tt.vars)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if !bytes.Equal(got, tt.wantFrames) {
				t.Errorf("wire body = %x, want %x", got, tt.wantFrames)
			}
			if gotLength != int64(len(tt.wantFrames)) {
				t.Errorf("Content-Length = %d, want %d", gotLength, len(tt.wantFrames))
			}
			if len(repeated.Messages) != len(edited.Messages) {
				t.Errorf("captured repeated request messages = %d, want %d", len(repeated.Messages), len(edited.Messages))
			}
		})
	}
}

func TestSendRejectsTruncatedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	}))
	defer srv.Close()

	flow, err := Send(&Template{Method: http.MethodGet, URL: srv.URL}, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Send error = %v, want unexpected EOF", err)
	}
	assertRepeaterResponseFailure(t, flow, err)
}

func TestSendRejectsResponseReadFailureAndClosesBody(t *testing.T) {
	readErr := errors.New("response stream reset")
	body := &repeaterFaultBody{
		data:     []byte("partial"),
		readErr:  readErr,
		closeErr: errors.New("response close failed"),
	}
	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = &http.Client{Transport: repeaterRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}

	flow, err := Send(&Template{Method: http.MethodGet, URL: "http://example.test/"}, nil)
	if !errors.Is(err, readErr) {
		t.Fatalf("Send error = %v, want %v", err, readErr)
	}
	if !body.closed {
		t.Error("response body was not closed")
	}
	assertRepeaterResponseFailure(t, flow, err)
}

func TestSendRetainsCompleteResponseWhenCloseFails(t *testing.T) {
	body := &repeaterFaultBody{
		data:     []byte("complete"),
		closeErr: errors.New("response close failed"),
	}
	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = &http.Client{Transport: repeaterRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}

	flow, err := Send(&Template{Method: http.MethodGet, URL: "http://example.test/"}, nil)
	if err != nil {
		t.Fatalf("Send error = %v, want nil", err)
	}
	if !body.closed {
		t.Error("response body was not closed")
	}
	if flow.Status != capture.StatusComplete {
		t.Errorf("flow status = %v, want complete", flow.Status)
	}
	if flow.Response == nil || string(flow.Response.Body) != "complete" {
		t.Errorf("complete response was not retained: %+v", flow.Response)
	}
}

func assertRepeaterResponseFailure(t *testing.T, flow *capture.Flow, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Send returned a nil error")
	}
	if flow == nil {
		t.Fatal("Send returned no failed flow")
	}
	if flow.Status != capture.StatusError {
		t.Errorf("flow status = %v, want error", flow.Status)
	}
	if flow.Err == nil {
		t.Error("flow did not retain the response error")
	}
	if flow.Response != nil {
		t.Errorf("partial response was retained: %+v", flow.Response)
	}
}

type repeaterRoundTripFunc func(*http.Request) (*http.Response, error)

func (f repeaterRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type repeaterFaultBody struct {
	data     []byte
	readErr  error
	closeErr error
	read     bool
	closed   bool
}

func (b *repeaterFaultBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	return copy(p, b.data), b.readErr
}

func (b *repeaterFaultBody) Close() error {
	b.closed = true
	return b.closeErr
}

func TestRenderRejectsUnsupportedBodyRepresentations(t *testing.T) {
	t.Run("content encoding", func(t *testing.T) {
		template := &Template{
			Method: "POST",
			URL:    "http://example.test/",
			Header: http.Header{"Content-Encoding": {"compress"}},
			Body:   []byte("logical"),
		}
		if _, err := template.Render(nil); err == nil || !strings.Contains(err.Error(), "unsupported Content-Encoding") {
			t.Fatalf("Render error = %v", err)
		}
	})

	t.Run("ambiguous legacy encoded body", func(t *testing.T) {
		f := capture.NewFlow("c", "example.test:443")
		f.Protocol = capture.ProtoHTTP1
		f.Request = &capture.Message{
			Headers: http.Header{"Content-Encoding": {"gzip"}},
			Body:    repeaterGzipBody(t, []byte("wire bytes")),
			Meta:    map[string]string{"method": "POST", "path": "/"},
		}
		if _, err := FromFlow(f); err == nil || !strings.Contains(err.Error(), "representation is unavailable") {
			t.Fatalf("FromFlow error = %v", err)
		}
	})

	t.Run("gRPC compression metadata", func(t *testing.T) {
		f := capture.NewFlow("c", "example.test:443")
		f.Protocol = capture.ProtoGRPC
		f.Request = &capture.Message{
			Headers: http.Header{"Content-Type": {"application/grpc"}},
			Meta:    map[string]string{"method": "POST", "path": "/service/call"},
		}
		f.Messages = []*capture.Message{
			repeaterGRPCMessage(capture.ClientToServer, true, []byte("compressed")),
		}
		if _, err := FromFlow(f); err == nil || !strings.Contains(err.Error(), "Grpc-Encoding") {
			t.Fatalf("FromFlow error = %v", err)
		}
	})
}

func TestRenderKeepsEmptyBodyEmpty(t *testing.T) {
	template := &Template{
		Method: "POST",
		URL:    "http://example.test/",
		Header: http.Header{"Content-Encoding": {"gzip"}},
	}
	req, err := template.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || req.ContentLength != 0 {
		t.Errorf("empty request body = %x, Content-Length = %d", got, req.ContentLength)
	}
}

func repeaterGzipBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func repeaterGRPCFrame(compressed bool, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	if compressed {
		frame[0] = 1
	}
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func repeaterGRPCMessage(direction capture.Direction, compressed bool, payload []byte) *capture.Message {
	meta := map[string]string{}
	if compressed {
		meta["compressed"] = "true"
	}
	return &capture.Message{
		Direction: direction,
		Body:      append([]byte(nil), payload...),
		Raw:       append([]byte(nil), payload...),
		Meta:      meta,
	}
}
