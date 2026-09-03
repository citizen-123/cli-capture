package protocol

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// wsSession funnels every write to a WebSocket connection through a per-peer
// mutex, so the two pump goroutines and any injected frame never interleave and
// corrupt the stream. It implements capture.Injector.
type wsSession struct {
	client, server *bufio.ReadWriter
	cmu, smu       sync.Mutex
}

func (s *wsSession) writeToClient(b []byte) error {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if _, err := s.client.Write(b); err != nil {
		return err
	}
	return s.client.Flush()
}

func (s *wsSession) writeToServer(b []byte) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	if _, err := s.server.Write(b); err != nil {
		return err
	}
	return s.server.Flush()
}

// Inject writes a new frame into the live session. Client→server frames are
// masked (as a real client's are); server→client frames are not.
func (s *wsSession) Inject(dir capture.Direction, opcode byte, payload []byte) error {
	if len(payload) > capture.MaxFrameBytes {
		return fmt.Errorf("WebSocket frame exceeds %d-byte limit", capture.MaxFrameBytes)
	}
	if !validWSOpcode(opcode) || opcode == opContinuation {
		return fmt.Errorf("invalid injectable WebSocket opcode %#x", opcode)
	}
	if isWSControl(opcode) && len(payload) > 125 {
		return fmt.Errorf("oversized injectable WebSocket control frame")
	}
	fr := &wsFrame{Fin: true, Opcode: opcode, Payload: payload}
	if dir == capture.ClientToServer {
		fr.Masked = true
		if _, err := rand.Read(fr.MaskKey[:]); err != nil {
			return err
		}
		return s.writeToServer(fr.encode())
	}
	return s.writeToClient(fr.encode())
}

