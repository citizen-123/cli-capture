package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/scope"
)

func grpcFrame(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func TestIsGRPCContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "base", contentType: "application/grpc", want: true},
		{name: "mixed case", contentType: "Application/GRPC", want: true},
		{name: "proto suffix", contentType: "application/grpc+proto", want: true},
		{name: "JSON suffix mixed case", contentType: "APPLICATION/GRPC+JSON", want: true},
		{name: "parameters and whitespace", contentType: " Application/GRPC+Proto ; Charset = utf-8 ; Version = 1 ", want: true},
		{name: "base with quoted parameter", contentType: `application/grpc; charset="utf-8"`, want: true},
		{name: "prefix collision", contentType: "application/grpcfoo", want: false},
		{name: "empty suffix", contentType: "application/grpc+", want: false},
		{name: "unrelated", contentType: "application/json", want: false},
		{name: "missing parameter value", contentType: "application/grpc; charset", want: false},
		{name: "invalid media type", contentType: "application/grpc / proto", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGRPCContentType(tt.contentType); got != tt.want {
				t.Errorf("isGRPCContentType(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
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

func TestH2CaptureGRPCContentTypeClassification(t *testing.T) {
	tests := []struct {
		name         string
		contentType  string
		wantProtocol capture.Protocol
		wantMessages int
	}{
		{
			name:         "case variant with parameters uses gRPC framing",
			contentType:  "Application/GRPC+Proto ; Charset = utf-8",
			wantProtocol: capture.ProtoGRPC,
			wantMessages: 2,
		},
		{
			name:         "prefix collision remains HTTP2",
			contentType:  "application/grpcfoo",
			wantProtocol: capture.ProtoHTTP2,
			wantMessages: 0,
		},
		{
			name:         "malformed media type remains HTTP2",
			contentType:  "application/grpc; charset",
			wantProtocol: capture.ProtoHTTP2,
			wantMessages: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := capture.NewStore()
			p := &Proxy{store: store, tamper: intercept.NewEngine()}
			cap := &h2Capture{
				base:   stubRT{respBody: grpcFrame([]byte("pong"))},
				proxy:  p,
				target: "svc.example:443",
				sni:    "svc.example",
			}

			req, err := http.NewRequest(
				"POST",
				"https://svc.example/pkg.Svc/Call",
				bytes.NewReader(grpcFrame([]byte("ping"))),
			)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tt.contentType)

			resp, err := cap.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatalf("read response: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("close response: %v", err)
			}

			flows := store.List()
			if len(flows) != 1 {
				t.Fatalf("want 1 flow, got %d", len(flows))
			}
			if got := flows[0].Protocol; got != tt.wantProtocol {
				t.Errorf("protocol = %s, want %s", got, tt.wantProtocol)
			}
			if got := len(flows[0].Messages); got != tt.wantMessages {
				t.Errorf("message count = %d, want %d", got, tt.wantMessages)
			}
		})
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
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, _ *capture.Message) {
		engine.Resolve(token, intercept.Resolution{Decision: intercept.Forward, EditedBody: []byte("EDITED")})
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
	flows := store.List()
	if len(flows) != 1 || string(flows[0].Request.Body) != "EDITED" ||
		string(flows[0].Request.Raw) != "EDITED" {
		t.Fatalf("captured forwarded request = %+v", flows)
	}
	if got := flows[0].Request.Meta[protocol.BodyRepresentationMeta]; got != protocol.BodyRepresentationDecoded {
		t.Errorf("body representation = %q", got)
	}
}

func TestH2CaptureRetainsNonInterceptedRequestBody(t *testing.T) {
	store := capture.NewStore()
	base := &recordRT{}
	p := &Proxy{store: store, tamper: intercept.NewEngine()}
	cap := &h2Capture{base: base, proxy: p, target: "svc:443", sni: "svc"}

	logical := []byte("captured")
	wire, err := capture.EncodeContentEncoding(logical, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "https://svc/x", bytes.NewReader(wire))
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	flows := store.List()
	if len(flows) != 1 {
		t.Fatalf("want 1 flow, got %d", len(flows))
	}
	if got := flows[0].Request.Body; !bytes.Equal(got, logical) {
		t.Errorf("captured request body = %q", got)
	}
	if got := flows[0].Request.Raw; !bytes.Equal(got, wire) {
		t.Errorf("captured request wire body = %x, want %x", got, wire)
	}
	if !bytes.Equal(base.gotBody, wire) {
		t.Errorf("upstream request wire body = %x, want %x", base.gotBody, wire)
	}
	if got := flows[0].Request.Meta[protocol.BodyRepresentationMeta]; got != protocol.BodyRepresentationDecoded {
		t.Errorf("body representation = %q", got)
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
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, m *capture.Message) {
		engine.Resolve(token, intercept.Resolution{Decision: intercept.Forward, EditedBody: bytes.ToUpper(m.Raw)})
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
	flows := store.List()
	if len(flows) != 1 {
		t.Fatalf("want 1 flow, got %d", len(flows))
	}
	var captured []string
	for _, message := range flows[0].Messages {
		if message.Direction == capture.ClientToServer {
			captured = append(captured, string(message.Body))
		}
	}
	if len(captured) != 2 || captured[0] != "PING" || captured[1] != "PONG" {
		t.Errorf("captured forwarded messages = %v, want [PING PONG]", captured)
	}
}

func TestCONNECTAuthorityHostPolicyUsesCanonicalDNSCase(t *testing.T) {
	policy, err := scope.Build([]string{
		"*.bank.example",
		"=192.0.2.10",
		"=2001:db8::a",
	}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{mitm: policy}

	tests := []struct {
		name      string
		authority string
		wantHost  string
	}{
		{
			name:      "DNS name and standard port",
			authority: "LOGIN.BANK.EXAMPLE:443",
			wantHost:  "LOGIN.BANK.EXAMPLE",
		},
		{
			name:      "DNS name and alternate port",
			authority: "LOGIN.BANK.EXAMPLE:8443",
			wantHost:  "LOGIN.BANK.EXAMPLE",
		},
		{
			name:      "IPv4 literal",
			authority: "192.0.2.10:443",
			wantHost:  "192.0.2.10",
		},
		{
			name:      "IPv6 literal",
			authority: "[2001:DB8::A]:443",
			wantHost:  "2001:DB8::A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := hostOnly(tt.authority)
			if host != tt.wantHost {
				t.Fatalf("hostOnly(%q) = %q, want %q", tt.authority, host, tt.wantHost)
			}
			if !p.shouldMITM(host) {
				t.Errorf("CONNECT authority %q should match the MITM host policy", tt.authority)
			}
		})
	}
}

type h2FaultBody struct {
	data     []byte
	readErr  error
	closeErr error
	read     bool
	closed   bool
}

func (b *h2FaultBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	n := copy(p, b.data)
	if b.readErr != nil {
		return n, b.readErr
	}
	return n, io.EOF
}

func (b *h2FaultBody) Close() error {
	b.closed = true
	return b.closeErr
}

type h2RoundTripFunc func(*http.Request) (*http.Response, error)

func (f h2RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestH2InterceptRejectsPartialRequestRead(t *testing.T) {
	readErr := errors.New("request stream reset")
	body := &h2FaultBody{data: []byte("partial"), readErr: readErr}
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetScope(set)
	engine.SetEnabled(true)
	cap := &h2Capture{
		base: h2RoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("upstream received a partial intercepted request")
			return nil, nil
		}),
		proxy:  &Proxy{store: store, tamper: engine},
		target: "svc:443",
		sni:    "svc",
	}
	req, _ := http.NewRequest(http.MethodPost, "https://svc/x", nil)
	req.Body = body

	if _, err := cap.RoundTrip(req); !errors.Is(err, readErr) {
		t.Fatalf("RoundTrip error = %v, want %v", err, readErr)
	}
	if !body.closed {
		t.Error("failed request body was not closed")
	}
	flow := store.List()[0]
	if flow.Status != capture.StatusError || flow.Response != nil {
		t.Fatalf("partial request flow = %+v, want terminal request error without response", flow)
	}
}

func TestH2InterceptRejectsResponseReadAndCloseErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body *h2FaultBody
	}{
		{name: "read error", body: &h2FaultBody{data: []byte("partial"), readErr: errors.New("response reset")}},
		{name: "close error", body: &h2FaultBody{data: []byte("complete"), closeErr: errors.New("response close failed")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := capture.NewStore()
			engine := intercept.NewEngine()
			set, err := scope.Build(nil, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			engine.SetScope(set)
			engine.SetInterceptResponses(true)
			cap := &h2Capture{
				base: h2RoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						Status:     "200 OK",
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       tt.body,
					}, nil
				}),
				proxy:  &Proxy{store: store, tamper: engine},
				target: "svc:443",
				sni:    "svc",
			}
			req, _ := http.NewRequest(http.MethodGet, "https://svc/x", nil)

			if _, err := cap.RoundTrip(req); err == nil {
				t.Fatal("partial response was treated as a complete response")
			}
			if !tt.body.closed {
				t.Error("failed response body was not closed")
			}
			flow := store.List()[0]
			if flow.Status != capture.StatusError || flow.Response == nil || flow.Response.Body != nil {
				t.Fatalf("partial response flow = %+v, want response metadata plus terminal error", flow)
			}
		})
	}
}

