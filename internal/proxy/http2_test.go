package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/scope"
)

func grpcFrame(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// stubRT stands in for the upstream HTTP/2 transport. It drains the request body
// (so request-side gRPC parsing runs) and returns a canned gRPC response with a
// status trailer.
type stubRT struct{ respBody []byte }

func (s stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/grpc"}},
		Body:       io.NopCloser(bytes.NewReader(s.respBody)),
		Trailer:    http.Header{"Grpc-Status": {"0"}},
	}, nil
}

// TestH2CaptureParsesGRPCBothWays drives the capturing RoundTripper directly
// (no sockets) and asserts the flow records the request and response gRPC
// messages plus the trailer status.
func TestH2CaptureParsesGRPCBothWays(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine() // interception off
	p := &Proxy{store: store, tamper: engine}

	cap := &h2Capture{
		base:   stubRT{respBody: grpcFrame([]byte("pong"))},
		proxy:  p,
		target: "svc.example:443",
		sni:    "svc.example",
	}

	reqBody := io.NopCloser(bytes.NewReader(grpcFrame([]byte("ping"))))
	req, _ := http.NewRequest("POST", "https://svc.example/pkg.Svc/Call", reqBody)
	req.Header.Set("Content-Type", "application/grpc")

	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	// ReverseProxy would read the body; emulate that so response parsing + the
	// trailer onEOF hook fire.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	flows := store.List()
	if len(flows) != 1 {
		t.Fatalf("want 1 flow, got %d", len(flows))
	}
	f := flows[0]
	if f.Protocol != capture.ProtoGRPC {
		t.Errorf("protocol = %s, want grpc", f.Protocol)
	}

	var reqMsgs, respMsgs int
	for _, m := range f.Messages {
		switch m.Direction {
		case capture.ClientToServer:
			reqMsgs++
			if string(m.Body) != "ping" {
				t.Errorf("request gRPC message = %q, want ping", m.Body)
			}
		case capture.ServerToClient:
			respMsgs++
			if string(m.Body) != "pong" {
				t.Errorf("response gRPC message = %q, want pong", m.Body)
			}
		}
	}
	if reqMsgs != 1 || respMsgs != 1 {
		t.Fatalf("want 1 req + 1 resp gRPC message, got %d + %d", reqMsgs, respMsgs)
	}
	if f.Response == nil || f.Response.Meta["grpc-status"] != "0" {
		t.Errorf("grpc-status trailer not captured: %+v", f.Response)
	}
}

// recordRT records the request body the upstream actually received.
type recordRT struct {
	gotBody  []byte
	respBody []byte
}

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		r.gotBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(r.respBody)),
		Trailer:    http.Header{},
	}, nil
}

// TestH2RequestBodyEdit proves that when an h2 flow is intercepted, the edited
// body — not the original — reaches the upstream.
func TestH2RequestBodyEdit(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, _ := scope.Build(nil, nil, true) // intercept everything
	engine.SetScope(set)
	engine.SetEnabled(true)
	engine.OnPause(func(f *capture.Flow, _ *capture.Message) {
		engine.Resolve(f.ID, intercept.Resolution{Decision: intercept.Forward, EditedBody: []byte("EDITED")})
	})

	base := &recordRT{respBody: []byte("ok")}
	p := &Proxy{store: store, tamper: engine}
	cap := &h2Capture{base: base, proxy: p, target: "svc:443", sni: "svc"}

	req, _ := http.NewRequest("POST", "https://svc/x", bytes.NewReader([]byte("original")))
	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if string(base.gotBody) != "EDITED" {
		t.Errorf("upstream received %q, want edited %q", base.gotBody, "EDITED")
	}
}

// TestH2GRPCStreamTamper proves per-message tampering: two streamed gRPC
// messages are each edited in flight (never buffered) and reach the upstream
// re-framed.
func TestH2GRPCStreamTamper(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, _ := scope.Build(nil, nil, true)
	engine.SetScope(set)
	engine.SetEnabled(true)
	engine.OnPause(func(f *capture.Flow, m *capture.Message) {
		engine.Resolve(f.ID, intercept.Resolution{Decision: intercept.Forward, EditedBody: bytes.ToUpper(m.Raw)})
	})

	base := &recordRT{}
	p := &Proxy{store: store, tamper: engine}
	cap := &h2Capture{base: base, proxy: p, target: "svc:443", sni: "svc"}

	reqBody := append(grpcFrame([]byte("ping")), grpcFrame([]byte("pong"))...)
	req, _ := http.NewRequest("POST", "https://svc/x", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/grpc")

	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Decode the frames the upstream actually received.
	var got []string
	off := 0
	for off+5 <= len(base.gotBody) {
		n := int(binary.BigEndian.Uint32(base.gotBody[off+1 : off+5]))
		got = append(got, string(base.gotBody[off+5:off+5+n]))
		off += 5 + n
	}
	if len(got) != 2 || got[0] != "PING" || got[1] != "PONG" {
		t.Errorf("upstream received %v, want [PING PONG]", got)
	}
}
