package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

type recordingHTTP1Tamper struct {
	delivered []*capture.Message
}

func (*recordingHTTP1Tamper) BeforeForward(*capture.Flow, *capture.Message) ([]byte, bool) {
	return nil, false
}

func (t *recordingHTTP1Tamper) BeforeDeliver(_ *capture.Flow, msg *capture.Message) ([]byte, bool) {
	t.delivered = append(t.delivered, msg)
	return nil, false
}

func (*recordingHTTP1Tamper) ShouldInterceptRequest(*capture.Flow) bool  { return false }
func (*recordingHTTP1Tamper) ShouldInterceptResponse(*capture.Flow) bool { return false }

type controlledHTTP1Writer struct {
	bytes.Buffer
	writes int
	failAt int
}

func (w *controlledHTTP1Writer) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("controlled downstream write failure")
	}
	return w.Buffer.Write(p)
}

type http1HandleResult struct {
	flow      *capture.Flow
	toClient  string
	toServer  string
	delivered []*capture.Message
	err       error
}

func runHTTP1Handle(response string) http1HandleResult {
	return runHTTP1HandleWith(response, "", 0)
}

func runHTTP1HandleWith(response, clientAfterRequest string, failClientWriteAt int) http1HandleResult {
	clientInput := strings.NewReader("GET /resource HTTP/1.1\r\nHost: example.test\r\n\r\n" + clientAfterRequest)
	serverInput := strings.NewReader(response)
	var toServer bytes.Buffer
	toClient := &controlledHTTP1Writer{failAt: failClientWriteAt}
	client := bufio.NewReadWriter(bufio.NewReader(clientInput), bufio.NewWriter(toClient))
	server := bufio.NewReadWriter(bufio.NewReader(serverInput), bufio.NewWriter(&toServer))
	flow := capture.NewFlow("client", "example.test:80")
	tamper := &recordingHTTP1Tamper{}

	err := (HTTP1{}).Handle(flow, client, server, tamper, func() {})
	return http1HandleResult{
		flow:      flow,
		toClient:  toClient.String(),
		toServer:  toServer.String(),
		delivered: tamper.delivered,
		err:       err,
	}
}

func TestHTTP1HandleRelaysEarlyHintsBeforeFinalResponse(t *testing.T) {
	wire := "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\n" +
		"HTTP/1.1 200 OK\r\nContent-Length: 5\r\nX-Final: yes\r\n\r\nhello"
	got := runHTTP1Handle(wire)

	if got.err != nil {
		t.Fatalf("Handle: %v", got.err)
	}
	if got.toClient != wire {
		t.Fatalf("bytes delivered to client:\n%q\nwant:\n%q", got.toClient, wire)
	}
	if got.flow.Status != capture.StatusComplete {
		t.Errorf("flow status = %s, want done", got.flow.Status)
	}
	if got.flow.Response == nil || got.flow.Response.Meta["status"] != "200 OK" || string(got.flow.Response.Body) != "hello" {
		t.Fatalf("captured final response = %+v", got.flow.Response)
	}
	if len(got.delivered) != 1 || got.delivered[0] != got.flow.Response {
		t.Fatalf("response interception saw %+v, want only final response", got.delivered)
	}
}

func TestHTTP1HandleRelaysMultipleInformationalResponsesInOrder(t *testing.T) {
	wire := "HTTP/1.1 100 Continue\r\n\r\n" +
		"HTTP/1.1 103 Early Hints\r\nLink: </one>; rel=preload\r\n\r\n" +
		"HTTP/1.1 103 Early Hints\r\nLink: </two>; rel=preload\r\n\r\n" +
		"HTTP/1.1 204 No Content\r\nX-Final: yes\r\n\r\n"
	got := runHTTP1Handle(wire)

	if got.err != nil {
		t.Fatalf("Handle: %v", got.err)
	}
	if got.toClient != wire {
		t.Fatalf("bytes delivered to client:\n%q\nwant:\n%q", got.toClient, wire)
	}
	if got.flow.Response == nil || got.flow.Response.Meta["status"] != "204 No Content" {
		t.Fatalf("captured final response = %+v", got.flow.Response)
	}
	if len(got.delivered) != 1 || got.delivered[0].Meta["status"] != "204 No Content" {
		t.Fatalf("response interception saw %+v, want only 204", got.delivered)
	}
}