func TestH2OversizedInterceptedRequestStreamsWithoutPartialEdit(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetScope(set)
	engine.SetEnabled(true)
	engine.OnPause(func(intercept.PauseToken, *capture.Flow, *capture.Message) {
		t.Fatal("oversized request should not be offered to the editor")
	})
	base := &recordRT{respBody: []byte("ok")}
	cap := &h2Capture{base: base, proxy: &Proxy{store: store, tamper: engine}, target: "svc:443", sni: "svc"}
	body := bytes.Repeat([]byte{'x'}, capture.MaxRetainedWireBodyBytes+1)
	req, _ := http.NewRequest(http.MethodPost, "https://svc/x", bytes.NewReader(body))

	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base.gotBody, body) {
		t.Fatalf("upstream body was changed or truncated: got %d bytes, want %d", len(base.gotBody), len(body))
	}
	flow := store.List()[0]
	if !flow.Request.Truncated || flow.Request.Meta[protocol.BodyRepresentationMeta] != protocol.BodyRepresentationUnavailable {
		t.Fatalf("oversized request capture = %+v, want truncated unavailable capture", flow.Request)
	}
}

func TestH2FixedLengthRequestFinalizesOnCloseAfterExactRead(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	const body = "fixed"
	cap := &h2Capture{
		base: h2RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			got := make([]byte, len(body))
			if _, err := io.ReadFull(req.Body, got); err != nil {
				return nil, err
			}
			if err := req.Body.Close(); err != nil {
				return nil, err
			}
			return &http.Response{
				Status:        "200 OK",
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("")),
				ContentLength: 0,
			}, nil
		}),
		proxy:  &Proxy{store: store, tamper: engine},
		target: "svc:443",
		sni:    "svc",
	}
	req, _ := http.NewRequest(http.MethodPost, "https://svc/x", strings.NewReader(body))
	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	flow := store.List()[0]
	if flow.Status != capture.StatusComplete || flow.Request == nil || string(flow.Request.Raw) != body {
		t.Fatalf("fixed-length request did not finalize on Close: %+v", flow)
	}
}

