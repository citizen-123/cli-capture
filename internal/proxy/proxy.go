// Package proxy is the transport-capture engine. It runs an HTTP proxy that a
// child process is pointed at (via HTTP_PROXY/HTTPS_PROXY). Plain requests are
// forwarded and captured; CONNECT requests are TLS-terminated with an on-the-fly
// leaf cert (man-in-the-middle) so HTTPS traffic is visible too. Once a
// decrypted, bidirectional byte stream exists, it hands off to the protocol
// registry — the engine itself is protocol-agnostic.
package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/scope"
)

type Proxy struct {
	ca     *ca.CA
	store  *capture.Store
	tamper protocol.Tamperer
	mitm   *scope.Set // policy: which TLS hosts to intercept vs. pass through
	ln     net.Listener

	// upstreamTLS builds the TLS config used to dial the real server for a given
	// SNI. Overridable (see SetUpstreamTLS) mainly so tests can trust a test
	// server's certificate; nil means "verify normally against system roots".
	upstreamTLS func(sni string) *tls.Config
}

func New(store *capture.Store, authority *ca.CA, tamper protocol.Tamperer) *Proxy {
	return &Proxy{store: store, ca: authority, tamper: tamper}
}

// SetUpstreamTLS overrides how the proxy dials upstream servers (both HTTP/1 and
// HTTP/2). Primarily for tests that need to trust a self-signed test server.
func (p *Proxy) SetUpstreamTLS(fn func(sni string) *tls.Config) { p.upstreamTLS = fn }

func (p *Proxy) upstreamConfig(sni string) *tls.Config {
	if p.upstreamTLS != nil {
		return p.upstreamTLS(sni)
	}
	return &tls.Config{ServerName: sni}
}

// SetMITMPolicy sets which TLS hosts get man-in-the-middled. A nil policy (the
// default) MITMs every host. In-scope ⇒ intercept; out-of-scope ⇒ blind tunnel
// (the encrypted bytes are forwarded untouched and only counted). The decision
// is made from the CONNECT host, the only identity known before the TLS
// handshake — SNI-based policy would require peeking the ClientHello first.
func (p *Proxy) SetMITMPolicy(s *scope.Set) { p.mitm = s }

func (p *Proxy) shouldMITM(host string) bool {
	if p.mitm == nil {
		return true
	}
	return p.mitm.InScope(scope.Target{Host: host, Protocol: "tls"})
}

// Listen binds the proxy socket. Pass "127.0.0.1:0" to get an ephemeral port,
// then read the chosen address from Addr().
func (p *Proxy) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.ln = ln
	return nil
}

// Addr is the bound address ("127.0.0.1:54321"), for building the child's
// HTTP_PROXY value.
func (p *Proxy) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// Serve runs the accept loop until the listener is closed.
func (p *Proxy) Serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *Proxy) Close() error {
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *Proxy) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	peek, err := br.Peek(8)
	if err != nil {
		return
	}
	if strings.HasPrefix(string(peek), "CONNECT ") {
		p.handleConnect(conn, br)
		return
	}
	p.handlePlain(conn, br)
}

// handleConnect implements HTTPS interception: acknowledge the tunnel, present
// a MITM leaf cert to the client, dial the real upstream over TLS, then bridge.
func (p *Proxy) handleConnect(conn net.Conn, br *bufio.Reader) {
	line, err := readRequestLine(br)
	if err != nil {
		return
	}
	// "CONNECT host:port HTTP/1.1"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	target := fields[1] // host:port
	host := hostOnly(target)

	// Drain the remaining CONNECT headers up to the blank line.
	if err := drainHeaders(br); err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	p.interceptTLS(conn, target, host)
}

// interceptTLS runs the man-in-the-middle (or blind passthrough) for a TLS
// connection whose real destination is target ("host:port"). It is the shared
// core of both the CONNECT proxy and transparent mode: everything from the MITM
// decision through TLS termination, the h2/h1 split, and the upstream dial.
func (p *Proxy) interceptTLS(client net.Conn, target, host string) {
	// Policy: out-of-scope TLS hosts are tunneled blind (never decrypted).
	if !p.shouldMITM(host) {
		p.passthrough(client, target, host)
		return
	}

	// Terminate TLS toward the client using a cert minted for the SNI it asks
	// for. This is the man-in-the-middle: the client believes it reached host.
	// Advertising h2 via ALPN lets HTTP/2 (and thus gRPC) clients negotiate it
	// with us instead of silently downgrading.
	clientTLS := tls.Server(client, &tls.Config{
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.ca.LeafFor(name)
		},
	})
	if err := clientTLS.Handshake(); err != nil {
		return
	}
	sni := clientTLS.ConnectionState().ServerName
	if sni == "" {
		sni = host
	}

	// HTTP/2 takes a completely different path (framed, multiplexed) handled by
	// Go's h2 stack; HTTP/1.x and WebSocket stay on the byte-level bridge.
	if clientTLS.ConnectionState().NegotiatedProtocol == "h2" {
		p.serveH2(clientTLS, target, sni)
		return
	}

	// Dial the real upstream and verify its certificate normally — we MITM the
	// client, but we are still a correct TLS client to the server.
	upstream, err := tls.Dial("tcp", target, p.upstreamConfig(sni))
	if err != nil {
		return
	}
	defer upstream.Close()

	flow := capture.NewFlow(client.RemoteAddr().String(), target)
	flow.SNI = sni
	flow.Secure = true
	p.bridge(flow, clientTLS, upstream)
}