func TestHTTP1HandleFailsWhenFinalResponseIsMissing(t *testing.T) {
	interim := "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\n"
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "EOF", suffix: ""},
		{name: "malformed response", suffix: "not-an-http-response\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runHTTP1Handle(interim + tc.suffix)

			if got.err == nil {
				t.Fatal("Handle returned nil error without a final response")
			}
			if got.flow.Status != capture.StatusError || got.flow.Err == nil {
				t.Fatalf("flow status/error = %s/%v, want error", got.flow.Status, got.flow.Err)
			}
			if got.flow.Response != nil {
				t.Fatalf("captured response = %+v, want nil", got.flow.Response)
			}
			if len(got.delivered) != 0 {
				t.Fatalf("response interception saw %+v, want none", got.delivered)
			}
			if got.toClient != interim {
				t.Fatalf("bytes delivered to client = %q, want relayed interim %q", got.toClient, interim)
			}
		})
	}
}

func TestHTTP1HandleTreatsSwitchingProtocolsAsTerminal(t *testing.T) {
	const (
		clientProtocolBytes = "from-client-after-upgrade"
		serverProtocolBytes = "from-server-after-upgrade"
	)
	response := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n"
	got := runHTTP1HandleWith(response+serverProtocolBytes, clientProtocolBytes, 0)

	if got.err != nil {
		t.Fatalf("Handle: %v", got.err)
	}
	if got.toClient != response+serverProtocolBytes {
		t.Fatalf("bytes delivered to client = %q, want handshake and switched-protocol bytes %q", got.toClient, response+serverProtocolBytes)
	}
	if !strings.HasSuffix(got.toServer, clientProtocolBytes) {
		t.Fatalf("bytes delivered to server = %q, want switched-protocol suffix %q", got.toServer, clientProtocolBytes)
	}
	if got.flow.Status != capture.StatusComplete {
		t.Errorf("flow status = %s, want done", got.flow.Status)
	}
	if got.flow.Response == nil || got.flow.Response.Meta["status"] != "101 Switching Protocols" {
		t.Fatalf("captured terminal response = %+v", got.flow.Response)
	}
	if len(got.delivered) != 1 || got.delivered[0] != got.flow.Response {
		t.Fatalf("response interception saw %+v, want terminal 101", got.delivered)
	}
}

func TestHTTP1HandleConnectionEndsUpgradeWhenUpstreamCloses(t *testing.T) {
	const (
		response = "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n"
		payload  = "upstream-protocol-data"
	)

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
			_, err = io.WriteString(serverPeer, response+payload)
		}
		_ = serverPeer.Close()
		originDone <- err
	}()

	flow := capture.NewFlow("client", "example.test:80")
	tamper := &recordingHTTP1Tamper{}
	handleDone := make(chan error, 1)
	go func() {
		client := bufio.NewReadWriter(bufio.NewReader(clientConn), bufio.NewWriter(clientConn))
		server := bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
		handleDone <- (HTTP1{}).HandleConnection(flow, client, server, clientConn, serverConn, tamper, func() {})
	}()

	if _, err := io.WriteString(clientPeer, "GET /resource HTTP/1.1\r\nHost: example.test\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	got, err := io.ReadAll(clientPeer)
	if err != nil {
		t.Fatalf("client did not observe clean EOF after upstream closed: %v", err)
	}
	if string(got) != response+payload {
		t.Fatalf("client received %q, want %q", got, response+payload)
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
}

func TestHTTP1HandleFailsOnInvalidFinalResponseBodyAfterInformational(t *testing.T) {
	interim := "HTTP/1.1 103 Early Hints\r\n\r\n"
	for _, tc := range []struct {
		name  string
		final string
	}{
		{name: "truncated content length", final: "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhi"},
		{name: "malformed chunk", final: "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runHTTP1Handle(interim + tc.final)

			if got.err == nil {
				t.Fatal("Handle returned nil error for invalid final body")
			}
			if got.flow.Status != capture.StatusError || got.flow.Err == nil {
				t.Fatalf("flow status/error = %s/%v, want error", got.flow.Status, got.flow.Err)
			}
			if got.flow.Response != nil {
				t.Fatalf("captured response = %+v, want nil", got.flow.Response)
			}
			if got.toClient != interim {
				t.Fatalf("bytes delivered to client = %q, want only interim %q", got.toClient, interim)
			}
		})
	}
}

