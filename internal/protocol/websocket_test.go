package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/export"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/scope"
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
	flow := capture.NewFlow("c", "s:443")
	session := &wsSession{client: src, server: serverRW, flow: flow}
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
	flow := capture.NewFlow("c", "s:443")
	flow.Protocol = capture.ProtoWebSocket
	notifications := 0
	s := &wsSession{client: client, server: server, flow: flow, touch: func() { notifications++ }}

	// client→server injection is masked and lands on the server peer.
	payload := []byte("hi")
	if err := s.Inject(capture.ClientToServer, WSText, payload); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'x' // The caller must not retain ownership of captured bytes.
	fr, err := readWSFrame(bufio.NewReader(&toServer))
	if err != nil {
		t.Fatal(err)
	}
	if !fr.Masked || fr.Opcode != opText || string(fr.Payload) != "hi" {
		t.Errorf("c2s inject: masked=%v op=%x payload=%q", fr.Masked, fr.Opcode, fr.Payload)
	}

	// server→client injection is unmasked and lands on the client peer.
	if err := s.Inject(capture.ServerToClient, WSBinary, []byte("yo\x00\xff")); err != nil {
		t.Fatal(err)
	}
	fr2, err := readWSFrame(bufio.NewReader(&toClient))
	if err != nil {
		t.Fatal(err)
	}
	if fr2.Masked || fr2.Opcode != opBinary || string(fr2.Payload) != "yo\x00\xff" {
		t.Errorf("s2c inject: masked=%v payload=%q", fr2.Masked, fr2.Payload)
	}

	// An echo is an ordinary incoming message, not another injected entry.
	echo := bufio.NewReadWriter(
		bufio.NewReader(bytes.NewReader((&wsFrame{Fin: true, Opcode: opText, Payload: []byte("hi")}).encode())),
		bufio.NewWriter(io.Discard),
	)
	pumpWS(s, echo, flow, capture.ServerToClient, nil, func() {})
	if len(flow.Messages) != 3 || notifications != 3 {
		t.Fatalf("messages=%d notifications=%d, want one per delivered frame", len(flow.Messages), notifications)
	}
	for i, want := range []struct {
		dir      capture.Direction
		payload  string
		injected string
		opcode   string
	}{
		{capture.ClientToServer, "hi", "true", "text"},
		{capture.ServerToClient, "yo\x00\xff", "true", "binary"},
		{capture.ServerToClient, "hi", "", "text"},
	} {
		msg := flow.Messages[i]
		if msg.Direction != want.dir || string(msg.Body) != want.payload || string(msg.Raw) != want.payload ||
			msg.Meta["opcode"] != want.opcode || msg.Meta["injected"] != want.injected || msg.Timestamp.IsZero() {
			t.Errorf("message %d = %+v", i, msg)
		}
	}
	var saved bytes.Buffer
	if err := capture.Save(&saved, []*capture.Flow{flow}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := capture.Load(&saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 || len(reloaded[0].Messages) != 3 {
		t.Fatalf("reloaded captures = %+v", reloaded)
	}
	for i, msg := range reloaded[0].Messages {
		original := flow.Messages[i]
		if msg.Direction != original.Direction || !bytes.Equal(msg.Body, original.Body) ||
			!bytes.Equal(msg.Raw, original.Raw) || !msg.Timestamp.Equal(original.Timestamp) ||
			msg.Meta["opcode"] != original.Meta["opcode"] || msg.Meta["injected"] != original.Meta["injected"] {
			t.Errorf("roundtrip changed message %d: %+v", i, msg)
		}
	}
	text := export.FlowText(reloaded[0])
	if strings.Count(text, "[injected]") != 2 || !strings.Contains(text, "hi") || !strings.Contains(text, "ws:binary") {
		t.Fatalf("text export lost injection history:\n%s", text)
	}
}

type registeredWSTamper struct {
	*intercept.Engine
	registered chan capture.Injector
}

func (w *registeredWSTamper) RegisterSession(id string, injector capture.Injector) {
	w.Engine.RegisterSession(id, injector)
	w.registered <- injector
}

type liveWebSocket struct {
	flow                       *capture.Flow
	client, server             net.Conn
	clientReader, serverReader *bufio.Reader
	injector                   capture.Injector
	stop                       func()
}

// openLiveWebSocket exercises the real upgrade and both pumps with independently
// driven peers. Capture reads happen only after stop joins the handler.
func openLiveWebSocket(t *testing.T, engine *intercept.Engine) *liveWebSocket {
	t.Helper()
	clientConn, clientPeer := net.Pipe()
	serverConn, serverPeer := net.Pipe()
	for _, conn := range []net.Conn{clientConn, clientPeer, serverConn, serverPeer} {
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	closeConnections := func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		_ = clientPeer.Close()
		_ = serverPeer.Close()
	}
	t.Cleanup(closeConnections)
	req := &http.Request{
		Method: "GET",
		URL:    &url.URL{Scheme: "http", Host: "example.test", Path: "/socket"},
		Host:   "example.test",
		Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}},
	}
	originDone := make(chan error, 1)
	serverReader := bufio.NewReader(serverPeer)
	go func() {
		incoming, err := http.ReadRequest(serverReader)
		if err == nil {
			_ = incoming.Body.Close()
			_, err = io.WriteString(serverPeer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		}
		originDone <- err
	}()
	tamper := &registeredWSTamper{Engine: engine, registered: make(chan capture.Injector, 1)}
	flow := capture.NewFlow("client", "example.test:80")
	done := make(chan error, 1)
	go func() {
		done <- handleWebSocket(flow, req,
			bufio.NewReadWriter(bufio.NewReader(clientConn), bufio.NewWriter(clientConn)),
			bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)),
			tamper, func() {}, closeConnections)
	}()
	clientReader := bufio.NewReader(clientPeer)
	response, err := http.ReadResponse(clientReader, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
	if err := <-originDone; err != nil {
		t.Fatal(err)
	}
	var injector capture.Injector
	select {
	case injector = <-tamper.registered:
	case <-time.After(time.Second):
		t.Fatal("live session was not registered")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		closeConnections()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("upgrade handler: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("upgrade handler did not stop")
		}
	}
	t.Cleanup(stop)
	return &liveWebSocket{flow, clientPeer, serverPeer, clientReader, serverReader, injector, stop}
}

