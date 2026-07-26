package protocol

import (
	"bufio"
	"fmt"
	"io"
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

func (HTTP1) Handle(f *capture.Flow, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error {
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
		Body:      capture.DecodeContentEncoding(bodyOf(rawReq), req.Header.Get("Content-Encoding")),
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
		return handleWebSocket(f, req, client, server, tamper, touch)
	}

	// Give the intercept engine its say: it may block (pause), edit, or drop.
	out, drop := tamper.BeforeForward(f, reqMsg)
	if drop {
		f.Status = capture.StatusComplete
		touch()
		return nil
	}
	if out != nil {
		// User supplied exact bytes to send.
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
	resp, err := http.ReadResponse(server.Reader, req)
	if err != nil {
		f.Status = capture.StatusError
		f.Err = err
		touch()
		return err
	}
	rawResp, err := httputil.DumpResponse(resp, true)
	if err != nil {
		return err
	}
	// DumpResponse restores resp.Body, so re-read it for the de-chunked body,
	// then decompress per Content-Encoding. This is display-only — the client is
	// still forwarded the original (compressed) rawResp below, untouched.
	deChunked, _ := io.ReadAll(resp.Body)
	respBody := capture.DecodeContentEncoding(deChunked, resp.Header.Get("Content-Encoding"))
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
		return err
	}
	if err := client.Flush(); err != nil {
		return err
	}

	f.Status = capture.StatusComplete
	touch()
	return nil
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
