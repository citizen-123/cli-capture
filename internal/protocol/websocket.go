package protocol

import (
	"bufio"
	"crypto/rand"
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
func handleWebSocket(f *capture.Flow, req *http.Request, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error {
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

	status, err := relayHandshakeResponse(server, client)
	if err != nil {
		f.Status = capture.StatusError
		f.Err = err
		touch()
		return err
	}
	if status != http.StatusSwitchingProtocols {
		// Server declined the upgrade; there are no frames to pump.
		f.Status = capture.StatusComplete
		touch()
		return nil
	}

	session := &wsSession{client: client, server: server}
	// Register the live session for frame injection if the tamper supports it.
	if reg, ok := tamper.(capture.SessionRegistrar); ok {
		reg.RegisterSession(f.ID, session)
		defer reg.UnregisterSession(f.ID)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pumpWS(session, client, f, capture.ClientToServer, tamper, touch) }()
	go func() { defer wg.Done(); pumpWS(session, server, f, capture.ServerToClient, nil, touch) }()
	wg.Wait()

	if f.Status == capture.StatusActive || f.Status == capture.StatusPending {
		f.Status = capture.StatusComplete
	}
	touch()
	return nil
}

// relayHandshakeResponse copies the upstream handshake response's status line and
// headers to the client verbatim, stopping exactly at the blank line so no
// following frame bytes are consumed into the wrong buffer. Returns the status
// code (101 on success).
func relayHandshakeResponse(server, client *bufio.ReadWriter) (int, error) {
	status, first := 0, true
	for {
		line, err := server.Reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		if _, err := client.Write([]byte(line)); err != nil {
			return 0, err
		}
		if first {
			status = parseStatusCode(line)
			first = false
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	return status, client.Flush()
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
func pumpWS(s *wsSession, src *bufio.ReadWriter, f *capture.Flow, dir capture.Direction, tamper Tamperer, touch func()) {
	writeOut := s.writeToServer // client→server data goes to the server
	if dir == capture.ServerToClient {
		writeOut = s.writeToClient
	}
	for {
		fr, err := readWSFrame(src.Reader)
		if err != nil {
			return
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
				out, drop := tamper.BeforeForward(f, msg)
				if drop {
					touch()
					continue // skip this frame entirely
				}
				if out != nil {
					fr.Payload = out
				}
			}
			touch()
		}
		if err := writeOut(fr.encode()); err != nil {
			return
		}
		if fr.Opcode == opClose {
			return
		}
	}
}
