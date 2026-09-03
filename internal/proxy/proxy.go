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
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/scope"
)

const (
	maxConcurrentConnections = 64
	initialPreambleTimeout   = 5 * time.Second
	tlsHandshakeTimeout      = 10 * time.Second
	maxConnectRequestLine    = 8 << 10
	maxConnectHeaderLine     = 8 << 10
	maxConnectHeaders        = 32 << 10
	idleRelayTimeout         = 2 * time.Minute
)

type Proxy struct {
	ca     *ca.CA
	store  *capture.Store
	tamper protocol.Tamperer
	mitm   *scope.Set // policy: which TLS hosts to intercept vs. pass through
	ln     net.Listener

	// connectionSlots bounds admitted peer connections, including setup, so slow
	// preambles cannot make the accept loop create unbounded goroutines.
	connectionSlots chan struct{}

	// upstreamTLS builds the TLS config used to dial the real server for a given
	// SNI. Overridable (see SetUpstreamTLS) mainly so tests can trust a test
	// server's certificate; nil means "verify normally against system roots".
	upstreamTLS func(sni string) *tls.Config
}

type storeAwareTamperer interface {
	SetStore(*capture.Store)
}

func New(store *capture.Store, authority *ca.CA, tamper protocol.Tamperer) *Proxy {
	if tamper, ok := tamper.(storeAwareTamperer); ok {
		tamper.SetStore(store)
	}
	return &Proxy{
		store:           store,
		ca:              authority,
		tamper:          tamper,
		connectionSlots: make(chan struct{}, maxConcurrentConnections),
	}
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
// (the encrypted bytes are forwarded untouched and only counted). CONNECT
// requires both its normalized authority and any effective ClientHello SNI to
// be in scope; transparent TLS uses the SNI, falling back to its destination.
func (p *Proxy) SetMITMPolicy(s *scope.Set) { p.mitm = s }

func (p *Proxy) shouldMITM(host string) bool {
	if p.mitm == nil {
		return true
	}
	return p.mitm.InScope(scope.Target{Host: host, Protocol: "tls"})
}

// ValidateListenAddress rejects proxy listeners that are not bound directly to
// a numeric loopback address. Resolving hostnames before binding is unsafe:
// their answers can change between validation and net.Listen.
func ValidateListenAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("proxy listener address %q must be host:port: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("proxy listener address %q must use a numeric loopback IP", addr)
	}
	return nil
}

// Listen binds the proxy socket. Pass "127.0.0.1:0" to get an ephemeral port,
// then read the chosen address from Addr().
func (p *Proxy) Listen(addr string) error {
	if err := ValidateListenAddress(addr); err != nil {
		return err
	}
	if p.connectionSlots == nil {
		p.connectionSlots = make(chan struct{}, maxConcurrentConnections)
	}
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
		if !p.tryAcquireConnection() {
			_ = conn.Close()
			continue
		}
		go func(conn net.Conn) {
			defer p.releaseConnection()
			p.handle(conn)
		}(conn)
	}
}

