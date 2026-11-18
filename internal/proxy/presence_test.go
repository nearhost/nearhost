package proxy

import (
	"crypto/x509"
	"errors"
	"testing"
)

func TestKeychainHoldsReadsTheListing(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("root")}
	fakeSecurity(t, "SHA-1 hash: "+keychainHash(cert)+"\n", nil)
	if !keychainHolds(cert, "/k") {
		t.Error("keychain with the root: want true")
	}
	fakeSecurity(t, "SHA-1 hash: "+keychainHash(cert)+"00\n", nil)
	if keychainHolds(cert, "/k") {
		t.Error("keychain with another certificate: want false")
	}
	fakeSecurity(t, "", errors.New("exit status 1"))
	if !keychainHolds(cert, "/k") {
		t.Error("unlistable keychain: want true, so the removal is attempted")
	}
}
