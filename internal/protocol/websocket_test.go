package protocol

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// upperTamper edits every client→server payload to upper-case, to prove the
// pump forwards the *edited* frame while recording the *original*.
type upperTamper struct{}

func (upperTamper) BeforeForward(_ *capture.Flow, msg *capture.Message) ([]byte, bool) {
	return bytes.ToUpper(msg.Raw), false
}

func (upperTamper) BeforeDeliver(_ *capture.Flow, _ *capture.Message) ([]byte, bool) {
	return nil, false
}

func (upperTamper) ShouldInterceptRequest(*capture.Flow) bool  { return false }
func (upperTamper) ShouldInterceptResponse(*capture.Flow) bool { return false }

func TestPumpWSCapturesAndTampers(t *testing.T) {
	// src: a masked text frame "hello" followed by a masked close frame.
	var srcBytes bytes.Buffer
	text := &wsFrame{Fin: true, Opcode: opText, Masked: true, MaskKey: [4]byte{9, 8, 7, 6}, Payload: []byte("hello")}
	closeF := &wsFrame{Fin: true, Opcode: opClose, Masked: true, MaskKey: [4]byte{9, 8, 7, 6}}
	srcBytes.Write(text.encode())
	srcBytes.Write(closeF.encode())

	var dstBytes bytes.Buffer
	src := bufio.NewReadWriter(bufio.NewReader(&srcBytes), bufio.NewWriter(&bytes.Buffer{}))
	// client→server pump writes to the session's server peer.
	serverRW := bufio.NewReadWriter(bufio.NewReader(&bytes.Buffer{}), bufio.NewWriter(&dstBytes))
	session := &wsSession{client: src, server: serverRW}

	flow := capture.NewFlow("c", "s:443")
	pumpWS(session, src, flow, capture.ClientToServer, upperTamper{}, func() {})

	// Recorded message is the ORIGINAL payload.
	if len(flow.Messages) != 1 {
		t.Fatalf("want 1 recorded data frame, got %d", len(flow.Messages))
	}
	if got := string(flow.Messages[0].Body); got != "hello" {
		t.Errorf("recorded body = %q, want original %q", got, "hello")
	}

	// Forwarded bytes are the EDITED payload.
	fr, err := readWSFrame(bufio.NewReader(&dstBytes))
	if err != nil {
		t.Fatalf("decode forwarded: %v", err)
	}
	if got := string(fr.Payload); got != "HELLO" {
		t.Errorf("forwarded payload = %q, want edited %q", got, "HELLO")
	}
}