func (p *Proxy) tryAcquireConnection() bool {
	if p.connectionSlots == nil {
		return true
	}
	select {
	case p.connectionSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *Proxy) releaseConnection() {
	if p.connectionSlots != nil {
		<-p.connectionSlots
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
	if err := conn.SetDeadline(time.Now().Add(initialPreambleTimeout)); err != nil {
		return
	}
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

// handleConnect acknowledges a valid CONNECT request, then selects MITM only
// after the authority and ClientHello SNI have both passed the host policy.
func (p *Proxy) handleConnect(conn net.Conn, br *bufio.Reader) {
	line, err := readRequestLine(br)
	if err != nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != "CONNECT" || !strings.HasPrefix(fields[2], "HTTP/") {
		return
	}
	target, authority, err := normalizeConnectAuthority(fields[1])
	if err != nil {
		return
	}

	if err := drainHeaders(br); err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	p.interceptTLS(conn, target, authority, true)
}

// interceptTLS chooses a blind tunnel or MITM after it has parsed the
// ClientHello without issuing a certificate. CONNECT supplies an authority
// policy identity in addition to the effective SNI; transparent traffic uses
// the SNI when present and its original destination otherwise.
func (p *Proxy) interceptTLS(client net.Conn, target, authority string, requireAuthorityPolicy bool) {
	if err := client.SetDeadline(time.Time{}); err != nil {
		return
	}
	serverName, replay, err := sniffClientHelloWithin(client, tlsHandshakeTimeout)
	if err != nil {
		return
	}

	effectiveHost := authority
	if serverName != "" {
		effectiveHost, err = normalizeTLSHost(serverName)
		if err != nil {
			return
		}
	}
	if (requireAuthorityPolicy && !p.shouldMITM(authority)) || !p.shouldMITM(effectiveHost) {
		p.passthrough(replay, target, effectiveHost)
		return
	}

	if err := replay.SetDeadline(time.Now().Add(tlsHandshakeTimeout)); err != nil {
		return
	}
	clientTLS := tls.Server(replay, &tls.Config{
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.ca.LeafFor(effectiveHost)
		},
	})
	if err := clientTLS.Handshake(); err != nil {
		return
	}
	if err := clientTLS.SetDeadline(time.Time{}); err != nil {
		return
	}

	// HTTP/2 takes a completely different path (framed, multiplexed) handled by
	// Go's h2 stack; HTTP/1.x and WebSocket stay on the byte-level bridge.
	if clientTLS.ConnectionState().NegotiatedProtocol == "h2" {
		clientRelay, err := newIdleConn(clientTLS, idleRelayTimeout)
		if err != nil {
			return
		}
		p.serveH2(clientRelay, target, effectiveHost)
		return
	}

	// Dial the real upstream and verify its certificate normally — we MITM the
	// client, but we are still a correct TLS client to the server.
	upstream, err := p.dialTLS(target, effectiveHost)
	if err != nil {
		return
	}
	defer upstream.Close()

	flow := capture.NewFlow(client.RemoteAddr().String(), target)
	flow.SNI = effectiveHost
	flow.Secure = true
	p.bridge(flow, clientTLS, upstream, detectFrom)
}

// HandleTransparent processes a connection captured by transparent redirection,
// whose original destination is target ("host:port"). It sniffs the first byte
// to tell TLS from plaintext and routes into the same pipeline as CONNECT — so
// transparent mode inherits MITM, scope, h2/gRPC, and interception for free.
func (p *Proxy) HandleTransparent(conn net.Conn, target string) {
	if !p.tryAcquireConnection() {
		_ = conn.Close()
		return
	}
	defer p.releaseConnection()
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(initialPreambleTimeout)); err != nil {
		return
	}
	host, err := normalizeTLSHost(hostOnly(target))
	if err != nil {
		return
	}
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x16 { // TLS handshake record type
		p.interceptTLS(rw(conn, br), target, host, false)
		return
	}
	// Preserve the preamble deadline only while a partial HTTP method could
	// become a complete signature. Other plaintext traffic must not wait for a
	// speculative protocol probe.
	for {
		n := nextHTTPPrefixLength(br)
		if n == 0 {
			break
		}
		if _, err := br.Peek(n); err != nil {
			return
		}
	}

	// Plaintext: dial the origin and bridge (HTTP/1, WebSocket, or raw TCP).
	upstream, err := net.DialTimeout("tcp", target, tlsHandshakeTimeout)
	if err != nil {
		return
	}
	defer upstream.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	flow := capture.NewFlow(conn.RemoteAddr().String(), target)
	p.bridge(flow, rw(conn, br), upstream, detectFromBuffered)
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

	upstream, err := net.DialTimeout("tcp", target, tlsHandshakeTimeout)
	if err != nil {
		return
	}
	defer upstream.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}

	flow := capture.NewFlow(conn.RemoteAddr().String(), target)
	p.bridge(flow, rw(conn, br), upstream, detectFrom)
}

// passthrough tunnels an out-of-scope TLS connection without decrypting it: the
// encrypted bytes flow both ways untouched and are only counted, so the flow
// still shows up in the traffic pane as opaque.
func (p *Proxy) passthrough(client net.Conn, target, host string) {
	upstream, err := net.DialTimeout("tcp", target, tlsHandshakeTimeout)
	if err != nil {
		return
	}
	defer upstream.Close()

	clientRelay, err := newIdleConn(client, idleRelayTimeout)
	if err != nil {
		return
	}
	upstreamRelay, err := newIdleConn(upstream, idleRelayTimeout)
	if err != nil {
		return
	}

	flow := capture.NewFlow(client.RemoteAddr().String(), target)
	flow.Mutate(func() {
		flow.SNI = host
		flow.Secure = true
		flow.Protocol = capture.ProtoRawTCP
	})
	up := &capture.Message{Direction: capture.ClientToServer, Timestamp: time.Now(), Summary: "→ 0 B (passthrough)"}
	down := &capture.Message{Direction: capture.ServerToClient, Timestamp: time.Now(), Summary: "← 0 B (passthrough)"}
	flow.AddMessage(up)
	flow.AddMessage(down)
	p.store.Add(flow)
	touch := func() { p.store.Touch(flow) }

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = clientRelay.Close()
			_ = upstreamRelay.Close()
		})
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	pump := func(dst, src net.Conn, m *capture.Message, arrow string) {
		defer wg.Done()

		buf := make([]byte, 32*1024)
		var total int
		for {
			n, readErr := src.Read(buf)
			if n > 0 {
				written, writeErr := writeAll(dst, buf[:n])
				if written > 0 {
					total += written
					flow.Mutate(func() {
						m.Summary = fmt.Sprintf("%s %s (passthrough)", arrow, humanBytes(total))
					})
					touch()
				}
				if writeErr != nil {
					results <- writeErr
					return
				}
			}
			if readErr == nil {
				continue
			}
			if errors.Is(readErr, io.EOF) {
				results <- closeRelayWrite(dst)
				return
			}
			results <- readErr
			return
		}
	}
	go pump(upstreamRelay, clientRelay, up, "→")
	go pump(clientRelay, upstreamRelay, down, "←")

	first := <-results
	if first != nil {
		closeBoth()
		<-results
		wg.Wait()
		flow.Mutate(func() {
			flow.Status = capture.StatusError
			flow.Err = first
		})
		touch()
		return
	}
	second := <-results
	wg.Wait()
	if second != nil {
		closeBoth()
		flow.Mutate(func() {
			flow.Status = capture.StatusError
			flow.Err = second
		})
		touch()
		return
	}

	flow.Mutate(func() {
		flow.Status = capture.StatusComplete
	})
	touch()
}

