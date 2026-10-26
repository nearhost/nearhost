package proxy

import "crypto/x509"

// removeSystemCopy deletes the root from the System keychain once its trust
// settings are gone. Tests replace it.
var removeSystemCopy = func(cert *x509.Certificate) error {
	return deleteFromKeychain(cert, systemKeychain)
}
