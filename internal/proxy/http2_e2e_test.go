package proxy_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
)

func grpcFrame(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[1] = byte(len(payload) >> 24)
	out[2] = byte(len(payload) >> 16)
	out[3] = byte(len(payload) >> 8)
	out[4] = byte(len(payload))
	copy(out[5:], payload)
	return out
}

// TestH2GRPCEndToEnd exercises the whole HTTP/2 path: a client CONNECTs to the
// proxy, negotiates h2 via ALPN through the MITM, and makes a gRPC call to an
// h2 upstream. It asserts the response and trailer survive the proxy and that
// the exchange was captured as a gRPC flow.
func TestH2GRPCEndToEnd(t *testing.T) {
	// --- h2 upstream that speaks a minimal gRPC exchange ---
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(grpcFrame([]byte("pong")))
		w.Header().Set("Grpc-Status", "0")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "https://") // host:port

	// --- proxy trusting the upstream's self-signed cert ---
	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := capture.NewStore()
	px := proxy.New(store, authority, intercept.NewEngine())

	upPool := x509.NewCertPool()
	upPool.AddCert(upstream.Certificate())
	px.SetUpstreamTLS(func(sni string) *tls.Config {
		return &tls.Config{ServerName: sni, RootCAs: upPool}
	})
	if err := px.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	go px.Serve()
	defer px.Close()

	// --- client: CONNECT, then ALPN-h2 TLS through the tunnel, then a gRPC call ---
	conn, err := net.Dial("tcp", px.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	statusLine, _ := br.ReadString('\n')
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT failed: %q", statusLine)
	}
	for {
		line, _ := br.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(authority.CertPEM())
	hostname, _, _ := net.SplitHostPort(target)
	tlsConn := tls.Client(conn, &tls.Config{ServerName: hostname, RootCAs: caPool, NextProtos: []string{"h2"}})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Fatalf("ALPN negotiated %q, want h2", got)
	}

	cc, err := (&http2.Transport{}).NewClientConn(tlsConn)
	if err != nil {
		t.Fatalf("h2 client conn: %v", err)
	}
	req, _ := http.NewRequest("POST", "https://"+target+"/pkg.Svc/Call", bytes.NewReader(grpcFrame([]byte("ping"))))
	req.Header.Set("Content-Type", "application/grpc")

	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("gRPC round trip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !bytes.Contains(body, []byte("pong")) {
		t.Errorf("response body missing pong: %q", body)
	}
	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Errorf("grpc-status trailer = %q, want 0", got)
	}

	// --- capture assertions ---
	deadline := time.Now().Add(time.Second)
	var f *capture.Flow
	for time.Now().Before(deadline) {
		if fs := store.List(); len(fs) == 1 {
			f = fs[0]
			if f.Protocol == capture.ProtoGRPC && len(f.Messages) >= 2 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f == nil {
		t.Fatal("no flow captured")
	}
	if f.Protocol != capture.ProtoGRPC {
		t.Errorf("flow protocol = %s, want grpc", f.Protocol)
	}
	var haveReq, haveResp bool
	for _, m := range f.Messages {
		if m.Direction == capture.ClientToServer && string(m.Body) == "ping" {
			haveReq = true
		}
		if m.Direction == capture.ServerToClient && string(m.Body) == "pong" {
			haveResp = true
		}
	}
	if !haveReq || !haveResp {
		t.Errorf("gRPC messages not captured both ways: req=%v resp=%v", haveReq, haveResp)
	}
}
