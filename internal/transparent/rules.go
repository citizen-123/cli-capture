package transparent

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// NFTRedirect returns the nftables commands (each an argv without the leading
// "nft") that redirect uid's TCP 80/443 traffic to proxyPort. A dedicated
// cli_capture table keeps teardown scoped. Requires CAP_NET_ADMIN.
func NFTRedirect(proxyPort, uid int) [][]string {
	return [][]string{
		{"add", "table", "ip", "cli_capture"},
		{"add", "chain", "ip", "cli_capture", "output", "{", "type", "nat", "hook", "output", "priority", "-100", ";", "}"},
		{"add", "rule", "ip", "cli_capture", "output", "meta", "skuid", strconv.Itoa(uid),
			"tcp", "dport", "{", "80", ",", "443", "}", "redirect", "to", ":" + strconv.Itoa(proxyPort)},
	}
}

// NFTFlush removes the rules NFTRedirect installed.
func NFTFlush() [][]string {
	return [][]string{{"delete", "table", "ip", "cli_capture"}}
}

// IPTablesRedirect is the legacy-iptables equivalent of NFTRedirect. It uses a
// dedicated cli_capture chain in the nat table so teardown is exact.
func IPTablesRedirect(proxyPort, uid int) [][]string {
	return [][]string{
		{"-t", "nat", "-N", "cli_capture"},
		{"-t", "nat", "-A", "cli_capture", "-p", "tcp", "-m", "owner", "--uid-owner", strconv.Itoa(uid),
			"-m", "multiport", "--dports", "80,443", "-j", "REDIRECT", "--to-ports", strconv.Itoa(proxyPort)},
		{"-t", "nat", "-A", "OUTPUT", "-j", "cli_capture"},
	}
}

// IPTablesFlush removes the rules IPTablesRedirect installed.
func IPTablesFlush() [][]string {
	return [][]string{
		{"-t", "nat", "-D", "OUTPUT", "-j", "cli_capture"},
		{"-t", "nat", "-F", "cli_capture"},
		{"-t", "nat", "-X", "cli_capture"},
	}
}

// Backend is a firewall tool (nft or iptables) plus its rule builders, so the
// apply/log code is written once against whichever is available.
type Backend struct {
	Bin      string
	Redirect func(proxyPort, uid int) [][]string
	Flush    func() [][]string
}

// DetectBackend prefers nftables, falls back to iptables, and errors if neither
// is on PATH.
func DetectBackend() (Backend, error) {
	if _, err := exec.LookPath("nft"); err == nil {
		return Backend{Bin: "nft", Redirect: NFTRedirect, Flush: NFTFlush}, nil
	}
	if _, err := exec.LookPath("iptables"); err == nil {
		return Backend{Bin: "iptables", Redirect: IPTablesRedirect, Flush: IPTablesFlush}, nil
	}
	return Backend{}, errors.New("transparent: neither nft nor iptables found on PATH")
}

// Shell renders argv command lists as copy-pasteable "<bin> …" lines for logging.
func Shell(bin string, cmds [][]string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = bin + " " + strings.Join(c, " ")
	}
	return out
}