// isWebSocketUpgrade reports whether an HTTP request is a WebSocket handshake.
// WebSocket is an HTTP/1.1 upgrade, so this is detected inside the HTTP handler
// rather than by the protocol registry (the 16-byte Detect peek can't see the
// Upgrade header).
func isWebSocketUpgrade(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") &&
		headerHasToken(req.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// handleWebSocket relays the upgrade handshake, then pumps frames both ways —
// capturing every data frame and, for client→server frames, offering them to
// the tamper hook. Called from HTTP1.Handle when the request is an upgrade.
func handleWebSocket(
	f *capture.Flow,
	req *http.Request,
	client, server *bufio.ReadWriter,
	tamper Tamperer,
	touch func(),
	closeUpgradeConnections func(),
) error {
	f.Mutate(func() {
		f.Protocol = capture.ProtoWebSocket
	})
	defer cancelPendingPauses(tamper, f)
	touch()

	// Refuse compression so frames stay plaintext and thus inspectable/editable.
	// Without this, a negotiated permessage-deflate would gzip every payload.
	req.Header.Del("Sec-WebSocket-Extensions")

	if err := req.Write(server); err != nil {
		return err
	}
	if err := server.Flush(); err != nil {
		return err
	}

	handshake, err := boundedHTTP1Head(server.Reader)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	statusLine := string(handshake[:bytes.IndexByte(handshake, '\n')+1])
	if parseStatusCode(statusLine) != http.StatusSwitchingProtocols {
		return relayWebSocketRejection(f, req, handshake, server, client, touch)
	}
	if _, err := client.Write(handshake); err != nil {
		return failHTTP1Response(f, err, touch)
	}
	if err := client.Flush(); err != nil {
		return failHTTP1Response(f, err, touch)
	}

	session := &wsSession{client: client, server: server}
	// Register the live session for frame injection if the tamper supports it.
	if reg, ok := tamper.(capture.SessionRegistrar); ok {
		reg.RegisterSession(f.ID, session)
		defer reg.UnregisterSession(f.ID)
	}

	results := make(chan error, 2)
	go func() {
		results <- pumpWS(session, client, f, capture.ClientToServer, tamper, touch)
	}()
	go func() {
		results <- pumpWS(session, server, f, capture.ServerToClient, tamper, touch)
	}()

	first := <-results
	cancelPendingPauses(tamper, f)
	if closeUpgradeConnections != nil {
		// An upgraded connection ends when either peer closes or its parser
		// fails. Interrupt the opposite pump so it cannot keep the Flow active
		// after an invalid frame or an upstream error.
		closeUpgradeConnections()
	}
	second := <-results
	if first != nil {
		f.Mutate(func() {
			f.Status = capture.StatusError
			f.Err = first
		})
		touch()
		return first
	}
	if second != nil && closeUpgradeConnections == nil {
		f.Mutate(func() {
			f.Status = capture.StatusError
			f.Err = second
		})
		touch()
		return second
	}

	f.Mutate(func() {
		if f.Status == capture.StatusActive || f.Status == capture.StatusPending {
			f.Status = capture.StatusComplete
		}
	})
	touch()
	return nil
}

func relayWebSocketRejection(
	f *capture.Flow,
	req *http.Request,
	handshake []byte,
	server, client *bufio.ReadWriter,
	touch func(),
) error {
	responseReader := bufio.NewReader(io.MultiReader(bytes.NewReader(handshake), server.Reader))
	resp, err := http.ReadResponse(responseReader, req)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	server.Reader = responseReader

	rawResp, body, unavailable, err := captureHTTP1Response(resp)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	respMsg := &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes)", resp.Status, len(body)),
		Headers:   resp.Header,
		Raw:       rawResp,
		Meta:      map[string]string{"status": resp.Status},
	}
	if unavailable {
		respMsg.Truncated = true
		respMsg.Meta[BodyRepresentationMeta] = BodyRepresentationUnavailable
	} else {
		respMsg.Body = capture.DecodeContentEncoding(body, strings.Join(resp.Header.Values("Content-Encoding"), ","))
		respMsg.Summary = fmt.Sprintf("%s (%d bytes)", resp.Status, len(respMsg.Body))
	}
	f.Mutate(func() {
		f.Response = respMsg
	})
	touch()

	if unavailable {
		if err := resp.Write(client); err != nil {
			return failHTTP1Response(f, err, touch)
		}
	} else if _, err := client.Write(rawResp); err != nil {
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

// parseStatusCode pulls the numeric code out of "HTTP/1.1 101 Switching …".
func parseStatusCode(statusLine string) int {
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0
	}
	n := 0
	for _, r := range fields[1] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// pumpWS reads frames from src, records data frames on the flow, optionally
// tampers client→server data frames, and forwards each frame to the opposite
// peer through the session's guarded writer. Control frames pass through
// untouched; a close ends the pump.
func pumpWS(s *wsSession, src *bufio.ReadWriter, f *capture.Flow, dir capture.Direction, tamper Tamperer, touch func()) error {
	writeOut := s.writeToServer // client→server data goes to the server
	if dir == capture.ServerToClient {
		writeOut = s.writeToClient
	}
	for {
		fr, err := readWSFrame(src.Reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if dir == capture.ClientToServer && !fr.Masked {
			return fmt.Errorf("unmasked client WebSocket frame")
		}
		if dir == capture.ServerToClient && fr.Masked {
			return fmt.Errorf("masked server WebSocket frame")
		}
		if isWSData(fr.Opcode) {
			msg := &capture.Message{
				Direction: dir,
				Timestamp: time.Now(),
				Summary:   dir.String() + " ws:" + wsOpcodeName(fr.Opcode) + " " + humanBytes(len(fr.Payload)),
				Body:      fr.Payload,
				Raw:       fr.Payload,
				Meta:      map[string]string{"opcode": wsOpcodeName(fr.Opcode)},
			}
			f.AddMessage(msg)
			if tamper != nil {
				var out []byte
				var drop bool
				if dir == capture.ClientToServer {
					out, drop = tamper.BeforeForward(f, msg)
				} else {
					out, drop = tamper.BeforeDeliver(f, msg)
				}
				if drop {
					touch()
					continue // skip this frame entirely
				}
				if out != nil {
					if len(out) > capture.MaxFrameBytes {
						return fmt.Errorf("edited WebSocket frame exceeds %d-byte limit", capture.MaxFrameBytes)
					}
					fr.Payload = out
				}
			}
			touch()
		}
		if err := writeOut(fr.encode()); err != nil {
			return err
		}
		if fr.Opcode == opClose {
			return nil
		}
	}
}