func closeRelayWrite(conn net.Conn) error {
	if conn, ok := conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}

type connectionProtocol interface {
	HandleConnection(
		f *capture.Flow,
		client, server *bufio.ReadWriter,
		clientConn, serverConn net.Conn,
		tamper protocol.Tamperer,
		touch func(),
	) error
}

// bridge is the shared hand-off: detect the protocol from the first client
// bytes, register the flow, and let the protocol drive both directions.
func (p *Proxy) bridge(flow *capture.Flow, client, server net.Conn, detect func(*bufio.Reader) protocol.Protocol) {
	clientRelay, err := newIdleConn(client, idleRelayTimeout)
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return
	}
	serverRelay, err := newIdleConn(server, idleRelayTimeout)
	if err != nil {
		_ = clientRelay.Close()
		_ = server.Close()
		return
	}

	cbr := asReader(clientRelay)
	proto := detect(cbr)

	clientRW := bufio.NewReadWriter(cbr, bufio.NewWriter(clientRelay))
	serverRW := bufio.NewReadWriter(bufio.NewReader(serverRelay), bufio.NewWriter(serverRelay))

	p.store.Add(flow)
	touch := func() { p.store.Touch(flow) }

	var handleErr error
	if handler, ok := proto.(connectionProtocol); ok {
		handleErr = handler.HandleConnection(flow, clientRW, serverRW, clientRelay, serverRelay, p.tamper, touch)
	} else {
		handleErr = proto.Handle(flow, clientRW, serverRW, p.tamper, touch)
	}
	if handleErr != nil {
		log.Printf("proxy: flow %s handle error: %v", flow.ID, handleErr)
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

// detectFromBuffered classifies transparent plaintext from bytes bufio has
// already read. Unlike Peek(16), it cannot wait for an application payload
// whose client may be waiting for an upstream response.
func detectFromBuffered(br *bufio.Reader) protocol.Protocol {
	peek, _ := br.Peek(br.Buffered())
	return protocol.Detect(peek)
}

// nextHTTPPrefixLength returns the fewest buffered bytes needed to confirm or
// reject a partial HTTP request signature. Zero means the current bytes are
// either a complete signature or unequivocally raw TCP.
func nextHTTPPrefixLength(br *bufio.Reader) int {
	peek, _ := br.Peek(br.Buffered())
	preamble := string(peek)
	next := 0
	for _, signature := range [...]string{
		"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ",
		"PATCH ", "CONNECT ", "TRACE ",
	} {
		if len(peek) < len(signature) && strings.HasPrefix(signature, preamble) &&
			(next == 0 || len(signature) < next) {
			next = len(signature)
		}
	}
	return next
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func normalizeConnectAuthority(authority string) (target, host string, err error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return "", "", fmt.Errorf("invalid CONNECT authority %q: %w", authority, err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", "", fmt.Errorf("invalid CONNECT port %q", port)
	}
	host, err = normalizeTLSHost(host)
	if err != nil {
		return "", "", err
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), host, nil
}

func normalizeTLSHost(host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", fmt.Errorf("empty TLS host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	for _, r := range host {
		if r > 0x7f {
			return "", fmt.Errorf("non-ASCII TLS host %q", host)
		}
	}
	if strings.ContainsAny(host, " \t\r\n/:\\[]%") {
		return "", fmt.Errorf("invalid TLS host %q", host)
	}
	return host, nil
}

func (p *Proxy) dialTLS(target, sni string) (*tls.Conn, error) {
	raw, err := net.DialTimeout("tcp", target, tlsHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Now().Add(tlsHandshakeTimeout)); err != nil {
		_ = raw.Close()
		return nil, err
	}
	upstream := tls.Client(raw, p.upstreamConfig(sni))
	if err := upstream.Handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return upstream, nil
}

func readRequestLine(br *bufio.Reader) (string, error) {
	line, err := readLimitedLine(br, maxConnectRequestLine)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(line), "\r\n"), nil
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
	total := 0
	for {
		line, err := readLimitedLine(br, maxConnectHeaderLine)
		if err != nil {
			return err
		}
		total += len(line)
		if total > maxConnectHeaders {
			return fmt.Errorf("CONNECT headers exceed %d bytes", maxConnectHeaders)
		}
		if (len(line) == 1 && line[0] == '\n') ||
			(len(line) == 2 && line[0] == '\r' && line[1] == '\n') {
			return nil
		}
	}
}

func readLimitedLine(br *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 4<<10))
	for {
		fragment, err := br.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, fmt.Errorf("proxy line exceeds %d bytes", limit)
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

func writeAll(dst net.Conn, p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := dst.Write(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
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
