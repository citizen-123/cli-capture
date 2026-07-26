package proxy

import (
	"bufio"
	"net"
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
