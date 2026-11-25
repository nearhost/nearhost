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

	"github.com/nearhost/nearhost/internal/report"
)

func TestEnsureCACreatesRootUnderStorage(t *testing.T) {
	storage := t.TempDir()
	cert, err := EnsureCA(storage, nil)
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
	first, err := EnsureCA(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) {
		t.Fatal("second EnsureCA regenerated the root")
	}
}

func TestEnsureCALeavesNoCaddyRunning(t *testing.T) {
	if _, err := EnsureCA(t.TempDir(), nil); err != nil {
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
	cert, err := EnsureCA(h.p.opts.StorageRoot, nil)
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
	_, err = EnsureCA(other, nil)
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
	if _, err := EnsureCA(storage, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCA(storage, nil); err != nil {
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
	var got report.Collector
	storage := filepath.Join(t.TempDir(), "caddy")
	if _, err := EnsureCA(storage, &got); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	ms := got.Messages()
	if len(ms) != 1 || ms[0].Code != report.CANotPrivate || ms[0].Level != report.Warning ||
		!strings.Contains(ms[0].Title, "could not make its CA folder "+storage+" private") || !strings.Contains(ms[0].Why, "Not supported") {
		t.Fatalf("warnings = %+v", ms)
	}
}

// A proxy makes its storage directory private once, before Caddy loads, and
// a failure is a line in the log, or a message to its Reports when it has
// them.
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
		return strings.HasPrefix(l, "warning [ca.not-private] ") && strings.Contains(l, "could not make its CA folder "+storage+" private")
	}) {
		t.Fatalf("no warning in the log: %q", lines)
	}
	p.Stop()

	var got report.Collector
	storage = filepath.Join(t.TempDir(), "caddy")
	p2 := New(Options{StorageRoot: storage, HTTPSPort: freePort(t), HTTPPort: freePort(t), Reports: &got})
	if err := p2.Apply(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p2.Stop() })
	if ms := got.Messages(); len(ms) != 1 || ms[0].Code != report.CANotPrivate {
		t.Fatalf("reports = %+v", ms)
	}
}

// Each kind of failure gets its own advice, naming the folder once.
func TestStorageWarningAdvisesByKind(t *testing.T) {
	const root = "/data/nearhost/caddy"
	// The shapes winsys.RestrictToOwner returns: an *fs.PathError naming
	// root, around the system's error or its own sentinel.
	pathErr := func(kind storageFailure, op, msg string) error {
		return &restrictError{kind: kind, err: &fs.PathError{Op: op, Path: root, Err: errors.New(msg)}}
	}
	for _, c := range []struct {
		err             error
		title, why      string
		stepText, stepC string
	}{
		{pathErr(failedDenied, "set the access list of", "Access is denied."),
			"Nearhost could not make its CA folder private.", root + " cannot be changed by this account.",
			"Run this once, as the same user, from an administrator terminal:", "nearhost trust"},
		{pathErr(failedShared, "restrict", "a folder other programs use too; its access list is left alone"),
			"Nearhost could not make its CA folder private.", "Its data folder is set to " + root + ", a location Nearhost will not change.",
			"Point XDG_DATA_HOME at a folder of its own.", ""},
		{pathErr(failedNoACLs, "restrict", "the drive's file system keeps no access lists"),
			"Nearhost could not make its CA folder " + root + " private.",
			"The drive's file system keeps no access lists, so other users of this computer may be able to read the CA's private key.",
			"Keep Nearhost's data folder (%LOCALAPPDATA%\\nearhost, or nearhost in XDG_DATA_HOME when that is set) on a local NTFS drive.", ""},
		{pathErr(failedOther, "set the owner of", "The system cannot find the file specified."),
			"Nearhost could not make its CA folder " + root + " private.", "The system cannot find the file specified.", "", ""},
		{errors.New("plain"), "Nearhost could not make its CA folder " + root + " private.", "Plain.", "", ""},
		// A localised Windows message starts with a letter of more than one
		// byte.
		{pathErr(failedOther, "set the owner of", "отказано в доступе."),
			"Nearhost could not make its CA folder " + root + " private.", "Отказано в доступе.", "", ""},
	} {
		m := storageWarning(root, c.err)
		if m.Code != report.CANotPrivate || m.Level != report.Warning || m.Title != c.title || m.Why != c.why {
			t.Errorf("storageWarning(%v) = %q / %q, want %q / %q", c.err, m.Title, m.Why, c.title, c.why)
		}
		var step report.Step
		if len(m.Fix) > 0 {
			step = m.Fix[0]
		}
		if len(m.Fix) > 1 || step.Text != c.stepText || step.Command != c.stepC {
			t.Errorf("storageWarning(%v) steps = %+v, want %q %q", c.err, m.Fix, c.stepText, c.stepC)
		}
		if !errors.Is(m, c.err) {
			t.Errorf("storageWarning(%v) does not wrap its error", c.err)
		}
		if n := strings.Count(m.Title+m.Why, root); n != 1 {
			t.Errorf("storageWarning(%v) names the folder %d times", c.err, n)
		}
	}
}
