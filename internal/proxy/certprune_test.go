package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// pruneNow is the time the prune tests take as now.
var pruneNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// testCertPEM returns a PEM certificate for name that expires at notAfter,
// signed by a key made for the test.
func testCertPEM(t *testing.T, name string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    notAfter.Add(-leafLifetime),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// writeFile writes data to path, making its directory.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// leafDir is where certmagic keeps the local CA's certificate for name.
func leafDir(storage, name string) string {
	return filepath.Join(storage, "certificates", "local", name)
}

// writeLeaf stores a certificate for name as certmagic does for the local
// CA: a directory of its own holding the chain, the key and the metadata.
// crt is the chain file's contents. It returns the directory.
func writeLeaf(t *testing.T, storage, name string, crt []byte) string {
	t.Helper()
	dir := leafDir(storage, name)
	writeFile(t, filepath.Join(dir, name+".crt"), crt)
	writeFile(t, filepath.Join(dir, name+".key"), []byte("not a real key"))
	writeFile(t, filepath.Join(dir, name+".json"), []byte(`{"sans":["`+name+`"]}`))
	return dir
}

// exists reports whether path is there, without following a symlink.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// recordLog sends the log to a recorder for the rest of the test.
func recordLog(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	SetSink(rec)
	t.Cleanup(func() { SetSink(nil) })
	return rec
}

// A certificate that expired more than a day ago goes, directory and key
// with it. One still valid stays, and so does the local CA, whose expired
// intermediate is another task's to replace, and the storage's lock files.
func TestPruneRemovesExpiredLeavesOnly(t *testing.T) {
	storage := t.TempDir()
	recordLog(t)
	expired := writeLeaf(t, storage, "pr-1234.myapp.test", testCertPEM(t, "pr-1234.myapp.test", pruneNow.Add(-48*time.Hour)))
	valid := writeLeaf(t, storage, "main.myapp.test", testCertPEM(t, "main.myapp.test", pruneNow.Add(100*time.Hour)))
	ca := filepath.Dir(RootCertPath(storage))
	writeFile(t, filepath.Join(ca, "root.crt"), testCertPEM(t, "root", pruneNow.Add(-1000*time.Hour)))
	writeFile(t, filepath.Join(ca, "root.key"), []byte("not a real key"))
	writeFile(t, filepath.Join(ca, "intermediate.crt"), testCertPEM(t, "intermediate", pruneNow.Add(-1000*time.Hour)))
	writeFile(t, filepath.Join(ca, "intermediate.key"), []byte("not a real key"))
	lock := filepath.Join(storage, "locks", "issue_cert_pr-1234.myapp.test.lock")
	writeFile(t, lock, []byte("{}"))

	pruneExpiredCertificates(storage, pruneNow)

	if exists(t, expired) {
		t.Error("the expired certificate's directory is still there")
	}
	for _, path := range []string{
		filepath.Join(valid, "main.myapp.test.crt"),
		filepath.Join(valid, "main.myapp.test.key"),
		filepath.Join(valid, "main.myapp.test.json"),
		filepath.Join(ca, "root.crt"), filepath.Join(ca, "root.key"),
		filepath.Join(ca, "intermediate.crt"), filepath.Join(ca, "intermediate.key"),
		lock,
		filepath.Join(storage, "certificates", "local"),
	} {
		if !exists(t, path) {
			t.Errorf("%s was removed", path)
		}
	}
}

// A certificate that expired within the last day stays: Caddy may be about
// to renew it, and a clock that was just set back a little is no reason to
// take it.
func TestPruneKeepsACertificateWithinTheMargin(t *testing.T) {
	storage := t.TempDir()
	recordLog(t)
	recent := writeLeaf(t, storage, "a.myapp.test", testCertPEM(t, "a.myapp.test", pruneNow.Add(-expiredCertMargin+time.Minute)))
	old := writeLeaf(t, storage, "b.myapp.test", testCertPEM(t, "b.myapp.test", pruneNow.Add(-expiredCertMargin-time.Minute)))

	pruneExpiredCertificates(storage, pruneNow)

	if !exists(t, recent) {
		t.Error("a certificate expired for less than the margin was removed")
	}
	if exists(t, old) {
		t.Error("a certificate expired for longer than the margin is still there")
	}
}

// The chain file holds the leaf first, and the leaf's expiry is the one
// that counts.
func TestPruneReadsTheLeafOfAChain(t *testing.T) {
	storage := t.TempDir()
	recordLog(t)
	expiredLeaf := writeLeaf(t, storage, "a.myapp.test", append(
		testCertPEM(t, "a.myapp.test", pruneNow.Add(-48*time.Hour)),
		testCertPEM(t, "intermediate", pruneNow.Add(500*time.Hour))...))
	validLeaf := writeLeaf(t, storage, "b.myapp.test", append(
		testCertPEM(t, "b.myapp.test", pruneNow.Add(100*time.Hour)),
		testCertPEM(t, "intermediate", pruneNow.Add(-500*time.Hour))...))

	pruneExpiredCertificates(storage, pruneNow)

	if exists(t, expiredLeaf) {
		t.Error("an expired leaf with a valid intermediate after it is still there")
	}
	if !exists(t, validLeaf) {
		t.Error("a valid leaf with an expired intermediate after it was removed")
	}
}

// A directory that does not hold exactly what certmagic writes for a
// certificate is left alone, and the log says why in one line.
func TestPruneLeavesUnexpectedDirectoriesAlone(t *testing.T) {
	expired := pruneNow.Add(-48 * time.Hour)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, storage string) string
		want  string
	}{
		{"unparsable", func(t *testing.T, storage string) string {
			return writeLeaf(t, storage, "d.myapp.test", []byte("not a certificate"))
		}, "has d.myapp.test.crt, which holds no certificate"},
		{"no crt", func(t *testing.T, storage string) string {
			dir := writeLeaf(t, storage, "d.myapp.test", testCertPEM(t, "d.myapp.test", expired))
			if err := os.Remove(filepath.Join(dir, "d.myapp.test.crt")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "has no d.myapp.test.crt"},
		{"extra file", func(t *testing.T, storage string) string {
			dir := writeLeaf(t, storage, "d.myapp.test", testCertPEM(t, "d.myapp.test", expired))
			writeFile(t, filepath.Join(dir, "notes.txt"), []byte("mine"))
			return dir
		}, "holds notes.txt"},
		{"subfolder", func(t *testing.T, storage string) string {
			dir := writeLeaf(t, storage, "d.myapp.test", testCertPEM(t, "d.myapp.test", expired))
			if err := os.Remove(filepath.Join(dir, "d.myapp.test.json")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "d.myapp.test.json", "file"), []byte("mine"))
			return dir
		}, "holds d.myapp.test.json, which is not a file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			rec := recordLog(t)
			dir := tc.setup(t, storage)
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}

			pruneExpiredCertificates(storage, pruneNow)

			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("the folder went: %v", err)
			}
			if len(after) != len(before) {
				t.Errorf("the folder held %d entries and holds %d", len(before), len(after))
			}
			want := " Left the certificate folder d.myapp.test in " + filepath.Join(storage, "certificates", "local") + " alone, as it " + tc.want + "."
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.lines) != 1 || rec.lines[0] != want {
				t.Errorf("logged %q, want [%q]", rec.lines, want)
			}
		})
	}
}