func TestHTTP1HandleRecordsDownstreamFinalResponseFailures(t *testing.T) {
	interim := "HTTP/1.1 103 Early Hints\r\n\r\n"
	for _, tc := range []struct {
		name  string
		final string
	}{
		{name: "flush", final: "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"},
		{name: "write", final: "HTTP/1.1 200 OK\r\nContent-Length: 5000\r\n\r\n" + strings.Repeat("x", 5000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The interim flush is the first underlying write; reject the
			// final response on the second.
			got := runHTTP1HandleWith(interim+tc.final, "", 2)

			if got.err == nil {
				t.Fatal("Handle returned nil downstream error")
			}
			if got.flow.Status != capture.StatusError || got.flow.Err == nil {
				t.Fatalf("flow status/error = %s/%v, want error", got.flow.Status, got.flow.Err)
			}
			if got.flow.Response == nil || got.flow.Response.Meta["status"] != "200 OK" {
				t.Fatalf("captured final response = %+v", got.flow.Response)
			}
			if got.toClient != interim {
				t.Fatalf("successfully written bytes = %q, want only interim %q", got.toClient, interim)
			}
		})
	}
}

func TestHTTP1HandleOrdinaryFinalResponseIsUnchanged(t *testing.T) {
	wire := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"
	got := runHTTP1Handle(wire)

	if got.err != nil {
		t.Fatalf("Handle: %v", got.err)
	}
	if got.toClient != wire {
		t.Fatalf("bytes delivered to client = %q, want %q", got.toClient, wire)
	}
	if got.flow.Response == nil || got.flow.Response.Meta["status"] != "200 OK" || string(got.flow.Response.Body) != "ok" {
		t.Fatalf("captured response = %+v", got.flow.Response)
	}
	if len(got.delivered) != 1 {
		t.Fatalf("response interception calls = %d, want 1", len(got.delivered))
	}
	if !strings.HasPrefix(got.toServer, "GET /resource HTTP/1.1\r\n") {
		t.Fatalf("forwarded request = %q", got.toServer)
	}
}

var _ Tamperer = (*recordingHTTP1Tamper)(nil)

func TestBoundedHTTP1HeadRejectsOversizedStartAndHeaderLines(t *testing.T) {
	tests := []struct {
		name string
		wire string
	}{
		{
			name: "start line",
			wire: "GET /" + strings.Repeat("x", maxHTTP1StartLineBytes) + " HTTP/1.1\r\n\r\n",
		},
		{
			name: "header section",
			wire: "GET / HTTP/1.1\r\nX-Test: " + strings.Repeat("x", maxHTTP1HeaderBytes) + "\r\n\r\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := boundedHTTP1Head(bufio.NewReader(strings.NewReader(tc.wire))); err == nil {
				t.Fatal("boundedHTTP1Head accepted oversized input")
			}
		})
	}
}

func TestBoundedHTTP1HeadPreservesBufferedPayload(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("GET / HTTP/1.1\r\nHost: test\r\n\r\npayload"))
	head, err := boundedHTTP1Head(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		t.Fatalf("header did not end at section boundary: %q", head)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "payload" {
		t.Fatalf("payload after header = %q, want preserved bytes", body)
	}
}
