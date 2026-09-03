package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/scope"
)

func TestSniffClientHelloFragmentedAndReplayable(t *testing.T) {
	hello := clientHelloBytes(t, "api.example.com")
	fragmented := fragmentHandshakeRecords(t, hello, 7)
	source := &memoryConn{input: fragmented, readChunk: 3}

	serverName, replay, err := sniffClientHello(source)
	if err != nil {
		t.Fatalf("sniff ClientHello: %v", err)
	}
	if serverName != "api.example.com" {
		t.Fatalf("SNI = %q, want api.example.com", serverName)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("read replayed connection: %v", err)
	}
	if !bytes.Equal(got, fragmented) {
		t.Fatal("replayed bytes differ from the fragmented ClientHello")
	}
	if source.output.Len() != 0 {
		t.Fatal("ClientHello probe wrote a TLS alert to the client")
	}
}

func TestSniffClientHelloMalformedAndOversizedAreBoundedAndReplayable(t *testing.T) {
	oversizedHandshake := make([]byte, 4+65536)
	oversizedHandshake[0] = 1 // ClientHello
	oversizedHandshake[1] = 1 // 65536-byte handshake body

	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "malformed",
			input: []byte{0x16, 0x03, 0x01, 0x00, 0x04, 0x01, 0x00, 0x00, 0x00},
		},
		{
			name:  "oversized peek",
			input: handshakeRecords(oversizedHandshake, 4),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &memoryConn{input: tt.input, readChunk: 11}
			_, replay, err := sniffClientHello(source)
			if err == nil {
				t.Fatal("sniff unexpectedly accepted invalid ClientHello")
			}
			if source.bytesRead > maxClientHelloPeek {
				t.Fatalf("probe read %d bytes, limit is %d", source.bytesRead, maxClientHelloPeek)
			}
			if tt.name == "oversized peek" && !errors.Is(err, errClientHelloPeekLimit) {
				t.Fatalf("oversized error = %v, want peek-limit error", err)
			}
			got, readErr := io.ReadAll(replay)
			if readErr != nil {
				t.Fatalf("read replayed connection: %v", readErr)
			}
			if !bytes.Equal(got, tt.input) {
				t.Fatal("invalid input was not replayed exactly")
			}
			if source.output.Len() != 0 {
				t.Fatal("failed probe wrote a TLS alert to the client")
			}
		})
	}
}

func TestSniffClientHelloTimesOutIncompleteInputAndClearsDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if err := client.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set initial write deadline: %v", err)
	}
	firstWrite := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte{0x16})
		firstWrite <- err
	}()

	start := time.Now()
	_, replay, err := sniffClientHelloWithin(server, 100*time.Millisecond)
	if err == nil {
		t.Fatal("incomplete ClientHello did not time out")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("incomplete ClientHello error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("incomplete ClientHello returned after %v, want bounded timeout", elapsed)
	}
	if err := <-firstWrite; err != nil {
		t.Fatalf("write incomplete ClientHello: %v", err)
	}

	var first [1]byte
	if _, err := io.ReadFull(replay, first[:]); err != nil || first[0] != 0x16 {
		t.Fatalf("replayed first byte = %#x, %v; want 0x16", first[0], err)
	}

	if err := client.SetWriteDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set follow-up write deadline: %v", err)
	}
	followupWrite := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte{0x03})
		followupWrite <- err
	}()
	var followup [1]byte
	if _, err := io.ReadFull(replay, followup[:]); err != nil || followup[0] != 0x03 {
		t.Fatalf("read after cleared deadline = %#x, %v; want 0x03", followup[0], err)
	}
	if err := <-followupWrite; err != nil {
		t.Fatalf("write after cleared deadline: %v", err)
	}
}

func TestTransparentTLSMITMPolicyUsesClientHelloSNI(t *testing.T) {
	state := transparentHandshake(t, "api.example.com", func(_ string) *scope.Set {
		return mustPolicy(t, scope.Exclude, scope.Include, scope.Glob, "*.example.com")
	})
	if got := state.PeerCertificates[0].Issuer.CommonName; got != "cli-capture Root CA" {
		t.Fatalf("peer issuer = %q, want proxy CA (transparent SNI should select MITM)", got)
	}
}

func TestTransparentTLSMITMPolicyFallsBackToDestinationIPWithoutSNI(t *testing.T) {
	state := transparentHandshake(t, "", func(targetHost string) *scope.Set {
		return mustPolicy(t, scope.Exclude, scope.Include, scope.Exact, targetHost)
	})
	if got := state.PeerCertificates[0].Issuer.CommonName; got != "cli-capture Root CA" {
		t.Fatalf("peer issuer = %q, want proxy CA (destination IP should select MITM)", got)
	}
}