func (w *liveWebSocket) send(t *testing.T, dir capture.Direction, opcode byte, payload string) {
	t.Helper()
	peer := w.client
	if dir == capture.ServerToClient {
		peer = w.server
	}
	frame := &wsFrame{
		Fin: true, Opcode: opcode, Payload: []byte(payload),
		Masked: dir == capture.ClientToServer, MaskKey: [4]byte{1, 2, 3, 4},
	}
	if _, err := peer.Write(frame.encode()); err != nil {
		t.Fatalf("send %s: %v", dir, err)
	}
}

func (w *liveWebSocket) receive(t *testing.T, dir capture.Direction, opcode byte, payload string) {
	t.Helper()
	reader := w.serverReader
	if dir == capture.ServerToClient {
		reader = w.clientReader
	}
	frame, err := readWSFrame(reader)
	if err != nil {
		t.Fatalf("receive %s: %v", dir, err)
	}
	if frame.Opcode != opcode || string(frame.Payload) != payload || frame.Masked != (dir == capture.ClientToServer) {
		t.Fatalf("received %s: opcode=%d payload=%q masked=%v", dir, frame.Opcode, frame.Payload, frame.Masked)
	}
}

func TestWebSocketDirectionalInterception(t *testing.T) {
	for _, tc := range []struct {
		name                string
		requests, responses bool
		scope               scope.Action
	}{
		{"response only", false, true, scope.Include},
		{"request only", true, false, scope.Include},
		{"both directions", true, true, scope.Include},
		{"out of scope", true, true, scope.Exclude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := intercept.NewEngine()
			engine.SetScope(&scope.Set{Default: tc.scope})
			engine.SetEnabled(tc.requests)
			engine.SetInterceptResponses(tc.responses)
			pauses := make(chan capture.Direction, 8)
			engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, msg *capture.Message) {
				pauses <- msg.Direction
				engine.Resolve(token, intercept.Resolution{EditedBody: bytes.ToUpper(msg.Raw)})
			})
			live := openLiveWebSocket(t, engine)
			for _, direction := range []struct {
				dir     capture.Direction
				enabled bool
			}{
				{capture.ClientToServer, tc.requests},
				{capture.ServerToClient, tc.responses},
			} {
				want := "payload"
				intercepted := direction.enabled && tc.scope == scope.Include
				if intercepted {
					want = "PAYLOAD"
				}
				live.send(t, direction.dir, opBinary, "payload")
				live.receive(t, direction.dir, opBinary, want)
				if intercepted {
					select {
					case dir := <-pauses:
						if dir != direction.dir {
							t.Fatalf("paused direction = %s, want %s", dir, direction.dir)
						}
					default:
						t.Fatal("frame delivered without directional pause")
					}
				}
				live.send(t, direction.dir, opPing, "control")
				live.receive(t, direction.dir, opPing, "control")
				select {
				case dir := <-pauses:
					t.Fatalf("unexpected %s pause for disabled/out-of-scope data or control frame", dir)
				default:
				}
			}
			live.stop()
			if len(live.flow.Messages) != 2 {
				t.Fatalf("captured %d messages, want only two data frames", len(live.flow.Messages))
			}
		})
	}
}

func TestWebSocketIncomingForwardEditDropContinues(t *testing.T) {
	engine := intercept.NewEngine()
	engine.SetScope(&scope.Set{Default: scope.Include})
	engine.SetInterceptResponses(true)
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, msg *capture.Message) {
		resolution := intercept.Resolution{}
		switch string(msg.Raw) {
		case "edit":
			resolution.EditedBody = []byte("edited")
		case "drop":
			resolution.Decision = intercept.Drop
		}
		engine.Resolve(token, resolution)
	})
	live := openLiveWebSocket(t, engine)
	for _, exchange := range []struct{ sent, received string }{
		{"forward", "forward"}, {"edit", "edited"}, {"drop", ""}, {"later", "later"},
	} {
		live.send(t, capture.ServerToClient, opText, exchange.sent)
		if exchange.received != "" {
			live.receive(t, capture.ServerToClient, opText, exchange.received)
		}
	}
	live.stop()
	if len(live.flow.Messages) != 4 {
		t.Fatalf("captured %d messages, want original messages including dropped frame", len(live.flow.Messages))
	}
	for i, want := range []string{"forward", "edit", "drop", "later"} {
		if got := string(live.flow.Messages[i].Body); got != want {
			t.Errorf("capture %d = %q, want original %q", i, got, want)
		}
	}
}

