// Package ca generates a local man-in-the-middle certificate authority and
// mints leaf certificates for intercepted TLS hosts on demand. The child
// process trusts this CA because the runner points its trust-store env vars
// (SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, …) at the exported PEM.
package ca

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/citizen-123/cli-capture/internal/ownerfile"
)

// leafCacheCapacity bounds the memory held by host-specific certificates.
// Evicted certificates remain safe for any TLS handshake already using them.
const leafCacheCapacity = 256

var errInvalidHost = errors.New("ca: invalid DNS host")

// CA holds the root keypair and an LRU cache of already-minted leaf certs
// keyed by the requested server name.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte

	mu        sync.Mutex
	leafs     map[string]*list.Element
	leafLRU   *list.List
	leafLimit int
}

type leafCacheEntry struct {
	host string
	leaf *tls.Certificate
}

func newCA(cert *x509.Certificate, key *ecdsa.PrivateKey, certPEM []byte) *CA {
	return &CA{
		cert:      cert,
		key:       key,
		certPEM:   certPEM,
		leafs:     make(map[string]*list.Element, leafCacheCapacity),
		leafLRU:   list.New(),
		leafLimit: leafCacheCapacity,
	}
}

// LoadOrCreate reads a CA from dir, generating and persisting a new one if none
// exists. dir is typically ~/.cli-capture.
func LoadOrCreate(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll leaves an existing directory's mode alone, so a dir the user
	// created by hand arrives as 0755 under the usual umask. This dir holds the
	// CA key and captured credentials; narrow it either way.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca.key")

	if cert, key, pemBytes, err := load(certPath, keyPath); err == nil {
		return newCA(cert, key, pemBytes), nil
	}
	return create(certPath, keyPath)
}

func load(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, nil, err
	}
	return parse(certPEM, keyPEM)
}

// LoadOrCreateSecure is LoadOrCreate for a root-owned StateDir. It uses
// dirfd-relative, no-follow operations for the CA state used by a privileged
// transparent proxy.
func LoadOrCreateSecure(dir *ownerfile.StateDir) (*CA, error) {
	cert, key, pemBytes, err := loadSecure(dir)
	if err == nil {
		return newCA(cert, key, pemBytes), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ca: read trusted state: %w", err)
	}
	return createSecure(dir)
}

func loadSecure(dir *ownerfile.StateDir) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPEM, err := dir.ReadFile("ca.pem")
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM, err := dir.ReadFile("ca.key")
	if err != nil {
		return nil, nil, nil, err
	}
	return parse(certPEM, keyPEM)
}

func parse(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	cblock, _ := pem.Decode(certPEM)
	kblock, _ := pem.Decode(keyPEM)
	if cblock == nil || kblock == nil {
		return nil, nil, nil, fmt.Errorf("ca: malformed pem")
	}
	cert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kblock.Bytes)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, key, certPEM, nil
}

func create(certPath, keyPath string) (*CA, error) {
	authority, certPEM, keyPEM, err := generate()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return authority, nil
}

func createSecure(dir *ownerfile.StateDir) (*CA, error) {
	authority, certPEM, keyPEM, err := generate()
	if err != nil {
		return nil, err
	}
	if err := dir.WriteFile("ca.pem", certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := dir.WriteFile("ca.key", keyPEM, 0o600); err != nil {
		return nil, err
	}
	return authority, nil
}

func generate() (*CA, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "cli-capture Root CA",
			Organization: []string{"cli-capture"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	authority := newCA(cert, key, certPEM)
	return authority, certPEM, keyPEM, nil
}

// CertPEM is the PEM-encoded root certificate, for writing to a trust file the
// child will load.
func (c *CA) CertPEM() []byte { return c.certPEM }

// LeafFor returns (minting and caching if necessary) a leaf certificate valid
// for host. host may be a DNS name or an IP literal.
func (c *CA) LeafFor(host string) (*tls.Certificate, error) {
	if err := validateHost(host); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.leafs[host]; ok {
		c.leafLRU.MoveToFront(element)
		return element.Value.(leafCacheEntry).leaf, nil
	}

	leaf, err := c.mintLeaf(host)
	if err != nil {
		return nil, err
	}
	if c.leafLimit > 0 {
		if c.leafLRU.Len() >= c.leafLimit {
			oldest := c.leafLRU.Back()
			entry := oldest.Value.(leafCacheEntry)
			delete(c.leafs, entry.host)
			c.leafLRU.Remove(oldest)
		}
		c.leafs[host] = c.leafLRU.PushFront(leafCacheEntry{host: host, leaf: leaf})
	}
	return leaf, nil
}

func (c *CA) mintLeaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
	}, nil
}

func validateHost(host string) error {
	if len(host) == 0 || len(host) > 253 {
		return errInvalidHost
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if strings.HasSuffix(host, ".") {
		host = host[:len(host)-1]
	}
	if host == "" {
		return errInvalidHost
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errInvalidHost
		}
		for i := range len(label) {
			char := label[i]
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') && char != '-' {
				return errInvalidHost
			}
		}
	}
	return nil
}
