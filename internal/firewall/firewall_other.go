//go:build !windows

package firewall

import "errors"

// Needed is whether this OS needs a firewall rule for networked mode.
const Needed = false

// Ready is true: only Windows needs a rule.
func Ready() bool { return true }

// Netsh fails: there is no netsh here.
func Netsh(args ...string) ([]byte, error) {
	return nil, errors.ErrUnsupported
}
