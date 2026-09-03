package protocol

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

type rawResponseTamper struct{}

func (rawResponseTamper) BeforeForward(*capture.Flow, *capture.Message) ([]byte, bool) { return nil, false }
func (rawResponseTamper) BeforeDeliver(_ *capture.Flow, msg *capture.Message) ([]byte, bool) {
	return []byte("edited:" + string(msg.Raw)), false
}
func (rawResponseTamper) ShouldInterceptRequest(*capture.Flow) bool  { return false }
func (rawResponseTamper) ShouldInterceptResponse(*capture.Flow) bool { return false }

func TestRawTCPResponseTamperEditsAndCapturesOriginal(t *testing.T) {
	input := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("reply")), bufio.NewWriter(io.Discard))
	var delivered bytes.Buffer
	output := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(&delivered))
	flow := capture.NewFlow("client", "server:443")
	if err := pumpRawServer(flow, input, output, nil, rawResponseTamper{}, func() {}); err != nil {
		t.Fatal(err)
	}
	if got := string(flow.Messages[0].Body); got != "reply" {
		t.Fatalf("captured response = %q, want original", got)
	}
	if got := delivered.String(); got != "edited:reply" {
		t.Fatalf("delivered response = %q, want tampered output", got)
	}
}

func TestRawTCPHandleConnectionPreservesHalfClose(t *testing.T) {
	clientConn, clientPeer := tcpPair(t)
	serverConn, serverPeer := tcpPair(t)
	defer clientConn.Close()
	defer clientPeer.Close()
	defer serverConn.Close()
	defer serverPeer.Close()

	flow := capture.NewFlow("client", "server:443")
	done := make(chan error, 1)
	go func() {
		client := bufio.NewReadWriter(bufio.NewReader(clientConn), bufio.NewWriter(clientConn))
		server := bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
		done <- (rawTCP{}).HandleConnection(flow, client, server, clientConn, serverConn, nil, func() {})
	}()

	upstreamRead := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(serverPeer)
		upstreamRead <- data
	}()
	if _, err := clientPeer.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := clientPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-upstreamRead:
		if string(got) != "request" {
			t.Fatalf("upstream received %q, want request", got)
		}
	case <-time.After(time.Second):
		t.Fatal("client EOF did not become an upstream half-close")
	}

	if _, err := serverPeer.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if err := serverPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(clientPeer)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "response" {
		t.Fatalf("client received %q, want response", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleConnection = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("raw relay stayed blocked after both directional half-closes")
	}
	if flow.Status != capture.StatusComplete {
		t.Fatalf("flow status = %v, want complete", flow.Status)
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err == nil {
			accepted <- conn
		}
	}()
	peer, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-accepted:
		return conn, peer
	case <-time.After(time.Second):
		_ = peer.Close()
		t.Fatal("timed out accepting TCP peer")
		return nil, nil
	}
}
