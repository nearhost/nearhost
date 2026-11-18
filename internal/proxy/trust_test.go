package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func TestEnsureCACreatesRootUnderStorage(t *testing.T) {
	storage := t.TempDir()
	cert, err := EnsureCA(storage)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("expected a CA certificate")
	}
	if !strings.Contains(cert.Subject.CommonName, CAName) {
		t.Fatalf("subject CN %q should contain %q", cert.Subject.CommonName, CAName)
	}
	onDisk, err := RootCert(storage)
	if err != nil {
		t.Fatalf("root not stored under the given storage: %v", err)
	}
	if onDisk.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatal("returned cert differs from the stored root")
	}
	if IsTrusted(cert) {
		t.Fatal("a fresh CA must not be trusted by the system store")
	}
}

func TestEnsureCATwiceReturnsSameRoot(t *testing.T) {
	storage := t.TempDir()
	first, err := EnsureCA(storage)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(storage)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) {
		t.Fatal("second EnsureCA regenerated the root")
	}
}

func TestEnsureCALeavesNoCaddyRunning(t *testing.T) {
	if _, err := EnsureCA(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	caddyMu.Lock()
	defer caddyMu.Unlock()
	if owner != nil {
		t.Fatal("EnsureCA left an owning proxy behind")
	}
	if caddy.ActiveContext().Context != nil {
		t.Fatal("EnsureCA left Caddy running")
	}
}

// A zero-site proxy must not listen at all, which is what lets EnsureCA create
// the CA without binding ports 80/443.
func TestZeroSiteProxyOpensNoListeners(t *testing.T) {
	https, http := freePort(t), freePort(t)
	p := New(Options{StorageRoot: t.TempDir(), HTTPSPort: https, HTTPPort: http})
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop() })
	for _, port := range []int{https, http} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			t.Fatalf("port %d is listening with zero sites", port)
		}
	}
	if _, err := RootCert(p.opts.StorageRoot); err != nil {
		t.Fatalf("zero-site config did not create the CA root: %v", err)
	}
}

func TestEnsureCAWhileProxyRunningKeepsItServing(t *testing.T) {
	up := upstream(t, "a")
	h := startProxy(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(up.Server)}})
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// The running proxy already created the root in its own storage.
	cert, err := EnsureCA(h.p.opts.StorageRoot)
	if err != nil {
		t.Fatalf("EnsureCA while proxy running: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("expected CA")
	}
	if !h.p.Running() {
		t.Fatal("EnsureCA displaced the running proxy")
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")

	// Even with a different storage that has no root yet, a running proxy
	// must not be replaced.
	other := t.TempDir()
	_, err = EnsureCA(other)
	if err == nil {
		t.Fatal("expected an error: cannot create a CA elsewhere while a proxy owns Caddy")
	}
	if !h.p.Running() {
		t.Fatal("failed EnsureCA displaced the running proxy")
	}
	h.getEventually(h.client("a.test", false), "a.test", "/hello")
}

// recordRestrict replaces restrictStorage for the test with one that records
// each root it is given and whether the CA's root key was already there,
// and returns err.
func recordRestrict(t *testing.T, err error) *[]string {
	t.Helper()
	var calls []string
	saved := restrictStorage
	restrictStorage = func(root string) error {
		if _, statErr := os.Stat(filepath.Join(root, "pki", "authorities", "local", "root.key")); statErr == nil {
			calls = append(calls, root+" (key already there)")
		} else {
			calls = append(calls, root)
		}
		return err
	}
	t.Cleanup(func() { restrictStorage = saved })
	return &calls
}

// EnsureCA makes the storage directory private before Caddy writes the CA's
// key into it, so the key never sits under a wider access list, and once a
// process for each directory.
func TestEnsureCAMakesStoragePrivateBeforeTheKey(t *testing.T) {
	calls := recordRestrict(t, nil)
	storage := filepath.Join(t.TempDir(), "caddy")
	if _, err := EnsureCA(storage); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCA(storage); err != nil {
		t.Fatal(err)
	}
	if want := []string{storage}; !slices.Equal(*calls, want) {
		t.Fatalf("restrictStorage calls = %q, want %q", *calls, want)
	}
}

// A storage directory that cannot be made private is a warning that names
// it, and the CA is still made.
func TestEnsureCAWarnsWhenStorageCannotBeMadePrivate(t *testing.T) {
	recordRestrict(t, errors.New("not supported"))
	var out strings.Builder
	saved := storageWarnings
	storageWarnings = &out
	t.Cleanup(func() { storageWarnings = saved })
	storage := filepath.Join(t.TempDir(), "caddy")
	if _, err := EnsureCA(storage); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "could not make its CA folder "+storage+" private") || !strings.Contains(got, "not supported") {
		t.Fatalf("warning = %q", got)
	}
}

