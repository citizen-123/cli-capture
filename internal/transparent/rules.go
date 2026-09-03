package transparent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const ruleNamespacePrefix = "cli_capture_"

// RuleNamespace names all firewall objects owned by one transparent-listener
// instance. The random suffix keeps concurrently running instances from
// claiming or tearing down each other's rules.
type RuleNamespace string

// NewRuleNamespace creates a nftables-table / iptables-chain name that is safe
// for both backends and unique for the lifetime of a listener.
func NewRuleNamespace() (RuleNamespace, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("transparent: generate firewall rule namespace: %w", err)
	}
	return RuleNamespace(ruleNamespacePrefix + hex.EncodeToString(suffix[:])), nil
}

// NFTRedirect returns the nftables commands (each an argv without the leading
// "nft") that redirect uid's TCP 80/443 traffic to proxyPort across IPv4 and
// IPv6. The namespace is owned by this listener alone. ApplyRedirect rolls this
// full plan back if any command fails.
func NFTRedirect(namespace RuleNamespace, proxyPort, uid int) [][]string {
	return append(
		nftIPRedirect(namespace, proxyPort, uid),
		nftIP6Redirect(namespace, proxyPort, uid)...,
	)
}

func nftIPRedirect(namespace RuleNamespace, proxyPort, uid int) [][]string {
	return nftFamilyRedirect("ip", namespace, proxyPort, uid)
}

func nftIP6Redirect(namespace RuleNamespace, proxyPort, uid int) [][]string {
	return nftFamilyRedirect("ip6", namespace, proxyPort, uid)
}

func nftFamilyRedirect(family string, namespace RuleNamespace, proxyPort, uid int) [][]string {
	table := string(namespace)
	return [][]string{
		{"add", "table", family, table},
		{"add", "chain", family, table, "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"},
		{"add", "rule", family, table, "output", "meta", "skuid", strconv.Itoa(uid),
			"tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":" + strconv.Itoa(proxyPort)},
	}
}

// NFTFlush removes only the IPv4 and IPv6 rules owned by namespace.
func NFTFlush(namespace RuleNamespace) [][]string {
	return append(nftIP6Flush(namespace), nftIPFlush(namespace)...)
}

func nftIPFlush(namespace RuleNamespace) [][]string {
	return [][]string{{"delete", "table", "ip", string(namespace)}}
}

func nftIP6Flush(namespace RuleNamespace) [][]string {
	return [][]string{{"delete", "table", "ip6", string(namespace)}}
}

// IPTablesRedirect is the legacy-iptables equivalent of NFTRedirect. It uses
// namespace as its dedicated nat-table chain so teardown is exact.
func IPTablesRedirect(namespace RuleNamespace, proxyPort, uid int) [][]string {
	chain := string(namespace)
	return [][]string{
		{"-t", "nat", "-N", chain},
		{"-t", "nat", "-A", chain, "-p", "tcp", "-m", "owner", "--uid-owner", strconv.Itoa(uid),
			"-m", "multiport", "--dports", "80,443", "-j", "REDIRECT", "--to-ports", strconv.Itoa(proxyPort)},
		{"-t", "nat", "-A", "OUTPUT", "-j", chain},
	}
}

// IPTablesFlush removes only the rules owned by namespace.
func IPTablesFlush(namespace RuleNamespace) [][]string {
	chain := string(namespace)
	return [][]string{
		{"-t", "nat", "-D", "OUTPUT", "-j", chain},
		{"-t", "nat", "-F", chain},
		{"-t", "nat", "-X", chain},
	}
}

// IP6TablesRedirect is the ip6tables equivalent of IPTablesRedirect.
func IP6TablesRedirect(namespace RuleNamespace, proxyPort, uid int) [][]string {
	return IPTablesRedirect(namespace, proxyPort, uid)
}

// IP6TablesFlush removes only the rules owned by namespace.
func IP6TablesFlush(namespace RuleNamespace) [][]string {
	return IPTablesFlush(namespace)
}

// Backend is a firewall tool (nft or iptables) plus its rule builders. Legacy
// iptables backends also name an ip6tables companion; automatic application
// refuses a legacy backend without one rather than bypassing IPv6.
type Backend struct {
	Bin          string
	Redirect     func(namespace RuleNamespace, proxyPort, uid int) [][]string
	Flush        func(namespace RuleNamespace) [][]string
	IPv6Bin      string
	IPv6Redirect func(namespace RuleNamespace, proxyPort, uid int) [][]string
	IPv6Flush    func(namespace RuleNamespace) [][]string
}

// trustedBackends enumerates only system-managed absolute binary paths. Rule
// application runs with CAP_NET_ADMIN, so resolving a tool through the
// launcher's PATH would let an untrusted environment choose a root command.
var trustedBackends = []Backend{
	{
		Bin:          "/usr/sbin/nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "/usr/sbin/nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	},
	{
		Bin:          "/usr/bin/nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "/usr/bin/nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	},
	{
		Bin:          "/sbin/nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "/sbin/nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	},
	{
		Bin:          "/bin/nft",
		Redirect:     nftIPRedirect,
		Flush:        nftIPFlush,
		IPv6Bin:      "/bin/nft",
		IPv6Redirect: nftIP6Redirect,
		IPv6Flush:    nftIP6Flush,
	},
	{
		Bin:          "/usr/sbin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/usr/sbin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	},
	{
		Bin:          "/usr/bin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/usr/bin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	},
	{
		Bin:          "/sbin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/sbin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	},
	{
		Bin:          "/bin/iptables",
		Redirect:     IPTablesRedirect,
		Flush:        IPTablesFlush,
		IPv6Bin:      "/bin/ip6tables",
		IPv6Redirect: IP6TablesRedirect,
		IPv6Flush:    IP6TablesFlush,
	},
}

// DetectBackend prefers nftables, falls back to paired iptables/ip6tables, and
// never consults PATH. Every returned command is a fixed absolute path under a
// trusted system directory. A lone iptables binary is intentionally unusable:
// applying only its IPv4 rules would silently leave IPv6 outside the proxy.
func DetectBackend() (Backend, error) {
	return detectBackend(trustedBackends, os.Stat)
}

func detectBackend(candidates []Backend, stat func(string) (os.FileInfo, error)) (Backend, error) {
	for _, candidate := range candidates {
		if !executable(candidate.Bin, stat) {
			continue
		}
		if candidate.IPv6Bin != "" && !executable(candidate.IPv6Bin, stat) {
			continue
		}
		return candidate, nil
	}
	return Backend{}, errors.New("transparent: neither nft nor paired iptables/ip6tables found in trusted system directories")
}

func executable(path string, stat func(string) (os.FileInfo, error)) bool {
	info, err := stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// Shell renders argv command lists as copy-pasteable "<bin> …" lines for logging.
func Shell(bin string, cmds [][]string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = bin + " " + strings.Join(c, " ")
	}
	return out
}
