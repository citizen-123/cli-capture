package proxy

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// peekConn wraps a net.Conn whose leading bytes were already buffered into a
// *bufio.Reader (because we peeked the request line). Reads are served from the
// buffer first so nothing the proxy peeked is lost when the protocol handler
// takes over. Writes and everything else pass straight through.
type peekConn struct {
	net.Conn
	br *bufio.Reader
}

func rw(c net.Conn, br *bufio.Reader) *peekConn {
	return &peekConn{Conn: c, br: br}
}

func (p *peekConn) Read(b []byte) (int, error) { return p.br.Read(b) }

const (
	maxClientHelloPeek     = 128 << 10
	clientHelloPeekTimeout = 5 * time.Second
)

var (
	errClientHelloPeekLimit = errors.New("client hello exceeds peek limit")
	errClientHelloPeekDone  = errors.New("client hello captured")
)

// replayConn puts bytes consumed by a protocol probe back in front of the
// underlying connection.
type replayConn struct {
	net.Conn
	prefix []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.prefix) != 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// clientHelloProbe records a bounded prefix and suppresses writes. crypto/tls
// can therefore parse the ClientHello without consuming client bytes or
// sending the probe's deliberate handshake failure back to the client.
type clientHelloProbe struct {
	net.Conn
	prefix []byte
}

func (c *clientHelloProbe) Read(p []byte) (int, error) {
	remaining := maxClientHelloPeek - len(c.prefix)
	if remaining == 0 {
		return 0, errClientHelloPeekLimit
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := c.Conn.Read(p)
	c.prefix = append(c.prefix, p[:n]...)
	return n, err
}

func (*clientHelloProbe) Write(p []byte) (int, error) { return len(p), nil }

// sniffClientHello uses the standard library's TLS parser so its SNI behavior
// stays aligned with the TLS server that will ultimately handle the connection.
// Every byte read during the byte- and time-bounded probe is replayed to the
// next consumer.
func sniffClientHello(conn net.Conn) (serverName string, replay net.Conn, err error) {
	return sniffClientHelloWithin(conn, clientHelloPeekTimeout)
}

func sniffClientHelloWithin(conn net.Conn, timeout time.Duration) (serverName string, replay net.Conn, err error) {
	probe := &clientHelloProbe{
		Conn:   conn,
		prefix: make([]byte, 0, 4<<10),
	}
	parsed := false
	parser := tls.Server(probe, &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			parsed = true
			serverName = hello.ServerName
			return nil, errClientHelloPeekDone
		},
	})
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", conn, fmt.Errorf("set ClientHello peek deadline: %w", err)
	}
	parseErr := parser.Handshake()
	replay = &replayConn{Conn: conn, prefix: probe.prefix}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return "", replay, fmt.Errorf("clear ClientHello peek deadline: %w", err)
	}
	if !parsed {
		return "", replay, fmt.Errorf("peek TLS ClientHello: %w", parseErr)
	}
	return serverName, replay, nil
}