func TestTransparentTLSPassthroughReceivesOriginalClientHello(t *testing.T) {
	hello := fragmentHandshakeRecords(t, clientHelloBytes(t, "api.example.com"), 5)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upstream.Close()

	received := make(chan []byte, 1)
	upstreamErr := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamErr <- err
			return
		}
		defer conn.Close()
		got := make([]byte, len(hello))
		_, err = io.ReadFull(conn, got)
		if err != nil {
			upstreamErr <- err
			return
		}
		received <- got
	}()

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	store := capture.NewStore()
	px := New(store, authority, intercept.NewEngine())
	px.SetMITMPolicy(mustPolicy(t, scope.Include, scope.Exclude, scope.Glob, "*.example.com"))

	client, proxySide := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		px.HandleTransparent(proxySide, upstream.Addr().String())
		close(handlerDone)
	}()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	if _, err := client.Write(hello); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}

	select {
	case got := <-received:
		if !bytes.Equal(got, hello) {
			t.Fatal("passthrough upstream did not receive the original ClientHello")
		}
	case err := <-upstreamErr:
		t.Fatalf("upstream read: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for passthrough ClientHello")
	}
	client.Close()
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("transparent passthrough did not stop")
	}

	flows := store.List()
	if len(flows) != 1 || flows[0].SNI != "api.example.com" {
		t.Fatalf("passthrough flow SNI = %q, want api.example.com", flowSNI(flows))
	}
}

func TestTransparentRawTCPForwardsShortGreetingPromptly(t *testing.T) {
	const timeout = time.Second
	greeting := []byte{0x01, 0x02, 0x03}
	reply := []byte{0x04, 0x05}

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upstream.Close()

	received := make(chan []byte, 1)
	upstreamErr := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamErr <- err
			return
		}
		defer conn.Close()

		got := make([]byte, len(greeting))
		if _, err := io.ReadFull(conn, got); err != nil {
			upstreamErr <- err
			return
		}
		if _, err := conn.Write(reply); err != nil {
			upstreamErr <- err
			return
		}
		received <- got
	}()

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	store := capture.NewStore()
	px := New(store, authority, intercept.NewEngine())

	client, proxySide := net.Pipe()
	defer client.Close()
	handlerDone := make(chan struct{})
	go func() {
		px.HandleTransparent(proxySide, upstream.Addr().String())
		close(handlerDone)
	}()

	if err := client.SetDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	if _, err := client.Write(greeting); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	gotReply := make([]byte, len(reply))
	if _, err := io.ReadFull(client, gotReply); err != nil {
		t.Fatalf("read upstream reply: %v", err)
	}
	if !bytes.Equal(gotReply, reply) {
		t.Fatalf("reply = %x, want %x", gotReply, reply)
	}
	select {
	case got := <-received:
		if !bytes.Equal(got, greeting) {
			t.Fatalf("upstream greeting = %x, want %x", got, greeting)
		}
	case err := <-upstreamErr:
		t.Fatalf("upstream: %v", err)
	case <-time.After(timeout):
		t.Fatal("upstream did not receive short greeting")
	}

	_ = client.Close()
	select {
	case <-handlerDone:
	case <-time.After(timeout):
		t.Fatal("transparent raw handler did not stop")
	}
	flows := store.List()
	if len(flows) != 1 || flows[0].Protocol != capture.ProtoRawTCP {
		t.Fatalf("transparent flow = %+v, want one raw TCP flow", flows)
	}
}

