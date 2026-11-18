package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/sudo"
)

// runIn is run with input on stdin.
func runIn(t *testing.T, cmd *cobra.Command, input string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "nearhost", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmd)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// writeCA writes a local CA in Caddy's layout under the storage root dir and
// returns its directory.
func writeCA(t *testing.T, dir string) string {
	t.Helper()
	ca := filepath.Join(dir, "pki", "authorities", "local")
	if err := os.MkdirAll(ca, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"root.crt", "root.key", "intermediate.crt", "intermediate.key"} {
		if err := os.WriteFile(filepath.Join(ca, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ca
}

// localCA sets up config and data directories for the test with a local CA
// in Caddy's layout and a sibling file beside it, and returns the CA's
// directory and the sibling's path.
func localCA(t *testing.T) (ca, sibling string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := config.CaddyDir()
	if err != nil {
		t.Fatal(err)
	}
	ca = writeCA(t, dir)
	sibling = filepath.Join(dir, "pki", "authorities", "other", "keep")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return ca, sibling
}

// rootCert makes a self-signed CA certificate, for the fingerprint and the
// copy kept of it.
func rootCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(0x1234abcd),
		Subject:               pkix.Name{CommonName: "Nearhost Local CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// deleteFake is a fake whose root is a real certificate.
func deleteFake(t *testing.T) *fakeTrust {
	f := newFake()
	f.cert = rootCert(t)
	return f
}

// present reports whether path is there, without following a final link.
func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// keptRoot is where the copy of the fake's root certificate goes.
func keptRoot(t *testing.T) string {
	t.Helper()
	data, err := config.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(data, "old-roots", "1234abcd.crt")
}

// colonHex is b as upper-case hex pairs joined with colons.
func colonHex(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

const deletedLine = "Deleted the local CA and its key."

func TestUntrustDeleteKeyRefusesWhileNearhostRuns(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.trusted = true
	f.running = true
	f.interactive = true
	_, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "Stop Nearhost") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) || f.uninstall != 0 || f.nssUninstalls != 0 || len(f.rootDirs) != 0 {
		t.Fatalf("something ran: uninstalls=%d nssUninstalls=%d rootDirs=%v", f.uninstall, f.nssUninstalls, f.rootDirs)
	}
}

// A check that cannot tell says so, and nothing happens.
func TestUntrustDeleteKeyRefusesWhenItCannotTellWhetherNearhostRuns(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.runningFn = func(int) (bool, error) { return false, errors.New("install id unreadable") }
	_, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not tell whether a Nearhost is running (install id unreadable)") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) || len(f.rootDirs) != 0 {
		t.Fatal("something ran")
	}
}

// A Nearhost started while the question waited stops the deletion.
func TestUntrustDeleteKeyChecksForNearhostAgainBeforeDeleting(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.interactive = true
	f.runningFn = func(call int) (bool, error) { return call > 1, nil }
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "Stop Nearhost") || f.runningChecks != 2 {
		t.Fatalf("err=%v checks=%d", err, f.runningChecks)
	}
	if !present(filepath.Join(ca, "root.key")) || strings.Contains(out, deletedLine) {
		t.Fatalf("deleted anyway: %q", out)
	}
}

func TestUntrustDeleteKeyRemovesTheCAOnYes(t *testing.T) {
	ca, sibling := localCA(t)
	f := deleteFake(t)
	f.trusted = true
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err != nil {
		t.Fatal(err)
	}
	if f.uninstall != 1 {
		t.Fatalf("untrust did not run first: uninstalls=%d", f.uninstall)
	}
	question := "Delete the local CA and its key in " + ca + "? Nearhost makes a new one, which you trust again, the next time it runs. [y/N]"
	if !strings.Contains(out, question) || !strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
	if strings.Index(out, "removed from the trust store") > strings.Index(out, question) {
		t.Fatalf("asked before untrusting: %q", out)
	}
	if present(ca) {
		t.Fatal("the CA's directory is still there")
	}
	if !present(sibling) {
		t.Fatal("a sibling of the CA's directory was removed")
	}
	// A copy of the root certificate, and nothing else, stays behind.
	kept := keptRoot(t)
	if !strings.Contains(out, kept) {
		t.Fatalf("output does not say where the root's copy is: %q", out)
	}
	data, err := os.ReadFile(kept)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || !bytes.Equal(block.Bytes, f.cert.Raw) || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("copy %q", data)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(kept); err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("copy mode %v, %v", fi.Mode(), err)
		}
	}
}