// Several folders left alone make one line, which counts them and names
// one.
func TestPruneLogsFoldersLeftAloneOnce(t *testing.T) {
	storage := t.TempDir()
	rec := recordLog(t)
	writeLeaf(t, storage, "a.myapp.test", []byte("not a certificate"))
	writeLeaf(t, storage, "b.myapp.test", []byte("not a certificate"))
	stray := filepath.Join(storage, "certificates", "local", "stray-file")
	writeFile(t, stray, []byte("mine"))

	pruneExpiredCertificates(storage, pruneNow)

	if !exists(t, stray) {
		t.Error("a stray file among the certificates was removed")
	}
	want := " Left 2 certificate folders in " + filepath.Join(storage, "certificates", "local") + " alone; a.myapp.test, for one, has a.myapp.test.crt, which holds no certificate."
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.lines) != 1 || rec.lines[0] != want {
		t.Errorf("logged %q, want [%q]", rec.lines, want)
	}
}

// A removal that failed part way, on Windows for a file another program
// held, leaves the .crt, or once that went an empty folder. The next start
// finishes either, and says nothing about the empty one.
func TestPruneFinishesAnEarlierPartialRemoval(t *testing.T) {
	storage := t.TempDir()
	rec := recordLog(t)
	crtOnly := leafDir(storage, "a.myapp.test")
	writeFile(t, filepath.Join(crtOnly, "a.myapp.test.crt"), testCertPEM(t, "a.myapp.test", pruneNow.Add(-48*time.Hour)))
	empty := leafDir(storage, "b.myapp.test")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}

	pruneExpiredCertificates(storage, pruneNow)

	if exists(t, crtOnly) {
		t.Error("a folder holding only an expired .crt is still there")
	}
	if exists(t, empty) {
		t.Error("an empty certificate folder is still there")
	}
	want := " Removed 1 expired certificate from the proxy's storage."
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.lines) != 1 || rec.lines[0] != want {
		t.Errorf("logged %q, want [%q]", rec.lines, want)
	}
}

