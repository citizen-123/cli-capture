//go:build linux

package transparent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// ApplyRedirect installs listener-owned redirect rules for uid→proxyPort using
// whichever backend is available (nft preferred, else paired
// iptables/ip6tables). It returns a teardown function that deletes only that
// listener's rules. Requires root (CAP_NET_ADMIN). A partially-applied ruleset
// is rolled back across both IP families. Returns the primary backend binary
// used.
func ApplyRedirect(proxyPort, uid int) (teardown func() error, backend string, err error) {
	if os.Geteuid() != 0 {
		return nil, "", fmt.Errorf("transparent: applying rules needs root (CAP_NET_ADMIN)")
	}
	be, err := DetectBackend()
	if err != nil {
		return nil, "", err
	}
	namespace, err := NewRuleNamespace()
	if err != nil {
		return nil, be.Bin, err
	}

	teardown, err = applyBackend(be, namespace, proxyPort, uid, runOne)
	if err != nil {
		return nil, be.Bin, err
	}
	return teardown, be.Bin, nil
}

type firewallCommand struct {
	bin  string
	argv []string
}

type commandRunner func(bin string, argv []string) error

func applyBackend(be Backend, namespace RuleNamespace, proxyPort, uid int, run commandRunner) (func() error, error) {
	families, err := be.commandFamilies(namespace, proxyPort, uid)
	if err != nil {
		return nil, err
	}

	owned := make([]firewallFamily, 0, len(families))
	for _, family := range families {
		for index, command := range family.redirect {
			if err := run(command.bin, command.argv); err != nil {
				_ = cleanupFamilies(owned, run)
				return nil, err
			}
			// A successful first command creates this backend family's private
			// table or chain. Only then is it safe to delete it on rollback.
			if index == 0 {
				owned = append(owned, family)
			}
		}
	}
	var teardownOnce sync.Once
	var teardownErr error
	return func() error {
		teardownOnce.Do(func() {
			teardownErr = cleanupFamilies(owned, run)
		})
		return teardownErr
	}, nil
}

type firewallFamily struct {
	redirect []firewallCommand
	flush    []firewallCommand
}

func (be Backend) commandPlan(namespace RuleNamespace, proxyPort, uid int) (redirect, flush []firewallCommand, err error) {
	families, err := be.commandFamilies(namespace, proxyPort, uid)
	if err != nil {
		return nil, nil, err
	}
	for _, family := range families {
		redirect = append(redirect, family.redirect...)
	}
	for index := len(families) - 1; index >= 0; index-- {
		flush = append(flush, families[index].flush...)
	}
	return redirect, flush, nil
}

func (be Backend) commandFamilies(namespace RuleNamespace, proxyPort, uid int) ([]firewallFamily, error) {
	if be.Bin == "" || be.Redirect == nil || be.Flush == nil {
		return nil, errors.New("transparent: incomplete firewall backend")
	}
	families := []firewallFamily{{
		redirect: commandList(be.Bin, be.Redirect(namespace, proxyPort, uid)),
		flush:    commandList(be.Bin, be.Flush(namespace)),
	}}
	if be.IPv6Bin == "" {
		return families, nil
	}
	if be.IPv6Redirect == nil || be.IPv6Flush == nil {
		return nil, errors.New("transparent: incomplete IPv6 firewall backend")
	}
	families = append(families, firewallFamily{
		redirect: commandList(be.IPv6Bin, be.IPv6Redirect(namespace, proxyPort, uid)),
		flush:    commandList(be.IPv6Bin, be.IPv6Flush(namespace)),
	})
	return families, nil
}

func cleanupFamilies(families []firewallFamily, run commandRunner) error {
	var firstErr error
	for index := len(families) - 1; index >= 0; index-- {
		if err := runAll(families[index].flush, run); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func commandList(bin string, commands [][]string) []firewallCommand {
	out := make([]firewallCommand, len(commands))
	for i, argv := range commands {
		out[i] = firewallCommand{bin: bin, argv: argv}
	}
	return out
}

func runAll(commands []firewallCommand, run commandRunner) error {
	var firstErr error
	for _, command := range commands {
		if err := run(command.bin, command.argv); err != nil && firstErr == nil {
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
