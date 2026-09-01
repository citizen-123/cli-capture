package replay

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

	"github.com/andybalholm/brotli"
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

func TestResendReencodesCapturedGzipBody(t *testing.T) {
	logical := []byte(`{"message":"captured"}`)
	compressed := gzipTestBody(t, logical)
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

	if _, err := Resend(f); err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if decodeErr != nil {
		t.Fatalf("origin could not decode gzip request: %v", decodeErr)
	}
	if !bytes.Equal(got, logical) {
		t.Errorf("decoded request = %q, want %q", got, logical)
	}
	if gotLength <= 1 {
		t.Errorf("Content-Length = %d, want recalculated compressed length", gotLength)
	}
}

func TestResendFramesCapturedGRPCMessages(t *testing.T) {
	compressed := gzipTestBody(t, []byte("second"))
	tests := []struct {
		name       string
		encoding   string
		messages   []*capture.Message
		wantFrames []byte
	}{
		{
			name: "unary",
			messages: []*capture.Message{
				grpcCaptureMessage(capture.ClientToServer, false, []byte("unary")),
			},
			wantFrames: grpcTestFrame(false, []byte("unary")),
		},
		{
			name:     "multi-message",
			encoding: "gzip",
			messages: []*capture.Message{
				grpcCaptureMessage(capture.ClientToServer, false, []byte("first")),
				grpcCaptureMessage(capture.ServerToClient, false, []byte("ignored response")),
				grpcCaptureMessage(capture.ClientToServer, true, compressed),
			},
			wantFrames: append(grpcTestFrame(false, []byte("first")), grpcTestFrame(true, compressed)...),
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

			addr := strings.TrimPrefix(srv.URL, "http://")
			header := http.Header{"Content-Type": {"application/grpc"}}
			if tt.encoding != "" {
				header.Set("Grpc-Encoding", tt.encoding)
			}
			f := capture.NewFlow("c", addr)
			f.Protocol = capture.ProtoGRPC
			f.Request = &capture.Message{
				Headers: header,
				Meta:    map[string]string{"method": "POST", "path": "/service/call"},
			}
			f.Messages = tt.messages

			replayed, err := Resend(f)
			if err != nil {
				t.Fatalf("Resend: %v", err)
			}
			if !bytes.Equal(got, tt.wantFrames) {
				t.Errorf("wire body = %x, want %x", got, tt.wantFrames)
			}
			if gotLength != int64(len(tt.wantFrames)) {
				t.Errorf("Content-Length = %d, want %d", gotLength, len(tt.wantFrames))
			}
			if len(replayed.Messages) != len(tt.messages)-countServerMessages(tt.messages) {
				t.Errorf("replayed request messages = %d", len(replayed.Messages))
			}
		})
	}
}

func TestResendDechunksCapturedHTTP1Body(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	chunked := []byte("7\r\npayload\r\n0\r\n\r\n")
	raw := append(
		[]byte(fmt.Sprintf("POST /chunked HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n", addr)),
		chunked...,
	)
	f := capture.NewFlow("c", addr)
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{
		Body: chunked,
		Raw:  raw,
		Meta: map[string]string{"method": "POST", "path": "/chunked"},
	}

	if _, err := Resend(f); err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("origin received %q, want payload", got)
	}
}

func TestResendAppliesRepeatedContentEncodingsInOrder(t *testing.T) {
	logical := []byte("chained")
	wire := brotliTestBody(t, gzipTestBody(t, logical))
	var got []byte
	var decodeErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gzipReader, err := gzip.NewReader(brotli.NewReader(r.Body))
		if err != nil {
			decodeErr = err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer gzipReader.Close()
		got, decodeErr = io.ReadAll(gzipReader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	raw := append(
		[]byte(fmt.Sprintf("POST /chain HTTP/1.1\r\nHost: %s\r\nContent-Encoding: gzip\r\nContent-Encoding: br\r\nContent-Length: %d\r\n\r\n", addr, len(wire))),
		wire...,
	)
	f := capture.NewFlow("c", addr)
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{
		Headers: http.Header{"Content-Encoding": {"gzip", "br"}},
		Body:    logical,
		Raw:     raw,
		Meta:    map[string]string{"method": "POST", "path": "/chain"},
	}

	if _, err := Resend(f); err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if decodeErr != nil {
		t.Fatalf("origin could not decode chained request: %v", decodeErr)
	}
	if !bytes.Equal(got, logical) {
		t.Errorf("decoded request = %q, want %q", got, logical)
	}
}

func TestResendRejectsTruncatedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	}))
	defer srv.Close()

	flow, err := Resend(replayResponseTestFlow(strings.TrimPrefix(srv.URL, "http://")))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Resend error = %v, want unexpected EOF", err)
	}
	assertReplayResponseFailure(t, flow, err)
}

func TestResendRejectsResponseReadFailureAndClosesBody(t *testing.T) {
	readErr := errors.New("response stream reset")
	body := &replayFaultBody{
		data:     []byte("partial"),
		readErr:  readErr,
		closeErr: errors.New("response close failed"),
	}
	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = &http.Client{Transport: replayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}

	flow, err := Resend(replayResponseTestFlow("example.test"))
	if !errors.Is(err, readErr) {
		t.Fatalf("Resend error = %v, want %v", err, readErr)
	}
	if !body.closed {
		t.Error("response body was not closed")
	}
	assertReplayResponseFailure(t, flow, err)
}

func TestResendRetainsCompleteResponseWhenCloseFails(t *testing.T) {
	body := &replayFaultBody{
		data:     []byte("complete"),
		closeErr: errors.New("response close failed"),
	}
	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = &http.Client{Transport: replayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}

	flow, err := Resend(replayResponseTestFlow("example.test"))
	if err != nil {
		t.Fatalf("Resend error = %v, want nil", err)
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

func assertReplayResponseFailure(t *testing.T, flow *capture.Flow, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Resend returned a nil error")
	}
	if flow == nil {
		t.Fatal("Resend returned no failed flow")
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

func replayResponseTestFlow(addr string) *capture.Flow {
	flow := capture.NewFlow("c", addr)
	flow.Protocol = capture.ProtoHTTP1
	flow.Request = &capture.Message{
		Meta: map[string]string{"method": http.MethodGet, "path": "/"},
	}
	return flow
}

type replayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f replayRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type replayFaultBody struct {
	data     []byte
	readErr  error
	closeErr error
	read     bool
	closed   bool
}

func (b *replayFaultBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	return copy(p, b.data), b.readErr
}

func (b *replayFaultBody) Close() error {
	b.closed = true
	return b.closeErr
}

func brotliTestBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := brotli.NewWriter(&out)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func gzipTestBody(t *testing.T, body []byte) []byte {
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

func grpcTestFrame(compressed bool, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	if compressed {
		frame[0] = 1
	}
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func grpcCaptureMessage(direction capture.Direction, compressed bool, payload []byte) *capture.Message {
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

func countServerMessages(messages []*capture.Message) int {
	count := 0
	for _, message := range messages {
		if message.Direction == capture.ServerToClient {
			count++
		}
	}
	return count
}
