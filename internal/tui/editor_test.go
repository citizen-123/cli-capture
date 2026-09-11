package tui

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

func TestMessageEditBufferTextareaPreservesBytes(t *testing.T) {
	cases := []struct {
		name  string
		start string
		body  string
	}{
		{"empty GET", "GET /search?q=one HTTP/1.1", ""},
		{"response CRLF body", "HTTP/1.1 404 Not Found", "first\r\nsecond\r\n"},
		{"binary", "POST / HTTP/1.1", "\x00\x01\xff\x1b[31m\x7f"},
		{"tabs", "POST / HTTP/1.1", "first\tsecond"},
		{"replacement rune", "POST / HTTP/1.1", "valid UTF-8 \ufffd"},
		{"LF text", "POST / HTTP/1.1", "first\nsecond\n"},
		{"UTF-8", "POST / HTTP/1.1", "snow: 雪"},
		{"textarea line limit", "POST / HTTP/1.1", strings.Repeat("line\n", 10000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.start + "\r\nHost: example.test\r\nX-Keep:\tunchanged\r\nX-Edit: before\r\nContent-Length: 999\r\n\r\n" + tc.body
			buffer := newMessageEditBuffer([]byte(raw), true)
			ta := newEditor()
			ta.SetValue(buffer.Text())
			got, err := buffer.Bytes(ta.Value())
			if err != nil || string(got) != raw {
				t.Fatalf("unchanged textarea round trip: error=%v, exact bytes=%v", err, string(got) == raw)
			}
			ta.SetValue(strings.Replace(ta.Value(), "X-Edit: before", "X-Edit: after", 1))
			got, err = buffer.Bytes(ta.Value())
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(raw, "X-Edit: before", "X-Edit: after", 1)
			if string(got) != want {
				t.Errorf("header edit changed unrelated bytes: got %q, want %q", got, want)
			}
		})
	}
}

