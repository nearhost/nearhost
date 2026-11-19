package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSignedRoot makes a CA certificate and its PEM.
func selfSignedRoot(t *testing.T, name string) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// resetRootsCache forgets the parsed pool for the test and after it.
func resetRootsCache(t *testing.T) {
	t.Helper()
	clear := func() {
		rootsCache.Lock()
		rootsCache.sig, rootsCache.pool = "", nil
		rootsCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestIsTrustedSeesACAAddedAfterTheFirstCheck(t *testing.T) {
	cert, _ := selfSignedRoot(t, "Hook Test CA")
	pool := x509.NewCertPool()
	old := systemRootPool
	systemRootPool = func() (*x509.CertPool, error) { return pool, nil }
	t.Cleanup(func() { systemRootPool = old })

	if IsTrusted(cert) {
		t.Fatal("trusted before the bundle had the CA")
	}
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	if !IsTrusted(cert) {
		t.Fatal("still not trusted after the bundle gained the CA")
	}
}

func TestIsTrustedFallsBackWhenTheBundleCannotBeRead(t *testing.T) {
	cert, _ := selfSignedRoot(t, "Fallback Test CA")
	old := systemRootPool
	systemRootPool = func() (*x509.CertPool, error) { return nil, errors.New("no bundle") }
	t.Cleanup(func() { systemRootPool = old })

	if IsTrusted(cert) {
		t.Fatal("an unknown CA was trusted with no readable bundle")
	}
}

func TestFreshSystemRootsReadsFilesAfterTheyChange(t *testing.T) {
	resetRootsCache(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.pem")
	empty := filepath.Join(dir, "certs.d")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", bundle)
	t.Setenv("SSL_CERT_DIR", empty)

	first, firstPEM := selfSignedRoot(t, "First CA")
	second, secondPEM := selfSignedRoot(t, "Second CA")
	if err := os.WriteFile(bundle, firstPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	oldRead := readCertFile
	readCertFile = func(name string) ([]byte, error) { reads++; return os.ReadFile(name) }
	t.Cleanup(func() { readCertFile = oldRead })

	if !IsTrusted(first) || IsTrusted(second) {
		t.Fatal("the first bundle should hold only the first CA")
	}
	for i := 0; i < 5; i++ {
		IsTrusted(first)
	}
	if reads != 1 {
		t.Fatalf("an unchanged bundle was read %d times, want 1", reads)
	}

	// The system rewrites the bundle: new size and mtime.
	if err := os.WriteFile(bundle, append(firstPEM, secondPEM...), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(bundle, future, future); err != nil {
		t.Fatal(err)
	}
	if !IsTrusted(second) {
		t.Fatal("the rewritten bundle was not read again")
	}
	if reads != 2 {
		t.Fatalf("a changed bundle was read %d times in total, want 2", reads)
	}
}

func TestFreshSystemRootsUsesTheFirstBundleThenTheDirectories(t *testing.T) {
	resetRootsCache(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.pem")
	certs := filepath.Join(dir, "certs.d")
	if err := os.Mkdir(certs, 0o700); err != nil {
		t.Fatal(err)
	}
	inBundle, bundlePEM := selfSignedRoot(t, "Bundle CA")
	inDir, dirPEM := selfSignedRoot(t, "Directory CA")
	if err := os.WriteFile(bundle, bundlePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certs, "one.pem"), dirPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", bundle)
	t.Setenv("SSL_CERT_DIR", certs)
	if !IsTrusted(inBundle) || !IsTrusted(inDir) {
		t.Fatal("both the bundle and the directory should count")
	}
}

func TestFreshSystemRootsErrorsWhenNothingReads(t *testing.T) {
	resetRootsCache(t)
	dir := t.TempDir()
	// A directory where a bundle file is expected cannot be read as a file.
	t.Setenv("SSL_CERT_FILE", dir)
	t.Setenv("SSL_CERT_DIR", filepath.Join(dir, "missing"))
	if _, err := freshSystemRoots(); err == nil {
		t.Fatal("expected an error when the only bundle cannot be read")
	}
}
