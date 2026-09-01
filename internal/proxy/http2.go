package proxy

import (
	"bytes"
	"fmt"
	"io"
	"mime"
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

// isGRPCContentType treats malformed values as ordinary HTTP/2 content types.
func isGRPCContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/grpc" ||
		strings.HasPrefix(mediaType, "application/grpc+") && len(mediaType) > len("application/grpc+")
}

func (c *h2Capture) RoundTrip(req *http.Request) (*http.Response, error) {
	isGRPC := isGRPCContentType(req.Header.Get("Content-Type"))

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
	case c.proxy.tamper.ShouldInterceptRequest(flow) && isGRPC && hasH2RequestBody(req):
		// Streaming gRPC being intercepted: tamper each message in flight, so a
		// long-lived/bidi stream is never buffered (which would deadlock it).
		reqMsg.Raw = h2HeaderDump(req)
		reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
		original := req.Body
		transformed := protocol.NewGRPCTamperReader(original,
			c.grpcTransform(flow, capture.ClientToServer, touch, c.proxy.tamper.BeforeForward))
		var wire bytes.Buffer
		req.ContentLength = -1
		req.Header.Del("Content-Length")
		onEOF, onClose := h2RequestCaptureCallbacks(reqMsg, &wire, true, touch, req.ContentLength)
		req.Body = &bodyWrap{
			r:       io.TeeReader(transformed, &wire),
			c:       original,
			onEOF:   onEOF,
			onClose: onClose,
		}

	case c.proxy.tamper.ShouldInterceptRequest(flow) && hasH2RequestBody(req):
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
		finishH2RequestCapture(reqMsg, out, false)
		setBody(req.Header, &req.ContentLength, len(out))
		req.Body = io.NopCloser(bytes.NewReader(out))

	default:
		// Not intercepted: observe while retaining the bytes that reach upstream.
		reqMsg.Raw = h2HeaderDump(req)
		switch {
		case !hasH2RequestBody(req):
			reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationDecoded
		case isGRPC:
			reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			original := req.Body
			observed := protocol.NewGRPCFramer(original, func(m protocol.GRPCMessage) {
				flow.AddMessage(grpcMsg(capture.ClientToServer, m))
				touch()
			})
			var wire bytes.Buffer
			onEOF, onClose := h2RequestCaptureCallbacks(reqMsg, &wire, true, touch, req.ContentLength)
			req.Body = &bodyWrap{
				r:       io.TeeReader(observed, &wire),
				c:       original,
				onEOF:   onEOF,
				onClose: onClose,
			}
		default:
			reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			original := req.Body
			var wire bytes.Buffer
			onEOF, onClose := h2RequestCaptureCallbacks(reqMsg, &wire, false, touch, req.ContentLength)
			req.Body = &bodyWrap{
				r:       io.TeeReader(original, &wire),
				c:       original,
				onEOF:   onEOF,
				onClose: onClose,
			}
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
		respMsg.Body = capture.DecodeContentEncoding(body, strings.Join(resp.Header.Values("Content-Encoding"), ","))
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

// grpcTransform builds a per-message tamper callback. The hook sees the
// captured input, while the Flow records only the bytes actually forwarded (or
// delivered); dropped messages are omitted.
func (c *h2Capture) grpcTransform(flow *capture.Flow, dir capture.Direction, touch func(), hook func(*capture.Flow, *capture.Message) ([]byte, bool)) protocol.GRPCTransform {
	return func(m protocol.GRPCMessage) ([]byte, bool) {
		out, drop := hook(flow, grpcMsg(dir, m))
		if !drop {
			forwarded := m
			if out != nil {
				forwarded.Data = out
			}
			flow.AddMessage(grpcMsg(dir, forwarded))
		}
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

func hasH2RequestBody(req *http.Request) bool {
	return req.Body != nil && req.Body != http.NoBody
}

func finishH2RequestCapture(message *capture.Message, wire []byte, isGRPC bool) {
	message.Raw = append([]byte(nil), wire...)
	if isGRPC {
		message.Body = append([]byte(nil), wire...)
	} else {
		encoding := strings.Join(http.Header(message.Headers).Values("Content-Encoding"), ",")
		logical, err := capture.DecodeContentEncodingStrict(wire, encoding)
		if err != nil {
			message.Body = append([]byte(nil), wire...)
			message.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			return
		}
		message.Body = logical
	}
	message.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationDecoded
}

func h2RequestCaptureCallbacks(message *capture.Message, wire *bytes.Buffer, isGRPC bool, touch func(), expectedLength int64) (func(), func()) {
	finish := func() {
		finishH2RequestCapture(message, wire.Bytes(), isGRPC)
		touch()
	}
	finishOnClose := func() {
		if expectedLength > 0 && int64(wire.Len()) == expectedLength {
			finish()
		}
	}
	return finish, finishOnClose
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

// bodyWrap is a pass-through ReadCloser that fires onEOF once when the reader
// reaches EOF. Request capture may also supply onClose to finalize a fully
// consumed fixed-length body whose transport does not perform a final EOF read.
type bodyWrap struct {
	r       io.Reader
	c       io.Closer
	onEOF   func()
	onClose func()
	done    bool
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
	if !b.done && b.onClose != nil {
		b.done = true
		b.onClose()
	}
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