func TestSessionInject(t *testing.T) {
	var toServer, toClient bytes.Buffer
	server := bufio.NewReadWriter(bufio.NewReader(&bytes.Buffer{}), bufio.NewWriter(&toServer))
	client := bufio.NewReadWriter(bufio.NewReader(&bytes.Buffer{}), bufio.NewWriter(&toClient))
	s := &wsSession{client: client, server: server}

	// client→server injection is masked and lands on the server peer.
	if err := s.Inject(capture.ClientToServer, WSText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	fr, err := readWSFrame(bufio.NewReader(&toServer))
	if err != nil {
		t.Fatal(err)
	}
	if !fr.Masked || fr.Opcode != opText || string(fr.Payload) != "hi" {
		t.Errorf("c2s inject: masked=%v op=%x payload=%q", fr.Masked, fr.Opcode, fr.Payload)
	}

	// server→client injection is unmasked and lands on the client peer.
	if err := s.Inject(capture.ServerToClient, WSText, []byte("yo")); err != nil {
		t.Fatal(err)
	}
	fr2, err := readWSFrame(bufio.NewReader(&toClient))
	if err != nil {
		t.Fatal(err)
	}
	if fr2.Masked || string(fr2.Payload) != "yo" {
		t.Errorf("s2c inject: masked=%v payload=%q", fr2.Masked, fr2.Payload)
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	up := &http.Request{Header: http.Header{"Upgrade": {"websocket"}, "Connection": {"keep-alive, Upgrade"}}}
	if !isWebSocketUpgrade(up) {
		t.Error("should detect a websocket upgrade with a multi-token Connection header")
	}
	plain := &http.Request{Header: http.Header{"Connection": {"keep-alive"}}}
	if isWebSocketUpgrade(plain) {
		t.Error("plain request should not be detected as an upgrade")
	}
}

func TestWebSocketRejectionWithContentLength(t *testing.T) {
	flow, responseBytes, err := runWebSocketHandshake(
		t,
		"HTTP/1.1 401 Unauthorized\r\nContent-Length: 13\r\nContent-Type: text/plain\r\n\r\naccess denied",
		nil,
	)
	if err != nil {
		t.Fatalf("handle rejection: %v", err)
	}

	resp, body := readClientResponse(t, responseBytes)
	if resp.StatusCode != http.StatusUnauthorized || string(body) != "access denied" {
		t.Fatalf("client response = %d %q, want 401 %q", resp.StatusCode, body, "access denied")
	}
	if flow.Status != capture.StatusComplete {
		t.Fatalf("flow status = %v, want complete", flow.Status)
	}
	if flow.Response == nil {
		t.Fatal("flow response was not captured")
	}
	if got := string(flow.Response.Body); got != "access denied" {
		t.Fatalf("captured body = %q, want %q", got, "access denied")
	}
	if flow.Response.Meta["status"] != "401 Unauthorized" {
		t.Fatalf("captured status = %q, want %q", flow.Response.Meta["status"], "401 Unauthorized")
	}
}

func TestWebSocketRejectionWithChunkedBody(t *testing.T) {
	flow, responseBytes, err := runWebSocketHandshake(
		t,
		"HTTP/1.1 400 Bad Request\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nnope\r\n5\r\nagain\r\n0\r\n\r\n",
		nil,
	)
	if err != nil {
		t.Fatalf("handle rejection: %v", err)
	}

	resp, body := readClientResponse(t, responseBytes)
	if resp.StatusCode != http.StatusBadRequest || string(body) != "nopeagain" {
		t.Fatalf("client response = %d %q, want 400 %q", resp.StatusCode, body, "nopeagain")
	}
	if flow.Response == nil {
		t.Fatal("flow response was not captured")
	}
	if string(flow.Response.Body) != "nopeagain" {
		t.Fatalf("captured response body = %q, want %q", flow.Response.Body, "nopeagain")
	}
}

func TestWebSocketRejectionWithCloseDelimitedBody(t *testing.T) {
	flow, responseBytes, err := runWebSocketHandshake(
		t,
		"HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Type: text/plain\r\n\r\nclosed body",
		nil,
	)
	if err != nil {
		t.Fatalf("handle rejection: %v", err)
	}

	resp, body := readClientResponse(t, responseBytes)
	if resp.StatusCode != http.StatusForbidden || string(body) != "closed body" {
		t.Fatalf("client response = %d %q, want 403 %q", resp.StatusCode, body, "closed body")
	}
	if flow.Response == nil {
		t.Fatal("flow response was not captured")
	}
	if string(flow.Response.Body) != "closed body" {
		t.Fatalf("captured response body = %q, want %q", flow.Response.Body, "closed body")
	}
}

func TestWebSocketEmptyRejection(t *testing.T) {
	flow, responseBytes, err := runWebSocketHandshake(
		t,
		"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n",
		nil,
	)
	if err != nil {
		t.Fatalf("handle rejection: %v", err)
	}

	resp, body := readClientResponse(t, responseBytes)
	if resp.StatusCode != http.StatusForbidden || len(body) != 0 {
		t.Fatalf("client response = %d %q, want empty 403", resp.StatusCode, body)
	}
	if flow.Response == nil {
		t.Fatal("flow response was not captured")
	}
	if len(flow.Response.Body) != 0 {
		t.Fatalf("captured response body = %q, want empty", flow.Response.Body)
	}
}

func TestWebSocketTruncatedRejectionIsError(t *testing.T) {
	flow, responseBytes, err := runWebSocketHandshake(
		t,
		"HTTP/1.1 401 Unauthorized\r\nContent-Length: 12\r\n\r\nshort",
		nil,
	)
	if err == nil {
		t.Fatal("truncated response unexpectedly succeeded")
	}
	if flow.Status != capture.StatusError || flow.Err == nil {
		t.Fatalf("flow error state = (%v, %v), want error", flow.Status, flow.Err)
	}
	if flow.Response != nil {
		t.Fatal("partial response was captured as complete")
	}
	if len(responseBytes) != 0 {
		t.Fatalf("client received partial response %q", responseBytes)
	}
}

func TestWebSocketRejectionWriteFailureIsError(t *testing.T) {
	server := bufio.NewReadWriter(
		bufio.NewReader(strings.NewReader("Content-Length: 4\r\n\r\nnope")),
		bufio.NewWriter(io.Discard),
	)
	downstream := &controlledHTTP1Writer{failAt: 1}
	client := bufio.NewReadWriter(
		bufio.NewReader(bytes.NewReader(nil)),
		bufio.NewWriter(downstream),
	)
	flow := capture.NewFlow("client", "example.test:80")

	err := relayWebSocketRejection(
		flow,
		&http.Request{Method: "GET"},
		"HTTP/1.1 400 Bad Request\r\n",
		server,
		client,
		func() {},
	)
	if err == nil {
		t.Fatal("downstream write failure unexpectedly succeeded")
	}
	if flow.Status != capture.StatusError || flow.Err == nil {
		t.Fatalf("flow error state = (%v, %v), want error", flow.Status, flow.Err)
	}
	if flow.Response == nil || string(flow.Response.Body) != "nope" {
		t.Fatal("complete upstream response was not captured before delivery failed")
	}
}

func TestWebSocketSwitchingProtocolsPreservesBufferedFrames(t *testing.T) {
	handshake := []byte("HTTP/1.1 101 Switching Protocols\r\nuPgRaDe: websocket\r\nConnection: Upgrade\r\nX-Weird:  spaced value \r\n\r\n")
	textFrame := (&wsFrame{Fin: true, Opcode: opText, Payload: []byte("ready")}).encode()
	serverClose := (&wsFrame{Fin: true, Opcode: opClose}).encode()
	upstream := append(append(append([]byte(nil), handshake...), textFrame...), serverClose...)
	clientClose := (&wsFrame{
		Fin:     true,
		Opcode:  opClose,
		Masked:  true,
		MaskKey: [4]byte{1, 2, 3, 4},
	}).encode()

	flow, responseBytes, err := runWebSocketHandshake(t, string(upstream), clientClose)
	if err != nil {
		t.Fatalf("handle upgrade: %v", err)
	}
	want := append(append(append([]byte(nil), handshake...), textFrame...), serverClose...)
	if !bytes.Equal(responseBytes, want) {
		t.Fatalf("client bytes changed:\n got %x\nwant %x", responseBytes, want)
	}
	if flow.Response != nil {
		t.Fatal("101 path unexpectedly captured a normal HTTP response")
	}
	if len(flow.Messages) != 1 || string(flow.Messages[0].Body) != "ready" {
		t.Fatalf("captured frames = %#v, want one ready frame", flow.Messages)
	}
}

func TestWebSocketHandleConnectionEndsWhenUpstreamCloses(t *testing.T) {
	handshake := []byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	textFrame := (&wsFrame{Fin: true, Opcode: opText, Payload: []byte("ready")}).encode()
	upstreamBytes := append(append([]byte(nil), handshake...), textFrame...)

	clientConn, clientPeer := net.Pipe()
	serverConn, serverPeer := net.Pipe()
	defer clientConn.Close()
	defer clientPeer.Close()
	defer serverConn.Close()
	defer serverPeer.Close()

	deadline := time.Now().Add(2 * time.Second)
	for _, conn := range []net.Conn{clientConn, clientPeer, serverConn, serverPeer} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}

	originDone := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(serverPeer))
		if err == nil {
			_ = req.Body.Close()
			_, err = serverPeer.Write(upstreamBytes)
		}
		_ = serverPeer.Close()
		originDone <- err
	}()

	flow := capture.NewFlow("client", "example.test:80")
	handleDone := make(chan error, 1)
	go func() {
		client := bufio.NewReadWriter(bufio.NewReader(clientConn), bufio.NewWriter(clientConn))
		server := bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
		handleDone <- (HTTP1{}).HandleConnection(flow, client, server, clientConn, serverConn, nil, func() {})
	}()

	if _, err := io.WriteString(
		clientPeer,
		"GET /socket HTTP/1.1\r\nHost: example.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
	); err != nil {
		t.Fatalf("write request: %v", err)
	}
	got, err := io.ReadAll(clientPeer)
	if err != nil {
		t.Fatalf("client did not observe clean EOF after upstream closed: %v", err)
	}
	if !bytes.Equal(got, upstreamBytes) {
		t.Fatalf("client bytes changed:\n got %x\nwant %x", got, upstreamBytes)
	}
	if err := <-originDone; err != nil {
		t.Fatalf("origin: %v", err)
	}
	select {
	case err := <-handleDone:
		if err != nil {
			t.Fatalf("HandleConnection: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleConnection did not return after upstream closed")
	}
	if flow.Status != capture.StatusComplete {
		t.Fatalf("flow status = %s, want done", flow.Status)
	}
	if len(flow.Messages) != 1 || string(flow.Messages[0].Body) != "ready" {
		t.Fatalf("captured frames = %#v, want one ready frame", flow.Messages)
	}
}

