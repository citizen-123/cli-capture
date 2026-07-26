package protocol

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"

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
