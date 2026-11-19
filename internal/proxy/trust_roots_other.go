//go:build !linux

package proxy

import "crypto/x509"

// systemRoots has nothing to add off Linux. macOS and Windows verify through
// the operating system on every call (SecTrustEvaluate and the CryptoAPI chain
// engine), so a CA trusted after start is already seen.
func systemRoots() (*x509.CertPool, bool) { return nil, false }
