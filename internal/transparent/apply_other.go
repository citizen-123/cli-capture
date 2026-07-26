//go:build !linux

package transparent

import "errors"

// ApplyRedirect is unsupported off Linux (netfilter is a Linux feature).
func ApplyRedirect(_, _ int) (func() error, string, error) {
	return nil, "", errors.New("transparent: rule application is only supported on Linux")
}
