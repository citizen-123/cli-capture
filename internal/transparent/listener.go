package transparent

import "net"

// Handler processes one redirected connection given its original destination
// ("host:port"). Typically wired to proxy.HandleTransparent.
type Handler func(conn net.Conn, originalDst string)

// Listener accepts transparently-redirected connections and resolves each one's
// original destination before dispatching.
type Listener struct {
	ln net.Listener
}

// Listen binds the transparent-proxy socket. This must be the address the
// netfilter REDIRECT rule targets.
func Listen(addr string) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln}, nil
}

func (l *Listener) Addr() string { return l.ln.Addr().String() }
func (l *Listener) Close() error { return l.ln.Close() }

// Serve accepts connections until the listener is closed, dispatching each to h
// with its recovered original destination. Connections without SO_ORIGINAL_DST
// (i.e. not actually redirected) are dropped.
func (l *Listener) Serve(h Handler) {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			tcp, ok := c.(*net.TCPConn)
			if !ok {
				c.Close()
				return
			}
			dst, err := OriginalDst(tcp)
			if err != nil {
				c.Close()
				return
			}
			h(c, dst)
		}(conn)
	}
}