// A proxy makes its storage directory private once, before Caddy loads, and
// a failure is a line in the log.
func TestProxyMakesStoragePrivateOnceAndLogsAFailure(t *testing.T) {
	calls := recordRestrict(t, errors.New("not supported"))
	var lines []string
	SetSink(LogFunc(func(_, line string) { lines = append(lines, line) }))
	t.Cleanup(func() { SetSink(nil) })
	storage := filepath.Join(t.TempDir(), "caddy")
	p := New(Options{StorageRoot: storage, HTTPSPort: freePort(t), HTTPPort: freePort(t)})
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop() })
	if err := p.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{storage}; !slices.Equal(*calls, want) {
		t.Fatalf("restrictStorage calls = %q, want %q", *calls, want)
	}
	if !slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, "could not make its CA folder "+storage+" private")
	}) {
		t.Fatalf("no warning in the log: %q", lines)
	}
}

// Each kind of failure gets its own advice, naming the folder.
func TestStorageWarningAdvisesByKind(t *testing.T) {
	const root = "/data/nearhost/caddy"
	// The shapes winsys.RestrictToOwner returns: an *fs.PathError naming
	// root, around the system's error or its own sentinel.
	pathErr := func(kind storageFailure, op, msg string) error {
		return &restrictError{kind: kind, err: &fs.PathError{Op: op, Path: root, Err: errors.New(msg)}}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{pathErr(failedDenied, "set the access list of", "Access is denied."),
			"Warning: Nearhost could not make its CA folder private: " + root + " cannot be changed by this account. Run `nearhost trust` once, as the same user, from an administrator terminal."},
		{pathErr(failedShared, "restrict", "a folder other programs use too; its access list is left alone"),
			"Warning: Nearhost could not make its CA folder private: its data folder is set to " + root + ", a location Nearhost will not change. Point XDG_DATA_HOME at a folder of its own."},
		{pathErr(failedNoACLs, "restrict", "the drive's file system keeps no access lists"),
			"Warning: Nearhost could not make its CA folder " + root + " private (the drive's file system keeps no access lists), so other users of this computer may be able to read the CA's private key. " +
				"Keep Nearhost's data folder (%LOCALAPPDATA%\\nearhost, or nearhost in XDG_DATA_HOME when that is set) on a local NTFS drive."},
		{pathErr(failedOther, "set the owner of", "The system cannot find the file specified."),
			"Warning: Nearhost could not make its CA folder " + root + " private: The system cannot find the file specified."},
		{errors.New("plain"), "Warning: Nearhost could not make its CA folder " + root + " private: plain."},
	} {
		got := storageWarning(root, c.err)
		if got != c.want {
			t.Errorf("storageWarning(%v):\n got %q\nwant %q", c.err, got, c.want)
		}
		if n := strings.Count(got, root); n != 1 {
			t.Errorf("storageWarning(%v) names the folder %d times: %q", c.err, n, got)
		}
	}
}