// HandleTransparent processes a connection captured by transparent redirection,
// whose original destination is target ("host:port"). It sniffs the first byte
// to tell TLS from plaintext and routes into the same pipeline as CONNECT — so
// transparent mode inherits MITM, scope, h2/gRPC, and interception for free.
func (p *Proxy) HandleTransparent(conn net.Conn, target string) {
	defer conn.Close()
	host := hostOnly(target)
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x16 { // TLS handshake record type
		p.interceptTLS(rw(conn, br), target, host)
		return
	}
	// Plaintext: dial the origin and bridge (HTTP/1, WebSocket, or raw TCP).
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	flow := capture.NewFlow(conn.RemoteAddr().String(), target)
	p.bridge(flow, rw(conn, br), upstream)
}

// handlePlain forwards a non-CONNECT (plaintext HTTP) proxy request. NOTE: for
// v1 this forwards the request in the absolute-form the client sent, which most
// origin servers accept; strict origin-form rewriting is a documented follow-up.
func (p *Proxy) handlePlain(conn net.Conn, br *bufio.Reader) {
	line, err := peekRequestLine(br)
	if err != nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	u, err := url.Parse(fields[1])
	if err != nil || u.Host == "" {
		return
	}
	target := u.Host
	if !strings.Contains(target, ":") {
		target += ":80"
	}

	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()

	flow := capture.NewFlow(conn.RemoteAddr().String(), target)
	p.bridge(flow, rw(conn, br), upstream)
}

// passthrough tunnels an out-of-scope TLS connection without decrypting it: the
// encrypted bytes flow both ways untouched and are only counted, so the flow
// still shows up in the traffic pane as opaque.
func (p *Proxy) passthrough(client net.Conn, target, host string) {
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()

	flow := capture.NewFlow(client.RemoteAddr().String(), target)
	flow.SNI = host
	flow.Secure = true
	flow.Protocol = capture.ProtoRawTCP
	p.store.Add(flow)
	touch := func() { p.store.Touch(flow) }

	up := &capture.Message{Direction: capture.ClientToServer, Timestamp: time.Now(), Summary: "→ 0 B (passthrough)"}
	down := &capture.Message{Direction: capture.ServerToClient, Timestamp: time.Now(), Summary: "← 0 B (passthrough)"}
	flow.Messages = []*capture.Message{up, down}

	var wg sync.WaitGroup
	wg.Add(2)
	pump := func(dst, src net.Conn, m *capture.Message, arrow string) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		var total int
		for {
			n, err := src.Read(buf)
			if n > 0 {
				dst.Write(buf[:n])
				total += n
				m.Summary = fmt.Sprintf("%s %s (passthrough)", arrow, humanBytes(total))
				touch()
			}
			if err != nil {
				return
			}
		}
	}
	go pump(upstream, client, up, "→")
	pump(client, upstream, down, "←")
	wg.Wait()

	flow.Status = capture.StatusComplete
	touch()
}

// bridge is the shared hand-off: detect the protocol from the first client
// bytes, register the flow, and let the protocol drive both directions.
func (p *Proxy) bridge(flow *capture.Flow, client, server net.Conn) {
	cbr := asReader(client)
	proto := detectFrom(cbr)

	clientRW := bufio.NewReadWriter(cbr, bufio.NewWriter(client))
	serverRW := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))

	p.store.Add(flow)
	touch := func() { p.store.Touch(flow) }

	if err := proto.Handle(flow, clientRW, serverRW, p.tamper, touch); err != nil {
		log.Printf("proxy: flow %s handle error: %v", flow.ID, err)
	}
}

// --- small helpers ---------------------------------------------------------

// asReader returns the *bufio.Reader for a conn, reusing the one we may have
// already wrapped (via rw) so buffered/peeked bytes are not lost.
func asReader(c net.Conn) *bufio.Reader {
	if w, ok := c.(*peekConn); ok {
		return w.br
	}
	return bufio.NewReader(c)
}

func detectFrom(br *bufio.Reader) protocol.Protocol {
	peek, _ := br.Peek(16)
	return protocol.Detect(peek)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func readRequestLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// peekRequestLine returns the first line without consuming it. It must never
// ask bufio for more bytes than are already buffered — Peek blocks until its
// count is satisfied, and after sending a request the client is waiting on us,
// so demanding unsent bytes would deadlock. We scan what a Read already
// delivered (a small localhost request arrives in one segment) and grow only up
// to the buffer size.
func peekRequestLine(br *bufio.Reader) (string, error) {
	if _, err := br.Peek(1); err != nil {
		return "", err
	}
	for n := br.Buffered(); n <= 4096; n++ {
		buf, err := br.Peek(n)
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			return strings.TrimRight(string(buf[:i]), "\r"), nil
		}
		if err != nil {
			break
		}
	}
	return "", fmt.Errorf("proxy: no request line in buffered data")
}

func drainHeaders(br *bufio.Reader) error {
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return nil
		}
	}
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