func TestH2ServerSetsHeaderAndStreamBounds(t *testing.T) {
	server := newH2Server()
	if server.MaxConcurrentStreams != uint32(maxConcurrentConnections) {
		t.Errorf("MaxConcurrentStreams = %d, want %d", server.MaxConcurrentStreams, maxConcurrentConnections)
	}
	opts := newH2ServeConnOpts(http.NotFoundHandler())
	if opts.BaseConfig == nil || opts.BaseConfig.MaxHeaderBytes != capture.MaxRetainedHeaderBytes {
		t.Errorf("MaxHeaderBytes = %#v, want %d", opts.BaseConfig, capture.MaxRetainedHeaderBytes)
	}
}

func TestH2InterceptsBodylessRequest(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetScope(set)
	engine.SetEnabled(true)
	paused := false
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, msg *capture.Message) {
		paused = true
		if len(msg.Raw) != 0 {
			t.Errorf("bodyless request editor seed = %q, want empty body", msg.Raw)
		}
		engine.Resolve(token, intercept.Resolution{Decision: intercept.Drop})
	})
	base := h2RoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("dropped bodyless request reached upstream")
		return nil, nil
	})
	cap := &h2Capture{base: base, proxy: &Proxy{store: store, tamper: engine}, target: "svc:443", sni: "svc"}
	req, err := http.NewRequest(http.MethodGet, "https://svc.example/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cap.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("RoundTrip error = %v, want dropped request", err)
	}
	if !paused {
		t.Fatal("bodyless request was not offered to interception")
	}
}

func TestH2CanceledRequestContextReleasesPendingIntercept(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetScope(set)
	engine.SetEnabled(true)
	paused := make(chan intercept.PauseToken, 1)
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, _ *capture.Message) {
		paused <- token
	})
	base := h2RoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled request reached upstream")
		return nil, nil
	})
	cap := &h2Capture{base: base, proxy: &Proxy{store: store, tamper: engine}, target: "svc:443", sni: "svc"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://svc.example/upload", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cap.RoundTrip(req)
		done <- err
	}()

	var token intercept.PauseToken
	select {
	case token = <-paused:
	case <-time.After(time.Second):
		t.Fatal("request did not become pending")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "dropped") {
			t.Fatalf("RoundTrip error = %v, want canceled drop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request left an intercept wait pending")
	}
	if engine.Resolve(token, intercept.Resolution{Decision: intercept.Forward}) {
		t.Fatal("canceled pause still accepted a resolution")
	}
}

func TestH2GRPCInterceptClearsResponseContentLength(t *testing.T) {
	store := capture.NewStore()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetScope(set)
	engine.SetInterceptResponses(true)
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, msg *capture.Message) {
		if msg.Direction != capture.ServerToClient {
			t.Fatalf("intercepted direction = %s, want server-to-client", msg.Direction)
		}
		engine.Resolve(token, intercept.Resolution{
			Decision:   intercept.Forward,
			EditedBody: []byte("a longer gRPC response"),
		})
	})
	original := grpcFrame([]byte("short"))
	base := h2RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": {"application/grpc"}, "Content-Length": {"10"}},
			Body:          io.NopCloser(bytes.NewReader(original)),
			ContentLength: int64(len(original)),
		}, nil
	})
	cap := &h2Capture{base: base, proxy: &Proxy{store: store, tamper: engine}, target: "svc:443", sni: "svc"}
	req, err := http.NewRequest(http.MethodPost, "https://svc.example/Call", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := cap.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d, want -1 for transformed gRPC response", resp.ContentLength)
	}
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length header = %q, want removed", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read transformed response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close transformed response: %v", err)
	}
	want := grpcFrame([]byte("a longer gRPC response"))
	if !bytes.Equal(got, want) {
		t.Errorf("transformed response = %q, want %q", got, want)
	}
}
