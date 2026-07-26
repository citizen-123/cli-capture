package ca

import (
	"crypto/x509"
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
