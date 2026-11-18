//go:build !windows

package proxy

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/smallstep/truststore"
)

// TestUninstallNSSLeavesTheSystemStoreAlone checks that the NSS-only uninstall
// asks the library to skip the system store.
func TestUninstallNSSLeavesTheSystemStoreAlone(t *testing.T) {
	orig := trustUninstall
	t.Cleanup(func() { trustUninstall = orig })
	var calls int
	trustUninstall = func(*x509.Certificate, ...truststore.Option) error { calls++; return nil }
	if err := UninstallNSS(&x509.Certificate{}); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// TestNSSDatabasesFindsProfilesAndTheSharedDatabase checks that only
// directories holding a certificate database are listed.
func TestNSSDatabasesFindsProfilesAndTheSharedDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orig := truststore.NSSProfile
	truststore.NSSProfile = filepath.Join(home, ".mozilla", "firefox", "*")
	t.Cleanup(func() { truststore.NSSProfile = orig })
	mk := func(dir, db string) string {
		p := filepath.Join(home, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if db != "" {
			if err := os.WriteFile(filepath.Join(p, db), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	a := mk(".mozilla/firefox/a.default", "cert9.db")
	mk(".mozilla/firefox/empty", "")
	shared := mk(".pki/nssdb", "cert9.db")
	if got, want := nssDatabases(), []string{a, shared}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nssDatabases = %v, want %v", got, want)
	}
}
