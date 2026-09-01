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
	f.Protocol = capture.ProtoHTTP1

	// --- request ---
	req, err := http.ReadRequest(client.Reader)
	if err != nil {
		f.Status = capture.StatusError
		f.Err = err
		touch()
		return err
	}
	rawReq, err := httputil.DumpRequest(req, true)
	if err != nil {
		return err
	}
	reqMsg := &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s %s", req.Method, req.URL.RequestURI(), req.Proto),
		Headers:   req.Header,
		Body:      capture.DecodeContentEncoding(bodyOf(rawReq), strings.Join(req.Header.Values("Content-Encoding"), ",")),
		Raw:       rawReq,
		Meta: map[string]string{
			"method": req.Method,
			"path":   req.URL.RequestURI(),
			"host":   req.Host,
		},
	}
	f.Request = reqMsg
	touch()

	// WebSocket is an HTTP upgrade: hand the connection to the frame pump after
	// relaying the handshake. The handshake itself is not tampered (editing it
	// would break the Sec-WebSocket-Accept computation); data frames are.
	if isWebSocketUpgrade(req) {
		return handleWebSocket(f, req, client, server, tamper, touch, closeUpgradeConnections)
	}

	// Give the intercept engine its say: it may block (pause), edit, or drop.
	out, drop := tamper.BeforeForward(f, reqMsg)
	if drop {
		f.Status = capture.StatusComplete
		touch()
		return nil
	}
	if out != nil {
		// User supplied exact bytes to send. Capture the edited request when it
		// is a complete HTTP message; otherwise mark it as unreconstructible.
		edited, parseErr := captureForwardedHTTP1(reqMsg, out)
		if parseErr != nil {
			reqMsg.Raw = append([]byte(nil), out...)
			reqMsg.Meta[BodyRepresentationMeta] = BodyRepresentationUnavailable
		} else {
			req = edited
		}
		touch()
		if _, err := server.Write(out); err != nil {
			return err
		}
	} else if err := req.Write(server); err != nil {
		// req.Write emits correct origin-form with a Host header, converting a
		// proxied absolute-form request into what an origin server expects.
		return err
	}
	if err := server.Flush(); err != nil {
		return err
	}

	// --- response ---
	// A server may send any number of informational responses before the
	// response that completes the exchange. Relay those immediately, but do not
	// expose them as the captured response or offer them to interception.
	// Status 101 is different: it terminates HTTP semantics and switches
	// protocols, so it is handled below as the terminal response.
	var resp *http.Response
	for {
		resp, err = http.ReadResponse(server.Reader, req)
		if err != nil {
			return failHTTP1Response(f, err, touch)
		}
		if resp.StatusCode == http.StatusSwitchingProtocols ||
			resp.StatusCode < 100 || resp.StatusCode >= 200 {
			break
		}

		rawInterim, dumpErr := httputil.DumpResponse(resp, true)
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
	rawResp, err := httputil.DumpResponse(resp, true)
	if err != nil {
		_ = resp.Body.Close()
		return failHTTP1Response(f, err, touch)
	}
	// DumpResponse restores resp.Body, so re-read it for the de-chunked body,
	// then decompress per Content-Encoding. This is display-only — the client is
	// forwarded the serialized compressed response below.
	deChunked, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return failHTTP1Response(f, err, touch)
	}
	if err := resp.Body.Close(); err != nil {
		return failHTTP1Response(f, err, touch)
	}
	respBody := capture.DecodeContentEncoding(deChunked, strings.Join(resp.Header.Values("Content-Encoding"), ","))
	respMsg := &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes)", resp.Status, len(respBody)),
		Headers:   resp.Header,
		Body:      respBody,
		Raw:       rawResp,
		Meta:      map[string]string{"status": resp.Status},
	}
	f.Response = respMsg
	touch()

	// Response-side tamper: may block (pause), edit, or drop (withhold) the
	// response before it reaches the client.
	out, drop = tamper.BeforeDeliver(f, respMsg)
	if drop {
		f.Status = capture.StatusComplete
		touch()
		return nil
	}
	if out == nil {
		out = rawResp
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

	f.Status = capture.StatusComplete
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
	f.Status = capture.StatusError
	f.Err = err
	touch()
	return err
}

func captureForwardedHTTP1(message *capture.Message, raw []byte) (*http.Request, error) {
	reader := bufio.NewReader(bytes.NewReader(raw))
	req, err := http.ReadRequest(reader)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
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
	if i := strings.Index(string(raw), "\r\n\r\n"); i >= 0 {
		return raw[i+4:]
	}
	return nil
}

func init() { Register(HTTP1{}) }
