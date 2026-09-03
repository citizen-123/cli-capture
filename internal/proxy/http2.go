package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"golang.org/x/net/http2"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

const (
	maxH2HeaderBytes       = capture.MaxRetainedHeaderBytes
	maxH2ConcurrentStreams = uint32(maxConcurrentConnections)
)

func newH2Server() *http2.Server {
	return &http2.Server{MaxConcurrentStreams: maxH2ConcurrentStreams}
}

// x/net derives its HTTP/2 frame-parser header-list limit from
// ServeConnOpts.BaseConfig.MaxHeaderBytes before reading a request.
func newH2ServeConnOpts(handler http.Handler) *http2.ServeConnOpts {
	return &http2.ServeConnOpts{
		BaseConfig: &http.Server{MaxHeaderBytes: maxH2HeaderBytes},
		Handler:    handler,
	}
}

// serveH2 handles an ALPN-negotiated HTTP/2 client connection. It runs Go's
// http2.Server toward the client and reverse-proxies each stream to the real
// upstream over HTTP/2, capturing (and, at the request-header boundary,
// intercepting) every exchange. gRPC bodies are parsed into per-message records
// as they stream. FlushInterval:-1 makes the proxy copy frames through
// immediately, which is required for server/bidi-streaming gRPC.
func (p *Proxy) serveH2(client net.Conn, target, sni string) {
	upstream := &http2.Transport{
		TLSClientConfig: p.upstreamConfig(sni),
		DialTLSContext: func(ctx context.Context, network, addr string, config *tls.Config) (net.Conn, error) {
			handshakeCtx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
			defer cancel()
			raw, err := (&net.Dialer{}).DialContext(handshakeCtx, network, addr)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, config)
			if err := conn.HandshakeContext(handshakeCtx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		},
	}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = target
		},
		Transport:     &h2Capture{base: upstream, proxy: p, target: target, sni: sni},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		},
	}

	newH2Server().ServeConn(client, newH2ServeConnOpts(rp))
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
	updateRequest := func(fn func()) {
		flow.Mutate(fn)
		touch()
	}

	// --- request side ---
	requestFailed := func(readErr error) {
		flow.Mutate(func() {
			flow.Status = capture.StatusError
			flow.Err = readErr
		})
		touch()
	}
	wrapRequest := func(reader io.Reader, closer io.Closer, grpc bool) {
		retained := &h2RetainedBody{}
		req.Body = &bodyWrap{
			r:        &h2RetainingReader{reader: reader, retained: retained},
			c:        closer,
			expected: req.ContentLength,
			onEOF: func() {
				flow.Mutate(func() {
					finishH2RetainedRequest(reqMsg, retained, grpc)
				})
				touch()
			},
			onError: requestFailed,
		}
	}
	switch {
	case c.proxy.tamper.ShouldInterceptRequest(flow) && !hasH2RequestBody(req):
		updateRequest(func() {
			finishH2RequestCapture(reqMsg, nil, false)
		})
		out, drop := protocol.BeforeForwardContext(req.Context(), c.proxy.tamper, flow, reqMsg)
		if drop {
			flow.Mutate(func() {
				flow.Status = capture.StatusComplete
			})
			touch()
			return nil, fmt.Errorf("cli-capture: request dropped by user")
		}
		if out == nil {
			break
		}
		if len(out) > capture.MaxRetainedWireBodyBytes {
			err := fmt.Errorf("edited HTTP/2 request exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
			requestFailed(err)
			return nil, err
		}
		updateRequest(func() {
			finishH2RequestCapture(reqMsg, out, false)
		})
		setBody(req.Header, &req.ContentLength, len(out))
		req.Body = io.NopCloser(bytes.NewReader(out))

	case c.proxy.tamper.ShouldInterceptRequest(flow) && isGRPC && hasH2RequestBody(req):
		// Streaming gRPC is transformed one bounded frame at a time.
		updateRequest(func() {
			setH2HeaderDump(reqMsg, req)
			reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
		})
		original := req.Body
		transformed := protocol.NewGRPCTamperReader(original,
			c.grpcTransform(flow, capture.ClientToServer, touch, func(f *capture.Flow, msg *capture.Message) ([]byte, bool) {
				return protocol.BeforeForwardContext(req.Context(), c.proxy.tamper, f, msg)
			}))
		req.ContentLength = -1
		req.Header.Del("Content-Length")
		wrapRequest(transformed, original, true)

	case c.proxy.tamper.ShouldInterceptRequest(flow) && hasH2RequestBody(req):
		// Editing requires a complete body. Retain at most the shared budget;
		// when it is exceeded, restore the consumed prefix and stream unchanged.
		original := req.Body
		if req.ContentLength > capture.MaxRetainedWireBodyBytes {
			updateRequest(func() {
				setH2HeaderDump(reqMsg, req)
				reqMsg.Truncated = true
				reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			})
			wrapRequest(original, original, false)
			break
		}
		body, oversized, readErr := readH2InterceptBody(original)
		if readErr != nil {
			requestFailed(readErr)
			return nil, readErr
		}
		if oversized {
			updateRequest(func() {
				setH2HeaderDump(reqMsg, req)
				reqMsg.Truncated = true
				reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			})
			wrapRequest(&h2PrefixedBody{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}, original, false)
			break
		}
		updateRequest(func() {
			finishH2RequestCapture(reqMsg, body, false)
		})
		out, drop := protocol.BeforeForwardContext(req.Context(), c.proxy.tamper, flow, reqMsg)
		if drop {
			flow.Mutate(func() {
				flow.Status = capture.StatusComplete
			})
			touch()
			return nil, fmt.Errorf("cli-capture: request dropped by user")
		}
		if out == nil {
			out = body
		}
		if len(out) > capture.MaxRetainedWireBodyBytes {
			err := fmt.Errorf("edited HTTP/2 request exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
			requestFailed(err)
			return nil, err
		}
		updateRequest(func() {
			finishH2RequestCapture(reqMsg, out, false)
		})
		setBody(req.Header, &req.ContentLength, len(out))
		req.Body = io.NopCloser(bytes.NewReader(out))

	default:
		// Not intercepted: observe bounded bytes while preserving the stream.
		updateRequest(func() {
			setH2HeaderDump(reqMsg, req)
			if !hasH2RequestBody(req) && !reqMsg.Truncated {
				reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationDecoded
			} else {
				reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			}
		})
		switch {
		case !hasH2RequestBody(req):
		case isGRPC:
			original := req.Body
			observed := protocol.NewGRPCFramerWithError(original, func(m protocol.GRPCMessage) {
				flow.AddMessage(grpcMsg(capture.ClientToServer, m))
				touch()
			}, func(error) {
				flow.Mutate(func() {
					reqMsg.Truncated = true
					reqMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
					flow.Truncated = true
				})
				touch()
			})
			wrapRequest(observed, original, true)
		default:
			wrapRequest(req.Body, req.Body, false)
		}
	}

	resp, err := c.base.RoundTrip(req)
	if err != nil {
		flow.Mutate(func() {
			flow.Status = capture.StatusError
			flow.Err = err
		})
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
	flow.Mutate(func() {
		flow.Response = respMsg
	})
	touch()
	updateResponse := func(fn func()) {
		flow.Mutate(fn)
		touch()
	}

	// --- response side ---
	responseFailed := func(readErr error) {
		flow.Mutate(func() {
			flow.Status = capture.StatusError
			flow.Err = readErr
		})
		touch()
	}
	onEnd := func() {
		flow.Mutate(func() {
			readGRPCStatus(resp, respMsg)
			if flow.Status != capture.StatusError {
				flow.Status = capture.StatusComplete
			}
		})
		touch()
	}
	wrapResponse := func(reader io.Reader, closer io.Closer) {
		resp.Body = &bodyWrap{r: reader, c: closer, expected: resp.ContentLength, onEOF: onEnd, onError: responseFailed}
	}
	switch {
	case c.proxy.tamper.ShouldInterceptResponse(flow) && isGRPC:
		// A transformed gRPC frame can change the response size after headers
		// have been received, so the original fixed length cannot be sent.
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		original := resp.Body
		wrapResponse(protocol.NewGRPCTamperReader(original,
			c.grpcTransform(flow, capture.ServerToClient, touch, func(f *capture.Flow, msg *capture.Message) ([]byte, bool) {
				return protocol.BeforeDeliverContext(req.Context(), c.proxy.tamper, f, msg)
			})), original)
		return resp, nil

	case c.proxy.tamper.ShouldInterceptResponse(flow):
		// Editing requires a complete response. On overflow restore the prefix
		// and let ReverseProxy stream the original response unchanged.
		original := resp.Body
		if resp.ContentLength > capture.MaxRetainedWireBodyBytes {
			updateResponse(func() {
				respMsg.Truncated = true
				respMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			})
			wrapResponse(original, original)
			return resp, nil
		}
		body, oversized, readErr := readH2InterceptBody(original)
		if readErr != nil {
			responseFailed(readErr)
			return nil, readErr
		}
		if oversized {
			updateResponse(func() {
				respMsg.Truncated = true
				respMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
			})
			prefixed := &h2PrefixedBody{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
			wrapResponse(prefixed, prefixed)
			return resp, nil
		}
		updateResponse(func() {
			readGRPCStatus(resp, respMsg) // trailers are populated once the body is fully read
			respMsg.Body = capture.DecodeContentEncoding(body, strings.Join(resp.Header.Values("Content-Encoding"), ","))
			respMsg.Raw = body
		})
		out, drop := protocol.BeforeDeliverContext(req.Context(), c.proxy.tamper, flow, respMsg)
		if drop {
			flow.Mutate(func() {
				flow.Status = capture.StatusComplete
			})
			touch()
			return nil, fmt.Errorf("cli-capture: response dropped by user")
		}
		if out == nil {
			out = body
		}
		if len(out) > capture.MaxRetainedWireBodyBytes {
			err := fmt.Errorf("edited HTTP/2 response exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
			responseFailed(err)
			return nil, err
		}
		setBody(resp.Header, &resp.ContentLength, len(out))
		resp.Body = io.NopCloser(bytes.NewReader(out))
		flow.Mutate(func() {
			flow.Status = capture.StatusComplete
		})
		touch()
		return resp, nil
	}

	// Not intercepted: observe streaming, read gRPC status from trailers at EOF.
	if isGRPC {
		original := resp.Body
		observed := protocol.NewGRPCFramerWithError(original, func(m protocol.GRPCMessage) {
			flow.AddMessage(grpcMsg(capture.ServerToClient, m))
			touch()
		}, func(error) {
			flow.Mutate(func() {
				respMsg.Truncated = true
				respMsg.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
				flow.Truncated = true
			})
			touch()
		})
		wrapResponse(observed, original)
	} else {
		wrapResponse(resp.Body, resp.Body)
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
			if out != nil && len(out) > capture.MaxFrameBytes {
				// grpcTamperReader will reject this oversized replacement before
				// it serializes any partial frame.
				flow.Mutate(func() {
					flow.Truncated = true
				})
				touch()
				return out, false
			}
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

// h2RetainedBody keeps a bounded copy of a streamed entity. It never retains
// the incoming slice's backing array, and its capacity never grows past the
// shared wire-body budget.
type h2RetainedBody struct {
	data      []byte
	truncated bool
}

func (b *h2RetainedBody) retain(p []byte) {
	remaining := capture.MaxRetainedWireBodyBytes - len(b.data)
	if remaining <= 0 {
		b.truncated = b.truncated || len(p) != 0
		return
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	needed := len(b.data) + len(p)
	if needed > cap(b.data) {
		nextCap := cap(b.data) * 2
		if nextCap < needed {
			nextCap = needed
		}
		if nextCap > capture.MaxRetainedWireBodyBytes {
			nextCap = capture.MaxRetainedWireBodyBytes
		}
		next := make([]byte, len(b.data), nextCap)
		copy(next, b.data)
		b.data = next
	}
	b.data = append(b.data, p...)
}

type h2RetainingReader struct {
	reader   io.Reader
	retained *h2RetainedBody
}

func (r *h2RetainingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.retained.retain(p[:n])
	}
	return n, err
}

type h2PrefixedBody struct {
	io.Reader
	closer io.Closer
}

func (r *h2PrefixedBody) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

// readH2InterceptBody reads one complete body only while it is within the
// configured edit budget. Callers restore an oversized prefix to the stream.
func readH2InterceptBody(body io.ReadCloser) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, capture.MaxRetainedWireBodyBytes+1))
	if err != nil {
		_ = body.Close()
		return nil, false, err
	}
	if len(data) > capture.MaxRetainedWireBodyBytes {
		return data, true, nil
	}
	if err := body.Close(); err != nil {
		return nil, false, err
	}
	return data, false, nil
}

func finishH2RetainedRequest(message *capture.Message, retained *h2RetainedBody, isGRPC bool) {
	if retained.truncated || message.Truncated {
		message.Raw = append([]byte(nil), retained.data...)
		message.Body = nil
		message.Truncated = true
		message.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
		return
	}
	finishH2RequestCapture(message, retained.data, isGRPC)
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

// bodyWrap preserves EOF, read, and close outcomes while attaching capture
// lifecycle callbacks. A fixed-length body closed after exactly its declared
// bytes is complete even when the transport does not issue a final Read EOF.
// A partial or failed close is never finalized as a complete capture.
type bodyWrap struct {
	r        io.Reader
	c        io.Closer
	expected int64
	read     int64
	onEOF    func()
	onError  func(error)
	done     bool
}

func (b *bodyWrap) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.done || err == nil {
		return n, err
	}
	b.done = true
	if err == io.EOF {
		if b.onEOF != nil {
			b.onEOF()
		}
	} else if b.onError != nil {
		b.onError(err)
	}
	return n, err
}

func (b *bodyWrap) Close() error {
	if b.c == nil {
		return nil
	}
	err := b.c.Close()
	if b.done {
		return err
	}
	b.done = true
	if err != nil {
		if b.onError != nil {
			b.onError(err)
		}
		return err
	}
	if b.expected >= 0 && b.read == b.expected {
		if b.onEOF != nil {
			b.onEOF()
		}
		return nil
	}
	if b.onError != nil {
		b.onError(io.ErrUnexpectedEOF)
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

// setH2HeaderDump renders a bounded pseudo-header view for display. Headers
// are untrusted input too, so a huge value marks the request unavailable rather
// than allocating an unbounded presentation string.
func setH2HeaderDump(message *capture.Message, req *http.Request) {
	var b strings.Builder
	truncated := false
	write := func(value string) {
		if truncated {
			return
		}
		remaining := capture.MaxRetainedHeaderBytes - b.Len()
		if remaining <= 0 {
			truncated = true
			return
		}
		if len(value) > remaining {
			_, _ = b.WriteString(value[:remaining])
			truncated = true
			return
		}
		_, _ = b.WriteString(value)
	}
	write(req.Method)
	write(" ")
	write(req.URL.RequestURI())
	write(" HTTP/2\r\n:authority: ")
	write(req.Host)
	write("\r\n")
	for key, values := range req.Header {
		for _, value := range values {
			write(key)
			write(": ")
			write(value)
			write("\r\n")
		}
	}
	message.Raw = []byte(b.String())
	if truncated {
		message.Truncated = true
		message.Meta[protocol.BodyRepresentationMeta] = protocol.BodyRepresentationUnavailable
	}
}

func clientAddr(req *http.Request, fallback string) string {
	if req.RemoteAddr != "" {
		return req.RemoteAddr
	}
	return fallback
}