func TestWebSocketInjectionOvertakesPausedFrameWithoutInterception(t *testing.T) {
	engine := intercept.NewEngine()
	engine.SetScope(&scope.Set{Default: scope.Include})
	engine.SetEnabled(true)
	paused := make(chan intercept.PauseToken, 2)
	engine.OnPause(func(token intercept.PauseToken, _ *capture.Flow, _ *capture.Message) {
		paused <- token
	})
	live := openLiveWebSocket(t, engine)
	live.send(t, capture.ClientToServer, opText, "paused")
	var token intercept.PauseToken
	select {
	case token = <-paused:
	case <-time.After(time.Second):
		t.Fatal("ordinary frame did not pause")
	}
	// Resolve on failure too, so a failed assertion cannot strand the pump.
	defer engine.Resolve(token, intercept.Resolution{})
	injected := make(chan error, 1)
	go func() {
		injected <- live.injector.Inject(capture.ClientToServer, WSText, []byte("injected"))
	}()
	live.receive(t, capture.ClientToServer, opText, "injected")
	if err := <-injected; err != nil {
		t.Fatal(err)
	}
	engine.Resolve(token, intercept.Resolution{})
	live.receive(t, capture.ClientToServer, opText, "paused")
	live.stop()
	if len(live.flow.Messages) != 2 {
		t.Fatalf("capture count = %d", len(live.flow.Messages))
	}
	if first, second := live.flow.Messages[0], live.flow.Messages[1]; string(first.Body) != "injected" ||
		first.Meta["injected"] != "true" || string(second.Body) != "paused" || second.Meta["injected"] != "" {
		t.Fatalf("capture order differs from delivery: %+v", live.flow.Messages)
	}
	select {
	case <-paused:
		t.Fatal("injected frame re-entered interception")
	default:
	}
	if _, ok := engine.Session(live.flow.ID); ok {
		t.Fatal("closed connection retained an injectable session")
	}
	if err := live.injector.Inject(capture.ServerToClient, WSText, []byte("closed")); err == nil {
		t.Fatal("stale injector accepted a closed connection")
	}
	if len(live.flow.Messages) != 2 {
		t.Fatal("failed closed-session injection was captured")
	}
}

type partialWSWriter struct {
	err error
}

func (w partialWSWriter) Write(p []byte) (int, error) {
	return len(p) / 2, w.err
}

func TestWebSocketIncompleteInjectionIsNotCaptured(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		err  error
	}{
		{"short flush", 8, nil},
		{"failed flush", 8, io.ErrClosedPipe},
		{"failed direct write", 8192, io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := capture.NewFlow("client", "server")
			notifications := 0
			dst := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(partialWSWriter{tc.err}))
			session := &wsSession{client: dst, server: dst, flow: flow, touch: func() { notifications++ }}
			err := session.Inject(capture.ClientToServer, WSBinary, bytes.Repeat([]byte("x"), tc.size))
			want := tc.err
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("injection error = %v, want %v", err, want)
			}
			if len(flow.Messages) != 0 || notifications != 0 {
				t.Fatalf("incomplete frame published: messages=%d notifications=%d", len(flow.Messages), notifications)
			}
		})
	}
}

func TestWebSocketInjectionNotificationCanInjectInWriteOrder(t *testing.T) {
	var received bytes.Buffer
	dst := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(&received))
	flow := capture.NewFlow("client", "server")
	session := &wsSession{client: dst, server: dst, flow: flow}
	calls := 0
	var callbackErr error
	session.touch = func() {
		calls++
		if calls == 1 {
			callbackErr = session.Inject(capture.ClientToServer, WSText, []byte("second"))
		}
	}
	done := make(chan error, 1)
	go func() { done <- session.Inject(capture.ClientToServer, WSText, []byte("first")) }()
	select {
	case err := <-done:
		if err != nil || callbackErr != nil {
			t.Fatalf("injection errors: first=%v second=%v", err, callbackErr)
		}
	case <-time.After(time.Second):
		t.Fatal("notification ran under the session write lock")
	}
	if len(flow.Messages) != 2 {
		t.Fatalf("capture count = %d, want two successful injections", len(flow.Messages))
	}
	reader := bufio.NewReader(&received)
	for i, want := range []string{"first", "second"} {
		frame, err := readWSFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if string(frame.Payload) != want || string(flow.Messages[i].Body) != want {
			t.Fatalf("wire/capture order at %d: wire=%q capture=%q", i, frame.Payload, flow.Messages[i].Body)
		}
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
