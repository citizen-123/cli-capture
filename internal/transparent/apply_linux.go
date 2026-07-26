//go:build linux

package transparent

import (
	"fmt"
	"os"
	"os/exec"
)

// ApplyRedirect installs the redirect rules for uid→proxyPort using whichever
// backend is available (nft preferred, else iptables) and returns a teardown
// function. Requires root (CAP_NET_ADMIN). Any stale cli_capture rules are
// flushed first, and a partially-applied ruleset is rolled back on error — so
// the firewall is never left half-configured. Returns the backend binary used.
func ApplyRedirect(proxyPort, uid int) (teardown func() error, backend string, err error) {
	if os.Geteuid() != 0 {
		return nil, "", fmt.Errorf("transparent: applying rules needs root (CAP_NET_ADMIN)")
	}
	be, err := DetectBackend()
	if err != nil {
		return nil, "", err
	}

	_ = runAll(be.Bin, be.Flush()) // best-effort clear of stale rules

	for _, argv := range be.Redirect(proxyPort, uid) {
		if err := runOne(be.Bin, argv); err != nil {
			_ = runAll(be.Bin, be.Flush()) // roll back the partial application
			return nil, be.Bin, err
		}
	}
	return func() error { return runAll(be.Bin, be.Flush()) }, be.Bin, nil
}

func runAll(bin string, cmds [][]string) error {
	var firstErr error
	for _, argv := range cmds {
		if err := runOne(bin, argv); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runOne(bin string, argv []string) error {
	out, err := exec.Command(bin, argv...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v: %s", bin, argv, err, out)
	}
	return nil
}
