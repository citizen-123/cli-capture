package transparent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestNFTRedirectPlanCoversIPv4AndIPv6(t *testing.T) {
	namespace := RuleNamespace("cli_capture_test")
	got := NFTRedirect(namespace, 8080, 1500)
	want := [][]string{
		{"add", "table", "ip", "cli_capture_test"},
		{"add", "chain", "ip", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"},
		{"add", "rule", "ip", "cli_capture_test", "output", "meta", "skuid", "1500", "tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":8080"},
		{"add", "table", "ip6", "cli_capture_test"},
		{"add", "chain", "ip6", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"},
		{"add", "rule", "ip6", "cli_capture_test", "output", "meta", "skuid", "1500", "tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":8080"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NFTRedirect() = %#v, want %#v", got, want)
	}

	flush := [][]string{
		{"delete", "table", "ip6", "cli_capture_test"},
		{"delete", "table", "ip", "cli_capture_test"},
	}
	if got := NFTFlush(namespace); !reflect.DeepEqual(got, flush) {
		t.Errorf("NFTFlush() = %#v, want %#v", got, flush)
	}
}

func TestLegacyCommandPlanCoversIPv4AndIPv6(t *testing.T) {
	backend := Backend{
		Bin:          "/usr/sbin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/usr/sbin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	}
	redirect, flush, err := backend.commandPlan(RuleNamespace("cli_capture_test"), 8080, 1500)
	if err != nil {
		t.Fatalf("build legacy command plan: %v", err)
	}
	wantRedirect := []firewallCommand{
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-N", "cli_capture_test"}},
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-A", "cli_capture_test", "-p", "tcp", "-m", "owner", "--uid-owner", "1500", "-m", "multiport", "--dports", "80,443", "-j", "REDIRECT", "--to-ports", "8080"}},
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-A", "OUTPUT", "-j", "cli_capture_test"}},
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-N", "cli_capture_test"}},
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-A", "cli_capture_test", "-p", "tcp", "-m", "owner", "--uid-owner", "1500", "-m", "multiport", "--dports", "80,443", "-j", "REDIRECT", "--to-ports", "8080"}},
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-A", "OUTPUT", "-j", "cli_capture_test"}},
	}
	wantFlush := []firewallCommand{
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-D", "OUTPUT", "-j", "cli_capture_test"}},
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-F", "cli_capture_test"}},
		{bin: "/usr/sbin/ip6tables", argv: []string{"-t", "nat", "-X", "cli_capture_test"}},
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-D", "OUTPUT", "-j", "cli_capture_test"}},
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-F", "cli_capture_test"}},
		{bin: "/usr/sbin/iptables", argv: []string{"-t", "nat", "-X", "cli_capture_test"}},
	}
	if !reflect.DeepEqual(redirect, wantRedirect) {
		t.Errorf("legacy redirect plan = %#v, want %#v", redirect, wantRedirect)
	}
	if !reflect.DeepEqual(flush, wantFlush) {
		t.Errorf("legacy flush plan = %#v, want %#v", flush, wantFlush)
	}
}

func TestApplyBackendRollsBackBothNFTFamilies(t *testing.T) {
	backend := Backend{
		Bin:          "nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	}
	namespace := RuleNamespace("cli_capture_test")
	var got []firewallCommand
	fail := errors.New("injected ip6 failure")
	_, err := applyBackend(backend, namespace, 8080, 1500, func(bin string, argv []string) error {
		got = append(got, firewallCommand{bin: bin, argv: argv})
		if reflect.DeepEqual(argv, []string{"add", "chain", "ip6", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"}) {
			return fail
		}
		return nil
	})
	if !errors.Is(err, fail) {
		t.Fatalf("apply backend error = %v, want injected IPv6 failure", err)
	}

	want := []firewallCommand{
		{bin: "nft", argv: []string{"add", "table", "ip", "cli_capture_test"}},
		{bin: "nft", argv: []string{"add", "chain", "ip", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"}},
		{bin: "nft", argv: []string{"add", "rule", "ip", "cli_capture_test", "output", "meta", "skuid", "1500", "tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":8080"}},
		{bin: "nft", argv: []string{"add", "table", "ip6", "cli_capture_test"}},
		{bin: "nft", argv: []string{"add", "chain", "ip6", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"}},
		{bin: "nft", argv: []string{"delete", "table", "ip6", "cli_capture_test"}},
		{bin: "nft", argv: []string{"delete", "table", "ip", "cli_capture_test"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %#v, want %#v", got, want)
	}
}

func TestApplyBackendDoesNotDeleteUnownedIPv6Namespace(t *testing.T) {
	backend := Backend{
		Bin:          "nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	}
	namespace := RuleNamespace("cli_capture_test")
	fail := errors.New("injected IPv6 table collision")
	var got []firewallCommand
	_, err := applyBackend(backend, namespace, 8080, 1500, func(bin string, argv []string) error {
		got = append(got, firewallCommand{bin: bin, argv: argv})
		if reflect.DeepEqual(argv, []string{"add", "table", "ip6", "cli_capture_test"}) {
			return fail
		}
		return nil
	})
	if !errors.Is(err, fail) {
		t.Fatalf("apply backend error = %v, want injected IPv6 failure", err)
	}
	want := []firewallCommand{
		{bin: "nft", argv: []string{"add", "table", "ip", "cli_capture_test"}},
		{bin: "nft", argv: []string{"add", "chain", "ip", "cli_capture_test", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"}},
		{bin: "nft", argv: []string{"add", "rule", "ip", "cli_capture_test", "output", "meta", "skuid", "1500", "tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":8080"}},
		{bin: "nft", argv: []string{"add", "table", "ip6", "cli_capture_test"}},
		{bin: "nft", argv: []string{"delete", "table", "ip", "cli_capture_test"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %#v, want %#v", got, want)
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

func TestListenRejectsNonLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:0", "localhost:0"} {
		if err := ValidateListenAddress(addr); err == nil {
			t.Errorf("ValidateListenAddress(%q) accepted a non-loopback listener", addr)
		}
		if listener, err := Listen(addr); err == nil {
			_ = listener.Close()
			t.Errorf("Listen(%q) accepted a non-loopback listener", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := ValidateListenAddress(addr); err != nil {
			t.Errorf("ValidateListenAddress(%q): %v", addr, err)
		}
	}
}

func TestConcurrentRuleNamespacesDoNotOverlap(t *testing.T) {
	const instances = 32
	namespaces := make(chan RuleNamespace, instances)
	errs := make(chan error, instances)
	var wg sync.WaitGroup
	for range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			namespace, err := NewRuleNamespace()
			if err != nil {
				errs <- err
				return
			}
			namespaces <- namespace
		}()
	}
	wg.Wait()
	close(namespaces)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	seen := make(map[RuleNamespace]struct{}, instances)
	for namespace := range namespaces {
		if !strings.HasPrefix(string(namespace), ruleNamespacePrefix) {
			t.Fatalf("rule namespace %q does not use the private prefix", namespace)
		}
		if _, exists := seen[namespace]; exists {
			t.Fatalf("duplicate rule namespace %q", namespace)
		}
		seen[namespace] = struct{}{}
	}
	if len(seen) != instances {
		t.Fatalf("generated %d rule namespaces, want %d", len(seen), instances)
	}

	legacy := Backend{
		Bin:          "iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	}

	for namespace := range seen {
		commands := append(NFTRedirect(namespace, 8080, 1500), NFTFlush(namespace)...)
		for _, command := range commands {
			for other := range seen {
				if other == namespace {
					continue
				}
				for _, arg := range command {
					if arg == string(other) {
						t.Fatalf("namespace %q command %q touches other instance %q", namespace, command, other)
					}
				}
			}
		}
		legacyRedirect, legacyFlush, err := legacy.commandPlan(namespace, 8080, 1500)
		if err != nil {
			t.Fatalf("build legacy plan for %q: %v", namespace, err)
		}
		for _, command := range append(legacyRedirect, legacyFlush...) {
			for other := range seen {
				if other == namespace {
					continue
				}
				for _, arg := range command.argv {
					if arg == string(other) {
						t.Fatalf("legacy namespace %q command %q touches other instance %q", namespace, command.argv, other)
					}
				}
			}
		}
	}
}

func TestDetectBackendUsesOnlyTrustedAbsolutePaths(t *testing.T) {
	var checked []string
	got, err := detectBackend(trustedBackends, func(path string) (os.FileInfo, error) {
		checked = append(checked, path)
		if path == "/usr/bin/nft" {
			return backendFileInfo{mode: 0o755}, nil
		}
		return nil, os.ErrNotExist
	})
	if err != nil {
		t.Fatalf("detect backend: %v", err)
	}
	if got.Bin != "/usr/bin/nft" {
		t.Fatalf("backend binary = %q, want /usr/bin/nft", got.Bin)
	}
	for _, path := range checked {
		if !filepath.IsAbs(path) {
			t.Errorf("backend discovery consulted non-absolute path %q", path)
		}
		if !strings.HasPrefix(path, "/usr/sbin/") && !strings.HasPrefix(path, "/usr/bin/") &&
			!strings.HasPrefix(path, "/sbin/") && !strings.HasPrefix(path, "/bin/") {
			t.Errorf("backend discovery consulted untrusted path %q", path)
		}
	}
}

func TestDetectBackendRejectsLegacyIPv4WithoutIPv6(t *testing.T) {
	candidate := Backend{
		Bin:          "/usr/sbin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/usr/sbin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	}
	available := map[string]bool{candidate.Bin: true}
	stat := func(path string) (os.FileInfo, error) {
		if available[path] {
			return backendFileInfo{mode: 0o755}, nil
		}
		return nil, os.ErrNotExist
	}

	if _, err := detectBackend([]Backend{candidate}, stat); err == nil {
		t.Fatal("legacy IPv4 backend without ip6tables was accepted")
	}

	available[candidate.IPv6Bin] = true
	got, err := detectBackend([]Backend{candidate}, stat)
	if err != nil {
		t.Fatalf("paired legacy backend rejected: %v", err)
	}
	if got.Bin != candidate.Bin || got.IPv6Bin != candidate.IPv6Bin {
		t.Errorf("detected backend = %#v, want paired legacy backend", got)
	}
}

type backendFileInfo struct {
	mode os.FileMode
}

func (i backendFileInfo) Name() string       { return "backend" }
func (i backendFileInfo) Size() int64        { return 0 }
func (i backendFileInfo) Mode() os.FileMode  { return i.mode }
func (i backendFileInfo) ModTime() time.Time { return time.Time{} }
func (i backendFileInfo) IsDir() bool        { return false }
func (i backendFileInfo) Sys() any           { return nil }
