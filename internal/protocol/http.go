package protocol

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// HTTP1 parses HTTP/1.x request/response exchanges. It is the reference
// implementation for the Protocol interface: detect on the request line,
// forward with a tamper hook, capture both sides into the Flow.
type HTTP1 struct{}

func (HTTP1) Name() capture.Protocol { return capture.ProtoHTTP1 }

// Detect matches an HTTP/1 request line: a known method followed by a space.
func (HTTP1) Detect(peek []byte) bool {
	s := string(peek)
	for _, m := range []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE "} {
		if strings.HasPrefix(s, m) {
			return true
		}
	}
	return false
}

const (
	maxHTTP1StartLineBytes = 8 << 10
	maxHTTP1HeaderBytes    = capture.MaxRetainedHeaderBytes
)

// boundedHTTP1Head reads exactly one HTTP/1 start line and header section
// without reading beyond it. Keeping the original bufio.Reader as the tail
// preserves already-buffered body bytes for the normal net/http parser.
func boundedHTTP1Head(r *bufio.Reader) ([]byte, error) {
	head := make([]byte, 0, 1024)
	lineLen := 0
	lines := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		head = append(head, b)
		lineLen++
		if len(head) > maxHTTP1HeaderBytes {
			return nil, fmt.Errorf("HTTP/1 header section exceeds %d bytes", maxHTTP1HeaderBytes)
		}
		if b != '\n' {
			continue
		}
		if lines == 0 && lineLen > maxHTTP1StartLineBytes {
			return nil, fmt.Errorf("HTTP/1 start line exceeds %d bytes", maxHTTP1StartLineBytes)
		}
		if lineLen == 1 || (lineLen == 2 && head[len(head)-2] == '\r') {
			return head, nil
		}
		lines++
		lineLen = 0
	}
}

func boundedHTTP1Reader(r *bufio.Reader) (*bufio.Reader, error) {
	head, err := boundedHTTP1Head(r)
	if err != nil {
		return nil, err
	}
	return bufio.NewReader(io.MultiReader(bytes.NewReader(head), r)), nil
}

func readHTTP1Request(r *bufio.Reader) (*http.Request, *bufio.Reader, error) {
	bounded, err := boundedHTTP1Reader(r)
	if err != nil {
		return nil, r, err
	}
	req, err := http.ReadRequest(bounded)
	return req, bounded, err
}

func readHTTP1Response(r *bufio.Reader, req *http.Request) (*http.Response, *bufio.Reader, error) {
	bounded, err := boundedHTTP1Reader(r)
	if err != nil {
		return nil, r, err
	}
	resp, err := http.ReadResponse(bounded, req)
	return resp, bounded, err
}

func (h HTTP1) Handle(f *capture.Flow, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error {
	return h.handle(f, client, server, tamper, touch, nil)
}

// HandleConnection lets the proxy provide ownership of the underlying
// connections. Generic 101 upgrades need that ownership to interrupt the
// opposite relay direction when either peer closes.
func (h HTTP1) HandleConnection(
	f *capture.Flow,
	client, server *bufio.ReadWriter,
	clientConn, serverConn net.Conn,
	tamper Tamperer,
	touch func(),
) error {
	closeConnections := func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	}
	return h.handle(f, client, server, tamper, touch, closeConnections)
}

