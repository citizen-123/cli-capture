package protocol

import (
	"bufio"
	"sync"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// rawTCP is the always-matches fallback: it pumps bytes both ways and records
// them as opaque Messages. It gives "all protocols" a floor — anything we can't
// parse is still captured and forwarded, just without structure. Real parsers
// (HTTP/2, gRPC, WebSocket) register ahead of it and win Detect.
type rawTCP struct{}

func (rawTCP) Name() capture.Protocol { return capture.ProtoRawTCP }

// Detect never claims the stream by content; Detect() in the registry falls
// back to rawTCP explicitly, so this returning false is correct.
func (rawTCP) Detect(peek []byte) bool { return false }

func (rawTCP) Handle(f *capture.Flow, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error {
	f.Protocol = capture.ProtoRawTCP

	var wg sync.WaitGroup
	wg.Add(2)

	// client → server (tamperable, chunk by chunk)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := client.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				msg := &capture.Message{
					Direction: capture.ClientToServer,
					Timestamp: time.Now(),
					Summary:   humanBytes(n) + " →",
					Body:      chunk,
					Raw:       chunk,
				}
				f.AddMessage(msg)
				out, drop := tamper.BeforeForward(f, msg)
				if !drop {
					if out == nil {
						out = msg.Body
					}
					server.Write(out)
					server.Flush()
				}
				touch()
			}
			if err != nil {
				return
			}
		}
	}()

	// server → client (observed only)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				f.AddMessage(&capture.Message{
					Direction: capture.ServerToClient,
					Timestamp: time.Now(),
					Summary:   "← " + humanBytes(n),
					Body:      append([]byte(nil), buf[:n]...),
				})
				client.Write(buf[:n])
				client.Flush()
				touch()
			}
			if err != nil {
				return
			}
		}
	}()

	wg.Wait()
	if f.Status == capture.StatusActive {
		f.Status = capture.StatusComplete
	}
	touch()
	return nil
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return itoa(n) + " B"
	case n < 1024*1024:
		return itoa(n/1024) + " KB"
	default:
		return itoa(n/(1024*1024)) + " MB"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
