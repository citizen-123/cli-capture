package ca

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestLeafChainsToCA(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	leaf, err := authority.LeafFor("api.example.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authority.CertPEM()) {
		t.Fatal("could not load CA pem")
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "api.example.com", Roots: roots}); err != nil {
		t.Fatalf("leaf does not verify against CA: %v", err)
	}
}

func TestLeafCached(t *testing.T) {
	authority, _ := LoadOrCreate(t.TempDir())
	a, _ := authority.LeafFor("host.test")
	b, _ := authority.LeafFor("host.test")
	if a != b {
		t.Fatal("expected cached leaf to be reused")
	}
}

func TestLeafCacheEvictsLeastRecentlyUsed(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	authority.leafLimit = 2

	if _, err := authority.LeafFor("one.test"); err != nil {
		t.Fatalf("mint one.test: %v", err)
	}
	two, err := authority.LeafFor("two.test")
	if err != nil {
		t.Fatalf("mint two.test: %v", err)
	}
	if _, err := authority.LeafFor("one.test"); err != nil {
		t.Fatalf("refresh one.test: %v", err)
	}
	if _, err := authority.LeafFor("three.test"); err != nil {
		t.Fatalf("mint three.test: %v", err)
	}

	authority.mu.Lock()
	_, oneCached := authority.leafs["one.test"]
	_, twoCached := authority.leafs["two.test"]
	_, threeCached := authority.leafs["three.test"]
	size := authority.leafLRU.Len()
	authority.mu.Unlock()
	if !oneCached || twoCached || !threeCached || size != 2 {
		t.Fatalf("cache after eviction: one=%t two=%t three=%t size=%d", oneCached, twoCached, threeCached, size)
	}
	if _, err := x509.ParseCertificate(two.Certificate[0]); err != nil {
		t.Fatalf("evicted leaf no longer has a usable certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(authority.CertPEM())
	serverConn, clientConn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		serverTLS := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{*two}})
		serverErr <- serverTLS.Handshake()
		serverTLS.Close()
	}()
	clientTLS := tls.Client(clientConn, &tls.Config{
		RootCAs:    roots,
		ServerName: "two.test",
	})
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("TLS handshake with evicted leaf: %v", err)
	}
	clientTLS.Close()
	if err := <-serverErr; err != nil {
		t.Fatalf("server TLS handshake with evicted leaf: %v", err)
	}

	reMinted, err := authority.LeafFor("two.test")
	if err != nil {
		t.Fatalf("re-mint evicted host: %v", err)
	}
	if reMinted == two {
		t.Fatal("evicted leaf was reused instead of minted again")
	}
}

func TestLeafForRejectsInvalidHosts(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	invalid := []string{
		"",
		".",
		"api..example.com",
		"-api.example.com",
		"api-.example.com",
		"api_example.com",
		"api/example.com",
		strings.Repeat("a", 254),
	}
	for _, host := range invalid {
		if _, err := authority.LeafFor(host); err == nil {
			t.Errorf("LeafFor(%q) succeeded for an invalid host", host)
		}
	}
	authority.mu.Lock()
	size := authority.leafLRU.Len()
	authority.mu.Unlock()
	if size != 0 {
		t.Errorf("invalid hosts populated cache: size = %d, want 0", size)
	}
}

func TestLeafCacheConcurrentRequestsReuseOneLeaf(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	const workers = 32
	leaves := make([]*tls.Certificate, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			leaves[i], errs[i] = authority.LeafFor("concurrent.example.test")
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent LeafFor call %d: %v", i, err)
		}
		if leaves[i] != leaves[0] {
			t.Fatalf("concurrent LeafFor call %d returned a different cached leaf", i)
		}
	}
	authority.mu.Lock()
	size := authority.leafLRU.Len()
	authority.mu.Unlock()
	if size != 1 {
		t.Errorf("concurrent cache size = %d, want 1", size)
	}
}