func (HTTP1) handle(
	f *capture.Flow,
	client, server *bufio.ReadWriter,
	tamper Tamperer,
	touch func(),
	closeUpgradeConnections func(),
) error {
	f.Mutate(func() {
		f.Protocol = capture.ProtoHTTP1
	})

	// --- request ---
	req, boundedClient, err := readHTTP1Request(client.Reader)
	client.Reader = boundedClient
	if err != nil {
		f.Mutate(func() {
			f.Status = capture.StatusError
			f.Err = err
		})
		touch()
		return err
	}
	rawReq, _, reqUnavailable, err := captureHTTP1Request(req)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	reqMsg := &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s %s", req.Method, req.URL.RequestURI(), req.Proto),
		Headers:   req.Header,
		Raw:       rawReq,
		Meta: map[string]string{
			"method": req.Method,
			"path":   req.URL.RequestURI(),
			"host":   req.Host,
		},
	}
	if reqUnavailable {
		reqMsg.Truncated = true
		reqMsg.Meta[BodyRepresentationMeta] = BodyRepresentationUnavailable
	} else {
		reqMsg.Body = capture.DecodeContentEncoding(bodyOf(rawReq), strings.Join(req.Header.Values("Content-Encoding"), ","))
	}
	f.Mutate(func() {
		f.Request = reqMsg
	})
	touch()

	// WebSocket is an HTTP upgrade: hand the connection to the frame pump after
	// relaying the handshake. The handshake itself is not tampered (editing it
	// would break the Sec-WebSocket-Accept computation); data frames are.
	if isWebSocketUpgrade(req) {
		return handleWebSocket(f, req, client, server, tamper, touch, closeUpgradeConnections)
	}

	// Give the intercept engine its say only when the whole request was safely
	// retained. An oversized body must stream unchanged rather than letting an
	// editor forward a partial replacement.
	var out []byte
	drop := false
	if !reqUnavailable && tamper != nil {
		out, drop = tamper.BeforeForward(f, reqMsg)
	}
	if drop {
		f.Mutate(func() {
			f.Status = capture.StatusComplete
		})
		touch()
		return nil
	}
	if out != nil {
		// User supplied exact bytes to send. Capture the edited request when it
		// is a complete HTTP message; otherwise mark it as unreconstructible.
		edited, parseErr := captureForwardedHTTP1(reqMsg, out)
		if parseErr != nil {
			reqMsg.Raw = append([]byte(nil), out...)
			reqMsg.Truncated = true
			reqMsg.Meta[BodyRepresentationMeta] = BodyRepresentationUnavailable
		} else {
			req = edited
		}
		touch()
		if _, err := server.Write(out); err != nil {
			return failHTTP1Response(f, err, touch)
		}
	} else if err := req.Write(server); err != nil {
		// req.Write emits correct origin-form with a Host header, converting a
		// proxied absolute-form request into what an origin server expects.
		return failHTTP1Response(f, err, touch)
	}
	if err := server.Flush(); err != nil {
		return failHTTP1Response(f, err, touch)
	}

	// --- response ---
	// A server may send any number of informational responses before the
	// response that completes the exchange. Relay those immediately, but do not
	// expose them as the captured response or offer them to interception.
	// Status 101 is different: it terminates HTTP semantics and switches
	// protocols, so it is handled below as the terminal response.
	var resp *http.Response
	for {
		var boundedServer *bufio.Reader
		resp, boundedServer, err = readHTTP1Response(server.Reader, req)
		server.Reader = boundedServer
		if err != nil {
			return failHTTP1Response(f, err, touch)
		}
		if resp.StatusCode == http.StatusSwitchingProtocols ||
			resp.StatusCode < 100 || resp.StatusCode >= 200 {
			break
		}

		rawInterim, dumpErr := httputil.DumpResponse(resp, false)
		// Informational responses cannot have HTTP message bodies. Close anyway
		// so a future net/http implementation cannot leave resources attached.
		_ = resp.Body.Close()
		if dumpErr != nil {
			return failHTTP1Response(f, dumpErr, touch)
		}
		if _, err := client.Write(rawInterim); err != nil {
			return failHTTP1Response(f, err, touch)
		}
		if err := client.Flush(); err != nil {
			return failHTTP1Response(f, err, touch)
		}
	}
	rawResp, deChunked, respUnavailable, err := captureHTTP1Response(resp)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	respMsg := &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes)", resp.Status, len(deChunked)),
		Headers:   resp.Header,
		Raw:       rawResp,
		Meta:      map[string]string{"status": resp.Status},
	}
	if respUnavailable {
		respMsg.Truncated = true
		respMsg.Meta[BodyRepresentationMeta] = BodyRepresentationUnavailable
	} else {
		respMsg.Body = capture.DecodeContentEncoding(deChunked, strings.Join(resp.Header.Values("Content-Encoding"), ","))
		respMsg.Summary = fmt.Sprintf("%s (%d bytes)", resp.Status, len(respMsg.Body))
	}
	f.Mutate(func() {
		f.Response = respMsg
	})
	touch()

	if respUnavailable {
		// The response body remains attached to resp and is streamed directly.
		// Do not pause it: an editor could only forward an incomplete body.
		if err := resp.Write(client); err != nil {
			return failHTTP1Response(f, err, touch)
		}
		if err := client.Flush(); err != nil {
			return failHTTP1Response(f, err, touch)
		}
		f.Mutate(func() {
			f.Status = capture.StatusComplete
		})
		touch()
		return nil
	}

	// Response-side tamper: may block (pause), edit, or drop (withhold) the
	// response before it reaches the client.
	out = nil
	drop = false
	if tamper != nil {
		out, drop = tamper.BeforeDeliver(f, respMsg)
	}
	if drop {
		f.Mutate(func() {
			f.Status = capture.StatusComplete
		})
		touch()
		return nil
	}
	if out == nil {
		out = rawResp
	}
	if len(out) > capture.MaxRetainedMessageBytes {
		return failHTTP1Response(f, fmt.Errorf("edited HTTP/1 response exceeds %d-byte limit", capture.MaxRetainedMessageBytes), touch)
	}
	if _, err := client.Write(out); err != nil {
		return failHTTP1Response(f, err, touch)
	}
	if err := client.Flush(); err != nil {
		return failHTTP1Response(f, err, touch)
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if err := relayHTTP1Upgrade(client, server, closeUpgradeConnections); err != nil {
			return failHTTP1Response(f, err, touch)
		}
	}

	f.Mutate(func() {
		f.Status = capture.StatusComplete
	})
	touch()
	return nil
}