// A certificate's folder swapped for a symlink after it was listed, to
// another folder inside the storage that holds files of the same names, is
// left alone, and so is what the link points at: the CA's folder, outside
// the issuer's, or a sibling certificate folder.
func TestPruneLeavesAFolderSwappedForASymlinkAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string // relative to the storage
		link   string // the symlink's text, relative to the folder's parent
		want   string
	}{
		{"to the CA", filepath.Join("pki", "authorities", "local"), filepath.Join("..", "..", "pki", "authorities", "local"),
			"Left the certificate folder a.myapp.test in %s alone, as it can't be opened"},
		{"to a sibling", filepath.Join("certificates", "local", "other"), "other",
			"Left 2 certificate folders in %s alone; a.myapp.test, for one, changed while it was checked."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			rec := recordLog(t)
			name := "a.myapp.test"
			dir := writeLeaf(t, storage, name, testCertPEM(t, name, pruneNow.Add(-48*time.Hour)))
			target := filepath.Join(storage, tc.target)
			writeFile(t, filepath.Join(target, name+".crt"), testCertPEM(t, name, pruneNow.Add(-48*time.Hour)))
			writeFile(t, filepath.Join(target, name+".key"), []byte("not a real key"))
			moved := filepath.Join(storage, "moved")
			beforeCertDir = func(n string) {
				if n != name {
					return
				}
				if err := os.Rename(dir, moved); err != nil {
					t.Error(err)
					return
				}
				symlinkOrSkip(t, tc.link, dir)
			}
			t.Cleanup(func() { beforeCertDir = func(string) {} })

			pruneExpiredCertificates(storage, pruneNow)

			for _, f := range []string{name + ".crt", name + ".key"} {
				if !exists(t, filepath.Join(target, f)) {
					t.Errorf("%s was removed through the symlink", f)
				}
				if !exists(t, filepath.Join(moved, f)) {
					t.Errorf("the moved folder lost %s", f)
				}
			}
			if !exists(t, dir) {
				t.Error("the symlink was removed")
			}
			want := " " + fmt.Sprintf(tc.want, filepath.Join(storage, "certificates", "local"))
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.lines) != 1 || !strings.HasPrefix(rec.lines[0], want) {
				t.Errorf("logged %q, want one line starting %q", rec.lines, want)
			}
		})
	}
}