func TestUntrustDeleteKeyKeepsTheCAOnNo(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", ""} {
		ca, _ := localCA(t)
		f := deleteFake(t)
		f.interactive = true
		out, err := runIn(t, newUntrustCmd(f.backend()), answer, "untrust", "--delete-key")
		if err != nil {
			t.Fatal(err)
		}
		if !present(filepath.Join(ca, "root.key")) || strings.Contains(out, deletedLine) {
			t.Fatalf("answer %q deleted the CA: %q", answer, out)
		}
		if !strings.Contains(out, "Kept the local CA and its key.") || present(keptRoot(t)) {
			t.Fatalf("output %q", out)
		}
	}
}

func TestUntrustDeleteKeyWithoutATerminalNeedsYes(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.trusted = true
	_, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(ca, "root.key")) || f.uninstall != 0 {
		t.Fatalf("something ran: uninstalls=%d", f.uninstall)
	}
}

// --yes deletes without asking, here with a root that was never trusted:
// untrust has nothing to do and the key still goes.
func TestUntrustDeleteKeyYesDeletesWithoutAsking(t *testing.T) {
	for _, flag := range []string{"--yes", "-y"} {
		ca, _ := localCA(t)
		f := deleteFake(t)
		out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", flag)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "[y/N]") || !strings.Contains(out, untrustNotInStore) || !strings.Contains(out, deletedLine) {
			t.Fatalf("output %q", out)
		}
		if present(ca) {
			t.Fatal("the CA's directory is still there")
		}
	}
}

// Failures a second run gets past delete nothing and ask nothing: the user
// fixes what the error names and runs the command again.
func TestUntrustDeleteKeyDeletesNothingWhenARetryWouldFinish(t *testing.T) {
	for name, set := range map[string]func(f *fakeTrust){
		"uninstall failed": func(f *fakeTrust) {
			f.trusted = true
			f.uninstErr = errors.New("sudo: 3 incorrect password attempts")
		},
		"no sudo found first": func(f *fakeTrust) {
			f.trusted = true
			f.sudoErr = sudo.ErrNotFound
		},
		"no sudo at the uninstall": func(f *fakeTrust) {
			f.trusted = true
			f.uninstErr = fmt.Errorf("uninstall: %w", sudo.ErrNotFound)
		},
		"browser holds the NSS store": func(f *fakeTrust) {
			f.nssUninstErr = errors.New("SEC_ERROR_BUSY")
		},
		"certutil missing": func(f *fakeTrust) {
			f.trusted = true
			f.nss = proxy.NSSStatus{State: proxy.NSSUnavailable, NoCertutil: true,
				Detail: `"certutil" not found`, Stores: []string{"/home/u/.pki/nssdb"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca, _ := localCA(t)
			f := deleteFake(t)
			set(f)
			f.interactive = true
			out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
			if err == nil || !strings.HasPrefix(err.Error(), "Nothing was deleted. ") ||
				!strings.HasSuffix(err.Error(), ", then run `nearhost untrust --delete-key` again.") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "`nearhost untrust` again") {
				t.Fatalf("err names the plain command: %v", err)
			}
			if strings.Contains(out, "[y/N]") || strings.Contains(out, deletedLine) || !present(filepath.Join(ca, "root.key")) || present(keptRoot(t)) {
				t.Fatalf("asked or deleted: %q", out)
			}
		})
	}
}

