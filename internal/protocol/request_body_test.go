package protocol

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestCapturedGRPCBodyRejectsTrailingFrameBytes(t *testing.T) {
	payload := []byte("valid")
	wire := append(frameGRPC(payload), 0, 0, 0)
	f := capture.NewFlow("c", "svc:443")
	f.Protocol = capture.ProtoGRPC
	f.Request = &capture.Message{
		Headers: http.Header{"Content-Type": {"application/grpc"}},
		Body:    wire,
		Raw:     append([]byte(nil), wire...),
		Meta: map[string]string{
			"method":               "POST",
			"path":                 "/svc/call",
			BodyRepresentationMeta: BodyRepresentationDecoded,
		},
	}
	f.Messages = []*capture.Message{{
		Direction: capture.ClientToServer,
		Body:      payload,
		Raw:       append([]byte(nil), payload...),
		Meta:      map[string]string{},
	}}

	if _, err := CapturedRequestBody(f); err == nil {
		t.Fatal("expected trailing partial gRPC frame to fail")
	}
}

func TestCapturedGRPCBodyDecodesOuterContentEncoding(t *testing.T) {
	framed := frameGRPC([]byte("payload"))
	wire, err := capture.EncodeContentEncoding(framed, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	f := capture.NewFlow("c", "svc:443")
	f.Protocol = capture.ProtoGRPC
	f.Request = &capture.Message{
		Headers: http.Header{
			"Content-Type":     {"application/grpc"},
			"Content-Encoding": {"gzip"},
		},
		Body: wire,
		Raw:  append([]byte(nil), wire...),
		Meta: map[string]string{BodyRepresentationMeta: BodyRepresentationDecoded},
	}

	body, err := CapturedRequestBody(f)
	if err != nil {
		t.Fatalf("CapturedRequestBody: %v", err)
	}
	if len(body.Messages) != 1 || string(body.Messages[0].Data) != "payload" {
		t.Fatalf("captured messages = %+v", body.Messages)
	}
	reencoded, err := EncodeRequestBody(capture.ProtoGRPC, f.Request.Headers, body)
	if err != nil {
		t.Fatalf("EncodeRequestBody: %v", err)
	}
	decoded, err := capture.DecodeContentEncodingStrict(reencoded, "gzip")
	if err != nil || !bytes.Equal(decoded, framed) {
		t.Errorf("re-encoded body decoded to %x, %v; want %x", decoded, err, framed)
	}
}

func TestCaptureForwardedHTTP1StoresEditedRequest(t *testing.T) {
	raw := []byte("PUT /edited HTTP/1.1\r\nHost: svc\r\nContent-Length: 6\r\nX-Edit: yes\r\n\r\nedited")
	message := &capture.Message{Meta: map[string]string{}}
	req, err := captureForwardedHTTP1(message, raw)
	if err != nil {
		t.Fatalf("captureForwardedHTTP1: %v", err)
	}
	if req.Method != "PUT" || message.Meta["path"] != "/edited" {
		t.Errorf("edited method/path = %s %s", req.Method, message.Meta["path"])
	}
	if string(message.Body) != "edited" || http.Header(message.Headers).Get("X-Edit") != "yes" {
		t.Errorf("edited capture = %+v", message)
	}

	f := capture.NewFlow("c", "svc:80")
	f.Protocol = capture.ProtoHTTP1
	f.Request = message
	body, err := CapturedRequestBody(f)
	if err != nil {
		t.Fatalf("CapturedRequestBody: %v", err)
	}
	if string(body.Body) != "edited" {
		t.Errorf("reconstructed edited body = %q", body.Body)
	}

	trailing := capture.NewFlow("c", "svc:80")
	trailing.Protocol = capture.ProtoHTTP1
	trailing.Request = &capture.Message{
		Raw: append(append([]byte(nil), raw...), []byte("trailing")...),
		Meta: map[string]string{
			BodyRepresentationMeta: BodyRepresentationUnavailable,
		},
	}
	if _, err := CapturedRequestBody(trailing); err == nil {
		t.Fatal("expected unavailable edited request with trailing bytes to fail")
	}
}

func TestCapturedHTTP2BodyAvailability(t *testing.T) {
	headerDump := []byte("POST /x HTTP/2\r\nContent-Type: application/octet-stream\r\n")
	t.Run("legacy header-only capture", func(t *testing.T) {
		f := capture.NewFlow("c", "svc:443")
		f.Protocol = capture.ProtoHTTP2
		f.Request = &capture.Message{Raw: headerDump, Meta: map[string]string{"method": "POST", "path": "/x"}}
		if _, err := CapturedRequestBody(f); err == nil {
			t.Fatal("expected unavailable legacy body to fail")
		}
	})

	t.Run("known empty capture", func(t *testing.T) {
		f := capture.NewFlow("c", "svc:443")
		f.Protocol = capture.ProtoHTTP2
		f.Request = &capture.Message{
			Raw: headerDump,
			Meta: map[string]string{
				"method":               "POST",
				"path":                 "/x",
				BodyRepresentationMeta: BodyRepresentationDecoded,
			},
		}
		body, err := CapturedRequestBody(f)
		if err != nil {
			t.Fatalf("CapturedRequestBody: %v", err)
		}
		if len(body.Body) != 0 {
			t.Errorf("known empty body = %x", body.Body)
		}
	})
}

func TestCapturedHTTPBodiesRejectContradictoryWireMetadata(t *testing.T) {
	logical := []byte("logical")
	gzipWire, err := capture.EncodeContentEncoding(logical, "gzip")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("HTTP/1 header mismatch", func(t *testing.T) {
		raw := append(
			[]byte(fmt.Sprintf("POST / HTTP/1.1\r\nHost: svc\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", len(gzipWire))),
			gzipWire...,
		)
		f := capture.NewFlow("c", "svc:80")
		f.Protocol = capture.ProtoHTTP1
		f.Request = &capture.Message{
			Headers: http.Header{"Content-Encoding": {"br"}},
			Body:    logical,
			Raw:     raw,
		}
		if _, err := CapturedRequestBody(f); err == nil {
			t.Fatal("expected contradictory Content-Encoding to fail")
		}
	})

	t.Run("HTTP/2 logical mismatch", func(t *testing.T) {
		f := capture.NewFlow("c", "svc:443")
		f.Protocol = capture.ProtoHTTP2
		f.Request = &capture.Message{
			Headers: http.Header{"Content-Encoding": {"gzip"}},
			Body:    []byte("different"),
			Raw:     gzipWire,
			Meta:    map[string]string{BodyRepresentationMeta: BodyRepresentationDecoded},
		}
		if _, err := CapturedRequestBody(f); err == nil {
			t.Fatal("expected contradictory HTTP/2 body to fail")
		}
	})

	if bytes.Equal(gzipWire, logical) {
		t.Fatal("test fixture was not encoded")
	}
}
