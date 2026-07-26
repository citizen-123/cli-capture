package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

// serveH2 handles an ALPN-negotiated HTTP/2 client connection. It runs Go's
// http2.Server toward the client and reverse-proxies each stream to the real
// upstream over HTTP/2, capturing (and, at the request-header boundary,
// intercepting) every exchange. gRPC bodies are parsed into per-message records
// as they stream. FlushInterval:-1 makes the proxy copy frames through
// immediately, which is required for server/bidi-streaming gRPC.
func (p *Proxy) serveH2(client net.Conn, target, sni string) {
	upstream := &http2.Transport{TLSClientConfig: p.upstreamConfig(sni)}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = target
		},
		Transport:     &h2Capture{base: upstream, proxy: p, target: target, sni: sni},
		FlushInterval: -1,
		ErrorHandler:  func(http.ResponseWriter, *http.Request, error) {},
	}
	(&http2.Server{}).ServeConn(client, &http2.ServeConnOpts{Handler: rp})
}

// h2Capture is the RoundTripper the ReverseProxy calls per stream. It records
// the flow, runs the request-side intercept hook, splices gRPC message parsers
// into the request/response bodies, and reads gRPC status from the trailers.
type h2Capture struct {
	base   http.RoundTripper
	proxy  *Proxy
	target string
	sni    string
}

func (c *h2Capture) RoundTrip(req *http.Request) (*http.Response, error) {
	isGRPC := strings.HasPrefix(req.Header.Get("Content-Type"), "application/grpc")

	flow := capture.NewFlow(clientAddr(req, c.target), c.target)
	flow.SNI = c.sni
	flow.Secure = true
	if isGRPC {
		flow.Protocol = capture.ProtoGRPC
	} else {
		flow.Protocol = capture.ProtoHTTP2
	}
	reqMsg := &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s HTTP/2", req.Method, req.URL.RequestURI()),
		Headers:   req.Header,
		Meta:      map[string]string{"method": req.Method, "path": req.URL.RequestURI()},
	}
	flow.Request = reqMsg
	c.proxy.store.Add(flow)
	touch := func() { c.proxy.store.Touch(flow) }

	// --- request side ---
	switch {
	case c.proxy.tamper.ShouldInterceptRequest(flow) && isGRPC && req.Body != nil:
		// Streaming gRPC being intercepted: tamper each message in flight, so a
		// long-lived/bidi stream is never buffered (which would deadlock it).
		reqMsg.Raw = h2HeaderDump(req)
		req.Body = &bodyWrap{r: protocol.NewGRPCTamperReader(req.Body,
			c.grpcTransform(flow, capture.ClientToServer, touch, c.proxy.tamper.BeforeForward)), c: req.Body}

	case c.proxy.tamper.ShouldInterceptRequest(flow) && req.Body != nil:
		// Unary/non-gRPC being intercepted: buffer→pause→edit→forward the body.
		body, _ := io.ReadAll(req.Body)
		req.Body.Close()
		reqMsg.Body = body
		reqMsg.Raw = body
		out, drop := c.proxy.tamper.BeforeForward(flow, reqMsg)
		if drop {
			flow.Status = capture.StatusComplete
			touch()
			return nil, fmt.Errorf("cli-capture: request dropped by user")
		}
		if out == nil {
			out = body
		}
		setBody(req.Header, &req.ContentLength, len(out))
		req.Body = io.NopCloser(bytes.NewReader(out))

	default:
		// Not intercepted: observe only, streaming untouched.
		reqMsg.Raw = h2HeaderDump(req)
		if req.Body != nil && isGRPC {
			req.Body = &bodyWrap{r: protocol.NewGRPCFramer(req.Body, func(m protocol.GRPCMessage) {
				flow.AddMessage(grpcMsg(capture.ClientToServer, m))
				touch()
			}), c: req.Body}
		}
		if _, drop := c.proxy.tamper.BeforeForward(flow, reqMsg); drop {
			flow.Status = capture.StatusComplete
			touch()
			return nil, fmt.Errorf("cli-capture: request dropped by user")
		}
	}

	resp, err := c.base.RoundTrip(req)
	if err != nil {
		flow.Status = capture.StatusError
		flow.Err = err
		touch()
		return nil, err
	}

	respMsg := &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("HTTP/2 %d", resp.StatusCode),
		Headers:   resp.Header,
		Meta:      map[string]string{"status": resp.Status},
	}
	flow.Response = respMsg
	touch()

	// --- response side ---
	switch {
	case c.proxy.tamper.ShouldInterceptResponse(flow) && isGRPC:
		// Streaming gRPC being intercepted: tamper each message in flight.
		resp.Body = &bodyWrap{
			r: protocol.NewGRPCTamperReader(resp.Body,
				c.grpcTransform(flow, capture.ServerToClient, touch, c.proxy.tamper.BeforeDeliver)),
			c: resp.Body,
			onEOF: func() {
				readGRPCStatus(resp, respMsg)
				flow.Status = capture.StatusComplete
				touch()
			},
		}
		return resp, nil

	case c.proxy.tamper.ShouldInterceptResponse(flow):
		// Unary/non-gRPC being intercepted: buffer→pause→edit→deliver.
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		readGRPCStatus(resp, respMsg) // trailers are populated once the body is fully read
		respMsg.Body = capture.DecodeContentEncoding(body, resp.Header.Get("Content-Encoding"))
		respMsg.Raw = body
		out, drop := c.proxy.tamper.BeforeDeliver(flow, respMsg)
		if drop {
			flow.Status = capture.StatusComplete
			touch()
			return nil, fmt.Errorf("cli-capture: response dropped by user")
		}
		if out == nil {
			out = body
		}
		setBody(resp.Header, &resp.ContentLength, len(out))
		resp.Body = io.NopCloser(bytes.NewReader(out))
		flow.Status = capture.StatusComplete
		touch()
		return resp, nil
	}

	// Not intercepted: observe streaming, read gRPC status from trailers at EOF.
	onEnd := func() {
		readGRPCStatus(resp, respMsg)
		flow.Status = capture.StatusComplete
		touch()
	}
	if isGRPC {
		resp.Body = &bodyWrap{
			r: protocol.NewGRPCFramer(resp.Body, func(m protocol.GRPCMessage) {
				flow.AddMessage(grpcMsg(capture.ServerToClient, m))
				touch()
			}),
			c:     resp.Body,
			onEOF: onEnd,
		}
	} else {
		resp.Body = &bodyWrap{r: resp.Body, c: resp.Body, onEOF: onEnd}
	}
	return resp, nil
}

