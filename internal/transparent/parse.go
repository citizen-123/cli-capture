// Package transparent implements transparent (redirect-based) capture for
// targets that ignore proxy environment variables. On Linux, a netfilter
// REDIRECT rule sends the target's TCP traffic to a local listener; the kernel
// preserves the pre-redirect destination, which we read via SO_ORIGINAL_DST and
// feed into the same MITM pipeline as the CONNECT proxy.
//
// This requires CAP_NET_ADMIN (root) to install the redirect rule, so the
// privileged path cannot run in an unprivileged sandbox — the parsing and rule
// generation here are unit-tested independently of it.
package transparent

import (
	"fmt"
	"net"
)

// parseSockaddrIn extracts "ip:port" from the raw bytes of a struct sockaddr_in
// as returned by getsockopt(SO_ORIGINAL_DST). Layout: [0:2] family, [2:4] port
// (network byte order), [4:8] IPv4 address.
func parseSockaddrIn(b []byte) (string, error) {
	if len(b) < 8 {
		return "", fmt.Errorf("transparent: short sockaddr (%d bytes)", len(b))
	}
	port := int(b[2])<<8 | int(b[3])
	ip := net.IPv4(b[4], b[5], b[6], b[7])
	return fmt.Sprintf("%s:%d", ip, port), nil
}

// parseSockaddrIn6 extracts "[ip]:port" from a struct sockaddr_in6. Layout:
// [0:2] family, [2:4] port (network byte order), [4:8] flowinfo, [8:24] IPv6
// address, [24:28] scope id.
func parseSockaddrIn6(b []byte) (string, error) {
	if len(b) < 24 {
		return "", fmt.Errorf("transparent: short sockaddr6 (%d bytes)", len(b))
	}
	port := int(b[2])<<8 | int(b[3])
	ip := make(net.IP, net.IPv6len)
	copy(ip, b[8:24])
	return fmt.Sprintf("[%s]:%d", ip, port), nil
}