func transparentHandshake(t *testing.T, serverName string, policy func(targetHost string) *scope.Set) tls.ConnectionState {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	target := upstream.Listener.Addr().String()
	targetHost, _, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split upstream address: %v", err)
	}

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	px := New(capture.NewStore(), authority, intercept.NewEngine())
	px.SetMITMPolicy(policy(targetHost))
	px.SetUpstreamTLS(func(string) *tls.Config {
		return &tls.Config{InsecureSkipVerify: true} // test server has a private, ephemeral CA
	})

	client, proxySide := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		px.HandleTransparent(proxySide, target)
		close(handlerDone)
	}()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}

	config := &tls.Config{ServerName: serverName}
	if serverName == "" {
		config.InsecureSkipVerify = true // deliberately exercise a no-SNI ClientHello
	} else {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(authority.CertPEM()) {
			t.Fatal("add proxy CA to root pool")
		}
		config.RootCAs = roots
	}
	tlsClient := tls.Client(&fragmentWriteConn{Conn: client, max: 3}, config)
	if err := tlsClient.Handshake(); err != nil {
		client.Close()
		t.Fatalf("transparent TLS handshake: %v", err)
	}
	state := tlsClient.ConnectionState()
	request, err := http.NewRequest(http.MethodGet, "https://"+target+"/", nil)
	if err != nil {
		tlsClient.Close()
		t.Fatalf("create request: %v", err)
	}
	request.Close = true
	if err := request.Write(tlsClient); err != nil {
		tlsClient.Close()
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsClient), request)
	if err != nil {
		tlsClient.Close()
		t.Fatalf("read response: %v", err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		response.Body.Close()
		tlsClient.Close()
		t.Fatalf("read response body: %v", err)
	}
	response.Body.Close()
	tlsClient.Close()

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("transparent TLS handler did not stop")
	}
	return state
}

func mustPolicy(t *testing.T, defaultAction, ruleAction scope.Action, strategy scope.Strategy, pattern string) *scope.Set {
	t.Helper()
	policy := &scope.Set{
		Default: defaultAction,
		Rules: []*scope.Rule{{
			Enabled: true,
			Action:  ruleAction,
			Conditions: []scope.Condition{{
				Field:    scope.FieldHost,
				Strategy: strategy,
				Pattern:  pattern,
			}},
		}},
	}
	if err := policy.Compile(); err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	return policy
}

func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	conn := &memoryConn{}
	config := &tls.Config{ServerName: serverName, InsecureSkipVerify: true}
	_ = tls.Client(conn, config).Handshake()
	if conn.output.Len() == 0 {
		t.Fatal("TLS client did not emit a ClientHello")
	}
	return bytes.Clone(conn.output.Bytes())
}

func fragmentHandshakeRecords(t *testing.T, records []byte, size int) []byte {
	t.Helper()
	var handshake []byte
	var version [2]byte
	for offset := 0; offset < len(records); {
		if len(records)-offset < 5 {
			t.Fatal("short TLS record header in generated ClientHello")
		}
		if records[offset] != 0x16 {
			t.Fatalf("generated first flight contains record type %#x, want handshake", records[offset])
		}
		version = [2]byte{records[offset+1], records[offset+2]}
		length := int(records[offset+3])<<8 | int(records[offset+4])
		offset += 5
		if len(records)-offset < length {
			t.Fatal("short TLS record payload in generated ClientHello")
		}
		handshake = append(handshake, records[offset:offset+length]...)
		offset += length
	}
	return handshakeRecordsWithVersion(handshake, size, version)
}

func handshakeRecords(handshake []byte, size int) []byte {
	return handshakeRecordsWithVersion(handshake, size, [2]byte{0x03, 0x01})
}

func handshakeRecordsWithVersion(handshake []byte, size int, version [2]byte) []byte {
	var records []byte
	for len(handshake) != 0 {
		n := min(size, len(handshake))
		records = append(records, 0x16, version[0], version[1], byte(n>>8), byte(n))
		records = append(records, handshake[:n]...)
		handshake = handshake[n:]
	}
	return records
}

func flowSNI(flows []*capture.Flow) string {
	if len(flows) == 0 {
		return ""
	}
	return flows[0].SNI
}

type memoryConn struct {
	input     []byte
	readChunk int
	bytesRead int
	output    bytes.Buffer
}

func (c *memoryConn) Read(p []byte) (int, error) {
	if c.bytesRead == len(c.input) {
		return 0, io.EOF
	}
	if c.readChunk > 0 && len(p) > c.readChunk {
		p = p[:c.readChunk]
	}
	n := copy(p, c.input[c.bytesRead:])
	c.bytesRead += n
	return n, nil
}

func (c *memoryConn) Write(p []byte) (int, error)    { return c.output.Write(p) }
func (*memoryConn) Close() error                     { return nil }
func (*memoryConn) LocalAddr() net.Addr              { return testAddr("local") }
func (*memoryConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (*memoryConn) SetDeadline(time.Time) error      { return nil }
func (*memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (*memoryConn) SetWriteDeadline(time.Time) error { return nil }

type fragmentWriteConn struct {
	net.Conn
	max int
}

func (c *fragmentWriteConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) != 0 {
		n := min(c.max, len(p))
		m, err := c.Conn.Write(p[:n])
		written += m
		p = p[m:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }
