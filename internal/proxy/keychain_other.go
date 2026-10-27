//go:build !darwin

package proxy

import "crypto/x509"

// removeSystemCopy has nothing to do: the trust store library's uninstall
// already deletes the anchor file it wrote. Tests replace it.
var removeSystemCopy = func(*x509.Certificate) error { return nil }
