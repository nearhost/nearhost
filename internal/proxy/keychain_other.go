//go:build !darwin

package proxy

import "crypto/x509"

// removeSystemCopy has nothing to do: the trust store library's uninstall
// already deletes the anchor file it wrote. Tests replace it.
var removeSystemCopy = func(*x509.Certificate) error { return nil }

// copyOnlyWhenUntrusted is false here: the library's uninstall is the whole
// removal and a failure of it is the outcome.
var copyOnlyWhenUntrusted = false
