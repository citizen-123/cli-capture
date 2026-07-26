package transparent

import (
	"strings"
	"testing"
)

func TestParseSockaddrIn(t *testing.T) {
	// family 0x0002, port 443 (0x01BB big-endian), addr 10.1.2.3, then padding.
	b := []byte{0x02, 0x00, 0x01, 0xBB, 10, 1, 2, 3, 0, 0, 0, 0, 0, 0, 0, 0}
	got, err := parseSockaddrIn(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.2.3:443" {
		t.Errorf("parseSockaddrIn = %q, want 10.1.2.3:443", got)
	}
}

func TestParseSockaddrInShort(t *testing.T) {
	if _, err := parseSockaddrIn([]byte{1, 2, 3}); err == nil {
		t.Error("expected an error for a short buffer")
	}
}

func TestParseSockaddrIn6(t *testing.T) {
	b := make([]byte, 28)
	b[0], b[1] = 0x0A, 0x00 // AF_INET6
	b[2], b[3] = 0x01, 0xBB // port 443
	// [8:24] = ::1
	b[23] = 1
	got, err := parseSockaddrIn6(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != "[::1]:443" {
		t.Errorf("parseSockaddrIn6 = %q, want [::1]:443", got)
	}
}

func TestNFTRedirect(t *testing.T) {
	joined := strings.Join(Shell("nft", NFTRedirect(8080, 1500)), "\n")
	for _, want := range []string{"redirect to :8080", "skuid 1500", "dport { 80 , 443 }", "nft add table ip cli_capture"} {
		if !strings.Contains(joined, want) {
			t.Errorf("NFTRedirect output missing %q:\n%s", want, joined)
		}
	}
}

func TestIPTablesRedirect(t *testing.T) {
	joined := strings.Join(Shell("iptables", IPTablesRedirect(8080, 1500)), "\n")
	for _, want := range []string{"--uid-owner 1500", "--dports 80,443", "--to-ports 8080", "-A OUTPUT -j cli_capture"} {
		if !strings.Contains(joined, want) {
			t.Errorf("IPTablesRedirect output missing %q:\n%s", want, joined)
		}
	}
}

func TestListenAddrClose(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if l.Addr() == "" {
		t.Error("Addr() returned empty")
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
