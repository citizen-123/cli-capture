package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/scope"
)

func TestListenRejectsNonLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:0", "localhost:0"} {
		if err := ValidateListenAddress(addr); err == nil {
			t.Errorf("ValidateListenAddress(%q) accepted a non-loopback listener", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := ValidateListenAddress(addr); err != nil {
			t.Errorf("ValidateListenAddress(%q): %v", addr, err)
		}
	}

	p := New(capture.NewStore(), nil, nil)
	if err := p.Listen("0.0.0.0:0"); err == nil {
		t.Fatal("Proxy.Listen accepted a wildcard listener")
	}
}

func TestServeClosesPeerWhenAdmissionLimitIsReached(t *testing.T) {
	p := New(capture.NewStore(), nil, nil)
	p.connectionSlots = make(chan struct{}, 1)
	p.connectionSlots <- struct{}{}
	if err := p.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer p.Close()
	go p.Serve()

	conn, err := net.DialTimeout("tcp", p.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var b [1]byte
	if n, err := conn.Read(b[:]); n != 0 || err == nil {
		t.Fatalf("admission-rejected peer returned %d bytes, %v; want closed peer", n, err)
	}
}

func TestHandleClosesIncompletePreamble(t *testing.T) {
	client, proxySide := net.Pipe()
	defer client.Close()

	p := New(capture.NewStore(), nil, nil)
	done := make(chan struct{})
	go func() {
		p.handle(proxySide)
		close(done)
	}()

	writeDone := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte("CON"))
		writeDone <- err
	}()
	if err := <-writeDone; err != nil {
		t.Fatalf("write incomplete preamble: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close incomplete peer: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy retained an incomplete preamble")
	}
}

func TestHandleRejectsOversizedCONNECTRequestOrHeader(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "request line",
			input: "CONNECT " + strings.Repeat("a", maxConnectRequestLine) + ":443 HTTP/1.1\r\n\r\n",
		},
		{
			name:  "header line",
			input: "CONNECT 127.0.0.1:443 HTTP/1.1\r\nX-Test: " + strings.Repeat("a", maxConnectHeaderLine) + "\r\n\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, proxySide := net.Pipe()
			defer client.Close()

			p := New(capture.NewStore(), nil, nil)
			done := make(chan struct{})
			go func() {
				p.handle(proxySide)
				close(done)
			}()
			writeDone := make(chan error, 1)
			go func() {
				_, err := io.WriteString(client, tt.input)
				writeDone <- err
			}()

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("proxy did not reject the oversized CONNECT input")
			}
			if err := <-writeDone; err != nil && err != io.ErrClosedPipe {
				t.Fatalf("write oversized CONNECT input: %v", err)
			}
			if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				if err == io.ErrClosedPipe {
					return
				}
				t.Fatalf("set read deadline: %v", err)
			}
			var b [1]byte
			if n, err := client.Read(b[:]); n != 0 || err == nil {
				t.Fatalf("oversized CONNECT returned %d bytes, %v; want closed peer", n, err)
			}
		})
	}
}

func TestCONNECTIPAuthorityDeniedSNIUsesBlindTunnel(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	p := New(capture.NewStore(), authority, intercept.NewEngine())
	p.SetMITMPolicy(mustPolicy(t, scope.Include, scope.Exclude, scope.Exact, "denied.example"))
	if err := p.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer p.Close()
	go p.Serve()

	conn, err := net.DialTimeout("tcp", p.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+upstream.Listener.Addr().String()+" HTTP/1.1\r\nHost: ignored\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	clientTLS := tls.Client(rw(conn, reader), &tls.Config{
		ServerName:         "denied.example",
		InsecureSkipVerify: true,
	})
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("blind TLS handshake: %v", err)
	}
	state := clientTLS.ConnectionState()
	if got := state.PeerCertificates[0].Issuer.CommonName; got == "cli-capture Root CA" {
		t.Fatal("excluded ClientHello SNI received a proxy MITM certificate")
	}
}