// grpcTransform builds a per-message tamper callback: it records each streamed
// gRPC message and runs it through the given intercept hook (BeforeForward or
// BeforeDeliver), returning the (possibly edited) bytes for re-framing.
func (c *h2Capture) grpcTransform(flow *capture.Flow, dir capture.Direction, touch func(), hook func(*capture.Flow, *capture.Message) ([]byte, bool)) protocol.GRPCTransform {
	return func(m protocol.GRPCMessage) ([]byte, bool) {
		mm := grpcMsg(dir, m)
		flow.AddMessage(mm)
		out, drop := hook(flow, mm)
		touch()
		return out, drop
	}
}

func readGRPCStatus(resp *http.Response, respMsg *capture.Message) {
	if st := resp.Trailer.Get("Grpc-Status"); st != "" {
		respMsg.Meta["grpc-status"] = st
		if gm := resp.Trailer.Get("Grpc-Message"); gm != "" {
			respMsg.Meta["grpc-message"] = gm
		}
	}
}

// setBody updates the content length after an edit. HTTP/2 frames the length
// itself, so the ContentLength field is what matters; we only touch the header
// if the peer sent one, to avoid inventing a Content-Length on a gRPC stream.
func setBody(h http.Header, contentLength *int64, n int) {
	*contentLength = int64(n)
	if h.Get("Content-Length") != "" {
		h.Set("Content-Length", strconv.Itoa(n))
	}
}

// bodyWrap is a pass-through ReadCloser that fires onEOF once, the first time
// the underlying reader reports io.EOF — used to read gRPC trailers and mark the
// flow complete after the body has fully streamed.
type bodyWrap struct {
	r     io.Reader
	c     io.Closer
	onEOF func()
	done  bool
}

func (b *bodyWrap) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF && !b.done {
		b.done = true
		if b.onEOF != nil {
			b.onEOF()
		}
	}
	return n, err
}

func (b *bodyWrap) Close() error {
	if b.c != nil {
		return b.c.Close()
	}
	return nil
}

func grpcMsg(dir capture.Direction, m protocol.GRPCMessage) *capture.Message {
	meta := map[string]string{"fields": protocol.ProtoFieldSummary(m.Data)}
	if m.Compressed {
		meta["compressed"] = "true"
	}
	return &capture.Message{
		Direction: dir,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s grpc %s", dir, humanBytes(len(m.Data))),
		Body:      m.Data,
		Raw:       m.Data,
		Meta:      meta,
	}
}

// h2HeaderDump renders an HTTP/2 request as pseudo-header text for display.
func h2HeaderDump(req *http.Request) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/2\r\n", req.Method, req.URL.RequestURI())
	fmt.Fprintf(&b, ":authority: %s\r\n", req.Host)
	for k, vv := range req.Header {
		for _, v := range vv {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	return []byte(b.String())
}

func clientAddr(req *http.Request, fallback string) string {
	if req.RemoteAddr != "" {
		return req.RemoteAddr
	}
	return fallback
}