// A symlink where a certificate's directory belongs is not followed, and
// neither is one in place of the issuer's directory: what they point at is
// outside the storage, or may be.
func TestPruneDoesNotFollowSymlinks(t *testing.T) {
	storage := t.TempDir()
	recordLog(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "elsewhere.myapp.test")
	crt := filepath.Join(target, "elsewhere.myapp.test.crt")
	writeFile(t, crt, testCertPEM(t, "elsewhere.myapp.test", pruneNow.Add(-48*time.Hour)))
	writeFile(t, filepath.Join(target, "elsewhere.myapp.test.key"), []byte("not a real key"))
	link := leafDir(storage, "elsewhere.myapp.test")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, target, link)

	// A second storage whose issuer directory is itself a link.
	storage2 := t.TempDir()
	issuer := filepath.Join(outside, "issuer")
	inner := filepath.Join(issuer, "x.myapp.test", "x.myapp.test.crt")
	writeFile(t, inner, testCertPEM(t, "x.myapp.test", pruneNow.Add(-48*time.Hour)))
	if err := os.MkdirAll(filepath.Join(storage2, "certificates"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, issuer, filepath.Join(storage2, "certificates", "local"))

	pruneExpiredCertificates(storage, pruneNow)
	pruneExpiredCertificates(storage2, pruneNow)

	if !exists(t, crt) {
		t.Error("an expired certificate behind a symlinked directory was removed")
	}
	if !exists(t, link) {
		t.Error("the symlink was removed")
	}
	if !exists(t, inner) {
		t.Error("an expired certificate behind a symlinked issuer directory was removed")
	}
}

// symlinkOrSkip links link to target, or skips the test where the user may
// not make symlinks: Windows without Developer Mode or the privilege.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("this Windows user cannot make symlinks: %v", err)
		}
		t.Fatal(err)
	}
}

// Certificates from an issuer other than the local CA are not Nearhost's
// to remove: it could not issue them again.
func TestPruneKeepsOtherIssuers(t *testing.T) {
	storage := t.TempDir()
	recordLog(t)
	dir := filepath.Join(storage, "certificates", "acme-v02.api.letsencrypt.org-directory", "x.example.com")
	writeFile(t, filepath.Join(dir, "x.example.com.crt"), testCertPEM(t, "x.example.com", pruneNow.Add(-48*time.Hour)))

	pruneExpiredCertificates(storage, pruneNow)

	if !exists(t, filepath.Join(dir, "x.example.com.crt")) {
		t.Error("another issuer's certificate was removed")
	}
}

// The log has one line saying how many certificates went, and nothing when
// none did or there is no storage yet.
func TestPruneLogsOneSummaryLine(t *testing.T) {
	storage := t.TempDir()
	rec := recordLog(t)

	pruneExpiredCertificates(filepath.Join(storage, "missing"), pruneNow)
	pruneExpiredCertificates(storage, pruneNow)
	writeLeaf(t, storage, "valid.myapp.test", testCertPEM(t, "valid.myapp.test", pruneNow.Add(time.Hour)))
	pruneExpiredCertificates(storage, pruneNow)
	rec.mu.Lock()
	if len(rec.lines) != 0 {
		t.Errorf("logged %q with nothing to remove, want nothing", rec.lines)
	}
	rec.mu.Unlock()

	for _, name := range []string{"a.myapp.test", "b.myapp.test", "c.myapp.test"} {
		writeLeaf(t, storage, name, testCertPEM(t, name, pruneNow.Add(-48*time.Hour)))
	}
	pruneExpiredCertificates(storage, pruneNow)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if want := " Removed 3 expired certificates from the proxy's storage."; len(rec.lines) != 1 || rec.lines[0] != want {
		t.Errorf("logged %q, want [%q]", rec.lines, want)
	}
}

// The proxy prunes as it starts, before Caddy loads anything from storage,
// and not on the reloads that follow, while Caddy may be using a
// certificate's files.
func TestApplyPrunesExpiredCertificatesAtStartOnly(t *testing.T) {
	storage := testutil.TempDir(t, "nh-caddy-")
	recordLog(t)
	now := time.Now()
	before := writeLeaf(t, storage, "before.myapp.test", testCertPEM(t, "before.myapp.test", now.Add(-48*time.Hour)))

	p := New(Options{StorageRoot: storage})
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	if exists(t, before) {
		t.Error("the proxy started without removing an expired certificate")
	}

	during := writeLeaf(t, storage, "during.myapp.test", testCertPEM(t, "during.myapp.test", now.Add(-48*time.Hour)))
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if !exists(t, during) {
		t.Error("a reload of the running proxy removed a certificate")
	}
}