// Trust Nearhost cannot remove does not stop the key going: a trusted root
// with no key on disk signs nothing new. The command still fails, names the
// old root by fingerprint and serial number beside the instructions, and
// never says to run untrust again.
func TestUntrustDeleteKeyDeletesWhenTrustRemains(t *testing.T) {
	for name, set := range map[string]func(t *testing.T, f *fakeTrust){
		"another store still trusts it": func(t *testing.T, f *fakeTrust) {
			f.trusted = true
			f.stillTrusted = true
		},
		"a file Nearhost did not install": func(t *testing.T, f *fakeTrust) {
			setHostOS(t, "linux")
			f.trusted = true
			f.foreign = true
		},
		"a browser store still trusts it": func(t *testing.T, f *fakeTrust) {
			f.trusted = true
			f.nss = proxy.NSSStatus{State: proxy.NSSCovered, Stores: []string{"/home/u/.mozilla/firefox/x.default"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca, _ := localCA(t)
			f := deleteFake(t)
			set(t, f)
			f.interactive = true
			out, err := runIn(t, newUntrustCmd(f.backend()), "yes\n", "untrust", "--delete-key")
			if err == nil {
				t.Fatal("no error while trust remains")
			}
			sum := sha256.Sum256(f.cert.Raw)
			for _, want := range []string{colonHex(sum[:]), colonHex(f.cert.SerialNumber.Bytes()), keptRoot(t)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err lacks %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error()+out, "nearhost untrust` again") || !strings.HasSuffix(err.Error(), ".") {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(out, deletedLine) || present(ca) || !present(keptRoot(t)) {
				t.Fatalf("the CA was not deleted, or no copy kept: %q", out)
			}
		})
	}
}

// A key whose root cannot be read still goes, and the command fails because
// nothing could check what trusts the root.
func TestUntrustDeleteKeyDeletesAKeyWithAnUnreadableRoot(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.rootErr = errors.New("root.crt is not PEM")
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not PEM") || f.uninstall != 0 || strings.Contains(err.Error(), "untrust` again") {
		t.Fatalf("err=%v uninstalls=%d", err, f.uninstall)
	}
	if !strings.Contains(out, deletedLine) || present(ca) {
		t.Fatalf("the CA was not deleted: %q", out)
	}
}

// A copy Nearhost 0.7 left in the config directory is named in the question
// and goes too.
func TestUntrustDeleteKeyRemovesTheOldCopy(t *testing.T) {
	ca, _ := localCA(t)
	cfg, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	old := writeCA(t, filepath.Join(cfg, "caddy"))
	f := deleteFake(t)
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Delete the local CA and its key in "+ca+" and the old copy in "+old+"? ") {
		t.Fatalf("output %q", out)
	}
	if present(ca) || present(old) || !strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
}

// An old copy that cannot be removed means no Deleted line and a failure
// that says what remains.
func TestUntrustDeleteKeyReportsAnOldCopyLeftBehind(t *testing.T) {
	localCA(t)
	cfg, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(cfg, "caddy")
	writeCA(t, oldRoot)
	f := deleteFake(t)
	f.removeFn = func(dir string) error {
		if dir == oldRoot {
			return errors.New("could not delete all of the old copy")
		}
		return proxy.RemoveCA(dir)
	}
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not delete all of the old copy") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
}

// When the key went but the certificates it issued did not, the failure says
// both.
func TestUntrustDeleteKeyReportsCertificatesLeftBehind(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	f.removeFn = func(dir string) error {
		if err := os.RemoveAll(ca); err != nil {
			return err
		}
		return errors.New("Still there: certificates/local/app.test")
	}
	_, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "key is deleted") || !strings.Contains(err.Error(), "certificates/local/app.test") {
		t.Fatalf("err = %v", err)
	}
}

// An authority directory that is a link is left alone and the command fails.
func TestUntrustDeleteKeyRefusesALinkedCADirectory(t *testing.T) {
	ca, _ := localCA(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "root.key"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ca); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ca); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f := deleteFake(t)
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), ca) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, deletedLine) || !present(filepath.Join(outside, "root.key")) || !present(ca) {
		t.Fatalf("output %q", out)
	}
}

// A deletion that fails says so and fails the command.
func TestUntrustDeleteKeyReportsAFailedDeletion(t *testing.T) {
	localCA(t)
	f := deleteFake(t)
	f.removeErr = errors.New("could not delete all of the CA")
	out, err := runIn(t, newUntrustCmd(f.backend()), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not delete all of the CA") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, deletedLine) {
		t.Fatalf("output %q", out)
	}
}

// Root deleting another user's CA is refused before anything happens.
func TestUntrustDeleteKeyRefusesAnotherUsersStorage(t *testing.T) {
	ca, _ := localCA(t)
	f := deleteFake(t)
	b := f.backend()
	b.ownerProblem = func(string) error { return errors.New("run it as that user") }
	_, err := runIn(t, newUntrustCmd(b), "", "untrust", "--delete-key", "--yes")
	if err == nil || !strings.Contains(err.Error(), "as that user") || len(f.rootDirs) != 0 || !present(ca) {
		t.Fatalf("err = %v", err)
	}
}

// With no CA at all there is nothing to delete or ask about.
func TestUntrustDeleteKeyWithNoCASaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := deleteFake(t)
	f.rootErr = fs.ErrNotExist
	f.interactive = true
	out, err := runIn(t, newUntrustCmd(f.backend()), "y\n", "untrust", "--delete-key")
	if err == nil || !strings.Contains(err.Error(), "no local CA found") || strings.Contains(out, "[y/N]") {
		t.Fatalf("err=%v output %q", err, out)
	}
}

func TestUntrustHelpNamesDeleteKeyAndYes(t *testing.T) {
	f := newFake()
	out, err := run(t, newUntrustCmd(f.backend()), "untrust", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--delete-key") || !strings.Contains(out, "-y, --yes") {
		t.Fatalf("help %q", out)
	}
}

// --yes means nothing without --delete-key.
func TestUntrustYesNeedsDeleteKey(t *testing.T) {
	localCA(t)
	f := newFake()
	if _, err := run(t, newUntrustCmd(f.backend()), "untrust", "--yes"); err == nil || !strings.Contains(err.Error(), "--delete-key") {
		t.Fatalf("err = %v", err)
	}
}