// relayHTTP1Upgrade retains ownership of a connection after a 101 response.
// Reading through the existing bufio readers is essential: ReadRequest and
// ReadResponse may already have buffered switched-protocol bytes.
func relayHTTP1Upgrade(client, server *bufio.ReadWriter, closeConnections func()) error {
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(http1FlushWriter{server}, client.Reader)
		errs <- err
	}()
	go func() {
		_, err := io.Copy(http1FlushWriter{client}, server.Reader)
		errs <- err
	}()

	first := <-errs
	if closeConnections != nil {
		// A switched connection terminates when either direction closes. Closing
		// both owned connections unblocks the opposite copy, avoiding the
		// client-waits-for-EOF/proxy-waits-for-client deadlock.
		closeConnections()
		<-errs
		return first
	}

	second := <-errs
	if first != nil {
		return first
	}
	return second
}

type http1FlushWriter struct {
	*bufio.ReadWriter
}

func (w http1FlushWriter) Write(p []byte) (int, error) {
	n, err := w.ReadWriter.Write(p)
	if err != nil {
		return n, err
	}
	if err := w.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

func failHTTP1Response(f *capture.Flow, err error, touch func()) error {
	f.Mutate(func() {
		f.Status = capture.StatusError
		f.Err = err
	})
	touch()
	return err
}

func captureForwardedHTTP1(message *capture.Message, raw []byte) (*http.Request, error) {
	if len(raw) > capture.MaxRetainedMessageBytes {
		return nil, fmt.Errorf("edited HTTP/1 request exceeds %d-byte limit", capture.MaxRetainedMessageBytes)
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	req, reader, err := readHTTP1Request(reader)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, capture.MaxRetainedWireBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > capture.MaxRetainedWireBodyBytes {
		return nil, fmt.Errorf("edited HTTP/1 request body exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
	}
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	if _, err := reader.ReadByte(); err == nil {
		return nil, fmt.Errorf("edited HTTP/1 request has trailing bytes")
	} else if err != io.EOF {
		return nil, err
	}

	message.Summary = fmt.Sprintf("%s %s %s", req.Method, req.URL.RequestURI(), req.Proto)
	message.Headers = req.Header
	message.Body = capture.DecodeContentEncoding(body, strings.Join(req.Header.Values("Content-Encoding"), ","))
	message.Raw = append([]byte(nil), raw...)
	message.Truncated = false
	message.Meta = map[string]string{
		"method":               req.Method,
		"path":                 req.URL.RequestURI(),
		"host":                 req.Host,
		BodyRepresentationMeta: BodyRepresentationDecoded,
	}
	return req, nil
}

// bodyOf splits a dumped HTTP message at the header/body boundary and returns
// the body bytes (empty if there is no body).
func bodyOf(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[i+4:]
	}
	return nil
}

type prefixedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *prefixedReadCloser) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

// captureHTTP1Request retains a complete request only while it remains within
// budget. If its declared or observed body is larger, it restores the consumed
// prefix to req.Body so Request.Write can stream the original bytes unchanged.
func captureHTTP1Request(req *http.Request) (raw, body []byte, unavailable bool, err error) {
	if req.Body == nil || req.Body == http.NoBody {
		raw, err = httputil.DumpRequest(req, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, unavailable = limitHTTPWire(raw)
		return raw, nil, unavailable, nil
	}
	if req.ContentLength > capture.MaxRetainedWireBodyBytes {
		raw, err = httputil.DumpRequest(req, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, _ = limitHTTPWire(raw)
		return raw, nil, true, nil
	}
	original := req.Body
	body, err = io.ReadAll(io.LimitReader(original, capture.MaxRetainedWireBodyBytes+1))
	if err != nil {
		_ = original.Close()
		return nil, nil, false, err
	}
	if len(body) > capture.MaxRetainedWireBodyBytes {
		req.Body = &prefixedReadCloser{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
		raw, err = httputil.DumpRequest(req, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, _ = limitHTTPWire(raw)
		return raw, nil, true, nil
	}
	if err := original.Close(); err != nil {
		return nil, nil, false, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	raw, err = httputil.DumpRequest(req, true)
	if err != nil {
		return nil, nil, false, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	raw, unavailable = limitHTTPWire(raw)
	return raw, body, unavailable, nil
}

// captureHTTP1Response is the response counterpart to captureHTTP1Request.
// Oversized bodies remain on resp.Body for Response.Write to stream directly.
func captureHTTP1Response(resp *http.Response) (raw, body []byte, unavailable bool, err error) {
	if resp.Body == nil || resp.Body == http.NoBody {
		raw, err = httputil.DumpResponse(resp, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, unavailable = limitHTTPWire(raw)
		return raw, nil, unavailable, nil
	}
	if resp.ContentLength > capture.MaxRetainedWireBodyBytes {
		raw, err = httputil.DumpResponse(resp, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, _ = limitHTTPWire(raw)
		return raw, nil, true, nil
	}
	original := resp.Body
	body, err = io.ReadAll(io.LimitReader(original, capture.MaxRetainedWireBodyBytes+1))
	if err != nil {
		_ = original.Close()
		return nil, nil, false, err
	}
	if len(body) > capture.MaxRetainedWireBodyBytes {
		resp.Body = &prefixedReadCloser{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
		raw, err = httputil.DumpResponse(resp, false)
		if err != nil {
			return nil, nil, false, err
		}
		raw, _ = limitHTTPWire(raw)
		return raw, nil, true, nil
	}
	if err := original.Close(); err != nil {
		return nil, nil, false, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	raw, err = httputil.DumpResponse(resp, true)
	if err != nil {
		return nil, nil, false, err
	}
	raw, unavailable = limitHTTPWire(raw)
	return raw, body, unavailable, nil
}

func limitHTTPWire(raw []byte) ([]byte, bool) {
	if len(raw) <= capture.MaxRetainedMessageBytes {
		return raw, false
	}
	return append([]byte(nil), raw[:capture.MaxRetainedMessageBytes]...), true
}

func init() { Register(HTTP1{}) }