func TestMessageEditBufferUsesFirstSeparator(t *testing.T) {
	body := "body begins\r\n\r\nand continues"
	raw := "POST / HTTP/1.1\nHost: h\nX-Edit: before\n\n" + body
	buffer := newMessageEditBuffer([]byte(raw), true)
	ta := newEditor()
	ta.SetValue(strings.Replace(buffer.Text(), "X-Edit: before", "X-Edit: after", 1))
	got, err := buffer.Bytes(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	head, gotBody, ok := strings.Cut(string(got), "\r\n\r\n")
	if !ok || gotBody != body || !strings.Contains(head, "X-Edit: after") {
		t.Fatalf("header/body boundary changed: %q", got)
	}
}

func TestMessageEditBufferExplicitReplacement(t *testing.T) {
	raw := "POST / HTTP/1.1\r\nHost: h\r\n\r\n\x00\xff\r\n"
	buffer := newMessageEditBuffer([]byte(raw), true)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	head, placeholder, _ := strings.Cut(ta.Value(), "\n\n")
	if buffer.Notice() == "" {
		t.Fatal("non-text body must be identified to the operator")
	}
	ta.SetValue(head + "\n\nreplacement without explicit action")
	if _, err := buffer.Bytes(ta.Value()); err == nil {
		t.Fatal("altering a preserved-body display must not silently replace bytes")
	}
	draft, err := buffer.ReplaceBody(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	ta.SetValue(draft)
	got, err := buffer.Bytes(ta.Value())
	if err != nil || string(got) != "POST / HTTP/1.1\r\nHost: h\r\n\r\n" {
		t.Fatalf("explicit empty replacement: got %q, error=%v", got, err)
	}
	// Replacement intent must win even if the user types the old placeholder.
	ta.SetValue(draft + placeholder)
	got, err = buffer.Bytes(ta.Value())
	if err != nil || string(got) != "POST / HTTP/1.1\r\nHost: h\r\n\r\n"+placeholder {
		t.Fatalf("explicit placeholder-text replacement: got %q, error=%v", got, err)
	}
	if buffer.Notice() != "" {
		t.Fatal("replacement must clear the preserved-body notice")
	}
}

func TestMessageEditBufferTextEditAndExplicitLengthRepair(t *testing.T) {
	buffer := newMessageEditBuffer([]byte("POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 1\r\ncontent-length: 2\r\nX-Keep: yes\r\n\r\nold"), true)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	head, _, _ := strings.Cut(ta.Value(), "\n\n")
	body := "雪\nsecond line\n"
	ta.SetValue(head + "\n\n" + body)
	raw, err := buffer.Bytes(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(raw, []byte(body)) || !bytes.Contains(raw, []byte("Content-Length: 1\r\ncontent-length: 2")) {
		t.Fatalf("ordinary send rewrote visible body or intentional lengths: %q", raw)
	}
	fixed, err := buffer.FixLength(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	ta.SetValue(fixed)
	raw, err = buffer.Bytes(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("repaired request is not readable by recipient: %v", err)
	}
	defer req.Body.Close()
	got, err := io.ReadAll(req.Body)
	if err != nil || string(got) != body || req.ContentLength != int64(len(body)) || req.Header.Get("X-Keep") != "yes" {
		t.Fatalf("recipient body=%q length=%d headers=%v error=%v", got, req.ContentLength, req.Header, err)
	}
	if len(req.Header.Values("Content-Length")) != 1 {
		t.Fatalf("repair left duplicate Content-Length: %v", req.Header)
	}
}

func TestMessageEditBufferRepairRetainedBody(t *testing.T) {
	body := "\x00\xfffirst\r\nsecond\r\n"
	buffer := newMessageEditBuffer([]byte("HTTP/1.1 200 OK\r\nX-Keep: yes\r\n\r\n"+body), true)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	fixed, err := buffer.FixLength(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	ta.SetValue(fixed)
	raw, err := buffer.Bytes(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != body || resp.ContentLength != int64(len(body)) {
		t.Fatalf("repaired response body=%q length=%d error=%v", got, resp.ContentLength, err)
	}
}

func TestMessageEditBufferRejectsMissingBoundaryWithoutLosingBytes(t *testing.T) {
	raw := "GET / HTTP/1.1\r\nHost: h\r\n"
	buffer := newMessageEditBuffer([]byte(raw), true)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	if _, err := buffer.FixLength(ta.Value()); err == nil {
		t.Fatal("cannot repair length without a body boundary")
	}
	got, err := buffer.Bytes(ta.Value())
	if err != nil || string(got) != raw {
		t.Fatalf("failed repair changed original bytes: %q, %v", got, err)
	}
	buffer = newMessageEditBuffer([]byte(raw+"\r\nbody"), true)
	ta.SetValue(strings.Replace(buffer.Text(), "\n\n", "\n", 1))
	if _, err := buffer.Bytes(ta.Value()); err == nil {
		t.Fatal("deleting separator must not silently discard the body")
	}
}

func TestMessageEditBufferBodyOnly(t *testing.T) {
	buffer := newMessageEditBuffer([]byte("\x00\xff\r\n"), false)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	got, err := buffer.Bytes(ta.Value())
	if err != nil || string(got) != "\x00\xff\r\n" {
		t.Fatalf("non-HTTP original changed: %q, %v", got, err)
	}
	if _, err := buffer.FixLength(ta.Value()); err == nil {
		t.Fatal("non-HTTP body must not receive a Content-Length header")
	}
	draft, err := buffer.ReplaceBody(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	ta.SetValue(draft + "GET / is payload\n\n雪")
	got, err = buffer.Bytes(ta.Value())
	if err != nil || string(got) != ta.Value() {
		t.Fatalf("non-HTTP text was parsed or rewritten: %q, %v", got, err)
	}
}

func TestMessageEditBufferInterceptedRequestReachesEndpoint(t *testing.T) {
	body := "one\r\ntwo\r\n"
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		received <- fmt.Sprintf("%s %s %s %s %v", r.Method, r.RequestURI, r.Header.Get("X-Edit"), got, err)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	raw := fmt.Sprintf("POST /edited?q=yes HTTP/1.1\r\nHost: %s\r\nX-Edit: before\r\nContent-Length: %d\r\n\r\n%s", addr, len(body), body)
	buffer := newMessageEditBuffer([]byte(raw), true)
	ta := newEditor()
	ta.SetValue(buffer.Text())
	ta.SetValue(strings.Replace(ta.Value(), "X-Edit: before", "X-Edit: after", 1))
	wire, err := buffer.Bytes(ta.Value())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got, want := <-received, "POST /edited?q=yes after "+body+" <nil>"; got != want {
		t.Fatalf("endpoint received %q, want %q", got, want)
	}
}

func TestRepeaterTextareaRetainsLogicalBodiesAtEndpoint(t *testing.T) {
	logical := []byte("logical\r\nbody\x00\xff")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(logical); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	grpcPayload := []byte("protobuf\x00\r\n\xff")
	grpcWire := make([]byte, 5+len(grpcPayload))
	binary.BigEndian.PutUint32(grpcWire[1:5], uint32(len(grpcPayload)))
	copy(grpcWire[5:], grpcPayload)
	cases := []struct {
		name     string
		method   string
		proto    capture.Protocol
		headers  http.Header
		body     []byte
		wire     []byte
		messages []*capture.Message
		want     []byte
	}{
		{name: "empty GET", method: "GET", proto: capture.ProtoHTTP1},
		{name: "gzip logical body", method: "POST", proto: capture.ProtoHTTP1, headers: http.Header{"Content-Encoding": {"gzip"}}, body: logical, wire: compressed.Bytes(), want: logical},
		{name: "gRPC framing", method: "POST", proto: capture.ProtoGRPC, headers: http.Header{"Content-Type": {"application/grpc"}}, messages: []*capture.Message{{Direction: capture.ClientToServer, Body: grpcPayload}}, want: grpcWire},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type receipt struct {
				method, target, header string
				body                   []byte
				err                    error
			}
			received := make(chan receipt, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reader := io.Reader(r.Body)
				var err error
				if r.Header.Get("Content-Encoding") == "gzip" {
					var decoded *gzip.Reader
					decoded, err = gzip.NewReader(r.Body)
					if err == nil {
						defer decoded.Close()
						reader = decoded
					}
				}
				var got []byte
				if err == nil {
					got, err = io.ReadAll(reader)
				}
				received <- receipt{r.Method, r.RequestURI, r.Header.Get("X-Edit"), got, err}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			headers := tc.headers.Clone()
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Edit", "before")
			headers.Set("Content-Length", strconv.Itoa(len(tc.wire)))
			flow := capture.NewFlow("editor-test", addr)
			flow.Protocol = tc.proto
			flow.Request = &capture.Message{Headers: headers, Body: tc.body, Meta: map[string]string{"method": tc.method, "path": "/original?q=yes", protocol.BodyRepresentationMeta: protocol.BodyRepresentationDecoded}}
			flow.Messages = tc.messages
			if tc.wire != nil {
				flow.Request.Raw = append([]byte(fmt.Sprintf("POST /original?q=yes HTTP/1.1\r\nHost: %s\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", addr, len(tc.wire))), tc.wire...)
			}
			base, err := repeater.FromFlow(flow)
			if err != nil {
				t.Fatal(err)
			}
			buffer := newMessageEditBuffer([]byte(base.Raw()), true)
			ta := newEditor()
			ta.SetValue(buffer.Text())
			if tc.name != "empty GET" {
				ta.SetValue(strings.Replace(ta.Value(), "X-Edit: before", "X-Edit: after", 1))
			}
			raw, err := buffer.Bytes(ta.Value())
			if err != nil {
				t.Fatal(err)
			}
			edited, err := repeater.ParseRaw(string(raw), base)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := repeater.Send(ctx, edited, nil); err != nil {
				t.Fatal(err)
			}
			got := <-received
			wantHeader := "after"
			if tc.name == "empty GET" {
				wantHeader = "before"
			}
			if got.err != nil || got.method != tc.method || got.target != "/original?q=yes" || got.header != wantHeader || !bytes.Equal(got.body, tc.want) {
				t.Fatalf("endpoint receipt: %+v; want method=%s header=%s body=%x", got, tc.method, wantHeader, tc.want)
			}
		})
	}
}
