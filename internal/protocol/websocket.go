package protocol

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
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
	flow           *capture.Flow
	touch          func()
}

// writeFrame records a delivered data frame before releasing its destination's
// write lock. Notifications run afterward so observers can safely inject.
func (s *wsSession) writeFrame(dir capture.Direction, b []byte, msg *capture.Message) error {
	dst, mu := s.server, &s.smu
	if dir == capture.ServerToClient {
		dst, mu = s.client, &s.cmu
	}
	mu.Lock()
	n, err := dst.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = dst.Flush()
	}
	if err == nil && msg != nil {
		s.flow.AddMessage(msg)
	}
	mu.Unlock()
	if err == nil && msg != nil && s.touch != nil {
		s.touch()
	}
	return err
}

// Inject writes a new frame into the live session. Client→server frames are
// masked (as a real client's are); server→client frames are not.
func (s *wsSession) Inject(dir capture.Direction, opcode byte, payload []byte) error {
	// The capture owns its payload after Inject returns.
	fr := &wsFrame{Fin: true, Opcode: opcode, Payload: bytes.Clone(payload)}
	if dir == capture.ClientToServer {
		fr.Masked = true
		if _, err := rand.Read(fr.MaskKey[:]); err != nil {
			return err
		}
	}
	msg := wsMessage(dir, opcode, fr.Payload)
	msg.Meta["injected"] = "true"
	return s.writeFrame(dir, fr.encode(), msg)
}

func wsMessage(dir capture.Direction, opcode byte, payload []byte) *capture.Message {
	return &capture.Message{
		Direction: dir,
		Timestamp: time.Now(),
		Summary:   dir.String() + " ws:" + wsOpcodeName(opcode) + " " + humanBytes(len(payload)),
		Body:      payload,
		Raw:       payload,
		Meta:      map[string]string{"opcode": wsOpcodeName(opcode)},
	}
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
// capturing every data frame and offering it to the directional tamper hook.
// Called from HTTP1.Handle when the request is an upgrade.
func handleWebSocket(
	f *capture.Flow,
	req *http.Request,
	client, server *bufio.ReadWriter,
	tamper Tamperer,
	touch func(),
	closeUpgradeConnections func(),
) error {
	f.Protocol = capture.ProtoWebSocket
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

	statusLine, err := server.Reader.ReadString('\n')
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}
	if parseStatusCode(statusLine) != http.StatusSwitchingProtocols {
		return relayWebSocketRejection(f, req, statusLine, server, client, touch)
	}
	if err := relaySwitchingProtocols(statusLine, server, client); err != nil {
		return failHTTP1Response(f, err, touch)
	}

	session := &wsSession{client: client, server: server, flow: f, touch: touch}
	// Register the live session for frame injection if the tamper supports it.
	if reg, ok := tamper.(capture.SessionRegistrar); ok {
		reg.RegisterSession(f.ID, session)
		defer reg.UnregisterSession(f.ID)
	}

	done := make(chan struct{}, 2)
	go func() {
		pumpWS(session, client, f, capture.ClientToServer, tamper, touch)
		done <- struct{}{}
	}()
	go func() {
		pumpWS(session, server, f, capture.ServerToClient, tamper, touch)
		done <- struct{}{}
	}()
	<-done
	if closeUpgradeConnections != nil {
		// An upgraded connection ends when either peer closes. Interrupt the
		// opposite pump so a silent peer cannot keep the Flow active forever.
		closeUpgradeConnections()
	}
	<-done

	if f.Status == capture.StatusActive || f.Status == capture.StatusPending {
		f.Status = capture.StatusComplete
	}
	touch()
	return nil
}

// relaySwitchingProtocols copies a successful handshake's status line and
// headers to the client verbatim, stopping exactly at the blank line so no
// following frame bytes are consumed into the wrong buffer.
func relaySwitchingProtocols(statusLine string, server, client *bufio.ReadWriter) error {
	if _, err := client.Write([]byte(statusLine)); err != nil {
		return err
	}
	for {
		line, err := server.Reader.ReadString('\n')
		if err != nil {
			return err
		}
		if _, err := client.Write([]byte(line)); err != nil {
			return err
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return client.Flush()
		}
	}
}

// relayWebSocketRejection parses and drains a rejected upgrade as a normal HTTP
// response before exposing any of it to the client. net/http owns all message
// framing here, including fixed-length, chunked, and close-delimited bodies.
func relayWebSocketRejection(
	f *capture.Flow,
	req *http.Request,
	statusLine string,
	server, client *bufio.ReadWriter,
	touch func(),
) error {
	responseReader := bufio.NewReader(io.MultiReader(strings.NewReader(statusLine), server.Reader))
	resp, err := http.ReadResponse(responseReader, req)
	if err != nil {
		return failHTTP1Response(f, err, touch)
	}

	rawResp, err := httputil.DumpResponse(resp, true)
	if err != nil {
		_ = resp.Body.Close()
		return failHTTP1Response(f, err, touch)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return failHTTP1Response(f, err, touch)
	}
	if err := resp.Body.Close(); err != nil {
		return failHTTP1Response(f, err, touch)
	}

	decodedBody := capture.DecodeContentEncoding(body, strings.Join(resp.Header.Values("Content-Encoding"), ","))
	f.Response = &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes)", resp.Status, len(decodedBody)),
		Headers:   resp.Header,
		Body:      decodedBody,
		Raw:       rawResp,
		Meta:      map[string]string{"status": resp.Status},
	}
	touch()

	if _, err := client.Write(rawResp); err != nil {
		return failHTTP1Response(f, err, touch)
	}
	if err := client.Flush(); err != nil {
		return failHTTP1Response(f, err, touch)
	}

	f.Status = capture.StatusComplete
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

// pumpWS reads frames from src and tampers data frames in their own direction.
// Captured payloads remain original, including dropped or undeliverable frames.
// Control frames pass through untouched; a close ends the pump.
func pumpWS(s *wsSession, src *bufio.ReadWriter, f *capture.Flow, dir capture.Direction, tamper Tamperer, touch func()) {
	for {
		fr, err := readWSFrame(src.Reader)
		if err != nil {
			return
		}
		var msg *capture.Message
		if isWSData(fr.Opcode) {
			msg = wsMessage(dir, fr.Opcode, fr.Payload)
			if tamper != nil {
				hook := tamper.BeforeForward
				if dir == capture.ServerToClient {
					hook = tamper.BeforeDeliver
				}
				out, drop := hook(f, msg)
				if drop {
					f.AddMessage(msg)
					touch()
					continue
				}
				if out != nil {
					fr.Payload = out
				}
			}
		}
		// Append with the write, not before a pause: an injection can overtake a
		// paused frame and must also precede it in the same direction's history.
		if err := s.writeFrame(dir, fr.encode(), msg); err != nil {
			if msg != nil {
				f.AddMessage(msg)
				touch()
			}
			return
		}
		if fr.Opcode == opClose {
			return
		}
	}
}
