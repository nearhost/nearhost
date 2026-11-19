//go:build !linux && !darwin

package proxy

import "crypto/x509"

// SystemHolds is always true here: Windows has no cheap presence check, and
// asking its uninstall to remove a root that is absent is harmless (it reports
// not found, with no security warning and no elevation).
func SystemHolds(*x509.Certificate) bool { return true }

// StillTrusted reports whether the system still trusts the root.
func StillTrusted(cert *x509.Certificate) bool { return IsTrusted(cert) }

// AnchorHelp has nothing to name here.
func AnchorHelp() (dir, update string) { return "", "" }
