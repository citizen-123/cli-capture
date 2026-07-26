//go:build !linux

package transparent

import (
	"errors"
	"net"
)

// OriginalDst is unsupported off Linux — SO_ORIGINAL_DST is a netfilter feature.
func OriginalDst(_ *net.TCPConn) (string, error) {
	return "", errors.New("transparent: capture is only supported on Linux")
}
