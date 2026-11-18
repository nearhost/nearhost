package proxy

import "crypto/x509"

// SystemHolds reports whether the System keychain holds the root, whether or
// not it trusts it.
func SystemHolds(cert *x509.Certificate) bool { return keychainHolds(cert, systemKeychain) }

// StillTrusted reports whether the system still trusts the root.
func StillTrusted(cert *x509.Certificate) bool { return IsTrusted(cert) }