func TestCONNECTIPAuthorityWithoutSNIUsesAuthorityPolicy(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	host, _, err := net.SplitHostPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split upstream address: %v", err)
	}

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	p := New(capture.NewStore(), authority, intercept.NewEngine())
	p.SetMITMPolicy(mustPolicy(t, scope.Exclude, scope.Include, scope.Exact, host))
	p.SetUpstreamTLS(func(string) *tls.Config {
		return &tls.Config{InsecureSkipVerify: true}
	})
	if err := p.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer p.Close()
	go p.Serve()

	conn, err := net.DialTimeout("tcp", p.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+upstream.Listener.Addr().String()+" HTTP/1.1\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority.CertPEM()) {
		t.Fatal("add proxy CA certificate")
	}
	clientTLS := tls.Client(rw(conn, reader), &tls.Config{ServerName: host, RootCAs: pool})
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("IP CONNECT TLS handshake: %v", err)
	}
	if got := clientTLS.ConnectionState().PeerCertificates[0].Issuer.CommonName; got != "cli-capture Root CA" {
		t.Fatalf("IP authority without SNI received issuer %q, want proxy CA", got)
	}
}

func TestPassthroughPreservesHalfClose(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upstream.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := upstream.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	client, proxySide := tcpPair(t)
	defer client.Close()
	defer proxySide.Close()
	p := New(capture.NewStore(), nil, nil)
	done := make(chan struct{})
	go func() {
		p.passthrough(proxySide, upstream.Addr().String(), "127.0.0.1")
		close(done)
	}()

	var upstreamPeer net.Conn
	select {
	case upstreamPeer = <-accepted:
		defer upstreamPeer.Close()
	case <-time.After(time.Second):
		t.Fatal("passthrough did not dial upstream")
	}

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("half-close client write: %v", err)
	}
	if err := upstreamPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set upstream read deadline: %v", err)
	}
	request, err := io.ReadAll(upstreamPeer)
	if err != nil {
		t.Fatalf("read half-closed request: %v", err)
	}
	if string(request) != "request" {
		t.Fatalf("upstream request = %q, want request", request)
	}

	if _, err := upstreamPeer.Write([]byte("response")); err != nil {
		t.Fatalf("write response: %v", err)
	}
	upstreamWriter, ok := upstreamPeer.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("upstream connection does not support CloseWrite")
	}
	if err := upstreamWriter.CloseWrite(); err != nil {
		t.Fatalf("half-close upstream write: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read half-closed response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("client response = %q, want response", response)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("passthrough did not finish after both directions half-closed")
	}
}

func TestReplayConnPreservesHalfCloseThroughIdleConn(t *testing.T) {
	client, proxySide := tcpPair(t)
	defer client.Close()

	relay, err := newIdleConn(&replayConn{
		Conn:   proxySide,
		prefix: []byte("already probed"),
	}, time.Second)
	if err != nil {
		t.Fatalf("wrap replay connection: %v", err)
	}
	defer relay.Close()

	if err := relay.CloseWrite(); err != nil {
		t.Fatalf("half-close replay relay: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	n, err := client.Read(make([]byte, 1))
	if n != 0 || err != io.EOF {
		t.Fatalf("client read after replay half-close = (%d, %v), want (0, EOF)", n, err)
	}
}

func TestBridgePreservesHalfCloseThroughWrappers(t *testing.T) {
	client, proxyClient := tcpPair(t)
	defer client.Close()
	defer proxyClient.Close()
	proxyServer, server := tcpPair(t)
	defer proxyServer.Close()
	defer server.Close()

	p := New(capture.NewStore(), nil, nil)
	done := make(chan struct{})
	go func() {
		p.bridge(
			capture.NewFlow(client.RemoteAddr().String(), server.RemoteAddr().String()),
			rw(proxyClient, bufio.NewReader(proxyClient)),
			proxyServer,
			detectFromBuffered,
		)
		close(done)
	}()

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("half-close client write: %v", err)
	}
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set server read deadline: %v", err)
	}
	request, err := io.ReadAll(server)
	if err != nil {
		t.Fatalf("read bridged request: %v", err)
	}
	if string(request) != "request" {
		t.Fatalf("server request = %q, want request", request)
	}

	if _, err := server.Write([]byte("response")); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if err := server.CloseWrite(); err != nil {
		t.Fatalf("half-close server write: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read bridged response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("client response = %q, want response", response)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not finish after both directions half-closed")
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen TCP pair: %v", err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatalf("dial TCP pair: %v", err)
	}
	server, err := listener.AcceptTCP()
	if err != nil {
		_ = client.Close()
		t.Fatalf("accept TCP pair: %v", err)
	}
	return client, server
}