func runWebSocketHandshake(t *testing.T, upstream string, clientFrames []byte) (*capture.Flow, []byte, error) {
	t.Helper()

	req := &http.Request{
		Method: "GET",
		URL:    &url.URL{Scheme: "http", Host: "example.test", Path: "/socket"},
		Host:   "example.test",
		Proto:  "HTTP/1.1",
		Header: http.Header{
			"Connection": {"Upgrade"},
			"Upgrade":    {"websocket"},
		},
	}
	var clientOutput, serverOutput bytes.Buffer
	client := bufio.NewReadWriter(
		bufio.NewReader(bytes.NewReader(clientFrames)),
		bufio.NewWriter(&clientOutput),
	)
	server := bufio.NewReadWriter(
		bufio.NewReader(strings.NewReader(upstream)),
		bufio.NewWriter(&serverOutput),
	)
	flow := capture.NewFlow("client", "example.test:80")

	err := handleWebSocket(flow, req, client, server, nil, func() {}, nil)
	return flow, append([]byte(nil), clientOutput.Bytes()...), err
}

func readClientResponse(t *testing.T, raw []byte) (*http.Response, []byte) {
	t.Helper()
	req := &http.Request{Method: "GET"}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), req)
	if err != nil {
		t.Fatalf("parse client response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read client response body: %v", err)
	}
	return resp, body
}
