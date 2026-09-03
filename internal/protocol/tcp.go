package protocol

import (
	"bufio"
	"fmt"
	"io"
	"net"
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

func (r rawTCP) Handle(f *capture.Flow, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error {
	return r.handle(f, client, server, nil, nil, tamper, touch)
}

// HandleConnection gives the fallback relay ownership of both sockets. EOF is
// propagated as a directional half-close; only a real relay error tears both
// sockets down to unblock the other direction.
func (r rawTCP) HandleConnection(
	f *capture.Flow,
	client, server *bufio.ReadWriter,
	clientConn, serverConn net.Conn,
	tamper Tamperer,
	touch func(),
) error {
	return r.handle(f, client, server, clientConn, serverConn, tamper, touch)
}

func (rawTCP) handle(
	f *capture.Flow,
	client, server *bufio.ReadWriter,
	clientConn, serverConn net.Conn,
	tamper Tamperer,
	touch func(),
) error {
	f.Mutate(func() {
		f.Protocol = capture.ProtoRawTCP
	})
	defer cancelPendingPauses(tamper, f)

	results := make(chan error, 2)
	go func() { results <- pumpRawClient(f, client, server, serverConn, tamper, touch) }()
	go func() { results <- pumpRawServer(f, server, client, clientConn, tamper, touch) }()

	first := <-results
	if first != nil {
		cancelPendingPauses(tamper, f)
		closeRawConnections(clientConn, serverConn)
		<-results
		f.Mutate(func() {
			f.Status = capture.StatusError
			f.Err = first
		})
		touch()
		return first
	}
	second := <-results
	if second != nil {
		closeRawConnections(clientConn, serverConn)
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

func closeRawConnections(clientConn, serverConn net.Conn) {
	deadline := time.Now().Add(time.Second)
	for _, conn := range []net.Conn{clientConn, serverConn} {
		if conn == nil {
			continue
		}
		_ = conn.SetDeadline(deadline)
		_ = conn.Close()
	}
}

func closeWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func pumpRawClient(
	f *capture.Flow,
	client, server *bufio.ReadWriter,
	serverConn net.Conn,
	tamper Tamperer,
	touch func(),
) error {
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
			var out []byte
			drop := false
			if tamper != nil {
				out, drop = tamper.BeforeForward(f, msg)
			}
			if !drop {
				if out == nil {
					out = msg.Body
				}
				if len(out) > capture.MaxFrameBytes {
					return fmt.Errorf("edited TCP chunk exceeds %d-byte limit", capture.MaxFrameBytes)
				}
				if _, writeErr := server.Write(out); writeErr != nil {
					return writeErr
				}
				if flushErr := server.Flush(); flushErr != nil {
					return flushErr
				}
			}
			touch()
		}
		if err != nil {
			if err == io.EOF {
				closeWrite(serverConn)
				return nil
			}
			return err
		}
	}
}

func pumpRawServer(
	f *capture.Flow,
	server, client *bufio.ReadWriter,
	clientConn net.Conn,
	tamper Tamperer,
	touch func(),
) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := server.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			msg := &capture.Message{
				Direction: capture.ServerToClient,
				Timestamp: time.Now(),
				Summary:   "← " + humanBytes(n),
				Body:      chunk,
				Raw:       chunk,
			}
			f.AddMessage(msg)
			var out []byte
			drop := false
			if tamper != nil {
				out, drop = tamper.BeforeDeliver(f, msg)
			}
			if !drop {
				if out == nil {
					out = msg.Body
				}
				if len(out) > capture.MaxFrameBytes {
					return fmt.Errorf("edited TCP chunk exceeds %d-byte limit", capture.MaxFrameBytes)
				}
				if _, writeErr := client.Write(out); writeErr != nil {
					return writeErr
				}
				if flushErr := client.Flush(); flushErr != nil {
					return flushErr
				}
			}
			touch()
		}
		if err != nil {
			if err == io.EOF {
				closeWrite(clientConn)
				return nil
			}
			return err
		}
	}
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
