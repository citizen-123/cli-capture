//go:build linux

package transparent

import (
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ip6OriginalDst is IPV6_ORIGINAL_DST; x/sys/unix does not name it.
const ip6OriginalDst = 80

// OriginalDst returns the pre-redirect destination of a transparently-proxied
// connection by reading SO_ORIGINAL_DST (IPv4) or IPV6_ORIGINAL_DST (IPv6) from
// the socket, which the kernel's NAT REDIRECT preserves.
func OriginalDst(conn *net.TCPConn) (string, error) {
	level, opt, size := unix.SOL_IP, unix.SO_ORIGINAL_DST, 16
	if isIPv6(conn) {
		level, opt, size = unix.SOL_IPV6, ip6OriginalDst, 28
	}

	raw, err := conn.SyscallConn()
	if err != nil {
		return "", err
	}
	sa := make([]byte, size)
	var sysErr syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		l := uint32(size)
		_, _, e := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(level),
			uintptr(opt),
			uintptr(unsafe.Pointer(&sa[0])),
			uintptr(unsafe.Pointer(&l)),
			0,
		)
		sysErr = e
	}); err != nil {
		return "", err
	}
	if sysErr != 0 {
		return "", sysErr
	}
	if size == 28 {
		return parseSockaddrIn6(sa)
	}
	return parseSockaddrIn(sa)
}

// isIPv6 reports whether the socket is a native IPv6 socket (not a v4-mapped one).
func isIPv6(conn *net.TCPConn) bool {
	la, ok := conn.LocalAddr().(*net.TCPAddr)
	return ok && la.IP.To4() == nil && la.IP.To16() != nil
}
