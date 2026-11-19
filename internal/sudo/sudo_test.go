//go:build unix

package sudo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// realOwnedByRoot is the ownership check Nearhost ships with.
var realOwnedByRoot = OwnedByRoot

// useCandidates makes paths the only places Binary looks, and root the owner
// of whatever it finds there.
func useCandidates(t *testing.T, paths ...string) {
	t.Helper()
	origCandidates, origOwned := Candidates, OwnedByRoot
	Candidates = paths
	OwnedByRoot = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { Candidates, OwnedByRoot = origCandidates, origOwned })
}

// fake points Binary at the stand-in for sudo, with password as the password,
// and returns the file whose presence means the credentials are cached. The
// stand-in is copied and made 0o755 so that the umask cannot make Binary
// reject it.
func fake(t *testing.T, password string) (cache, log string) {
	t.Helper()
	src, err := os.ReadFile("testdata/sudo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "sudo")
	if err := os.WriteFile(p, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	useCandidates(t, p)
	cache, log = filepath.Join(dir, "cache"), filepath.Join(dir, "log")
	t.Setenv("FAKE_SUDO_PASSWORD", password)
	t.Setenv("FAKE_SUDO_CACHE", cache)
	t.Setenv("FAKE_SUDO_LOG", log)
	return cache, log
}

func TestCachedAsksNothing(t *testing.T) {
	cache, log := fake(t, "secret")
	ctx := context.Background()
	if Cached(ctx) {
		t.Fatal("cached with no credentials")
	}
	if err := os.WriteFile(cache, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !Cached(ctx) {
		t.Error("not cached with credentials")
	}
	calls, _ := os.ReadFile(log)
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if call != "-n -v" {
			t.Errorf("Cached ran sudo %q, want -n -v", call)
		}
	}
}

func TestValidateCachesTheCredentials(t *testing.T) {
	_, log := fake(t, "secret")
	ctx := context.Background()
	if err := Validate(ctx, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a wrong password: %v", err)
	}
	if Cached(ctx) {
		t.Fatal("a wrong password cached the credentials")
	}
	if err := Validate(ctx, "secret"); err != nil {
		t.Fatalf("the right password: %v", err)
	}
	if !Cached(ctx) {
		t.Error("the right password did not cache the credentials")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "-S -v -p") {
		t.Errorf("calls %q", calls)
	}
	if strings.Contains(string(calls), "secret") {
		t.Error("the password reached sudo's arguments")
	}
}

func TestValidateSaysWhySudoFailed(t *testing.T) {
	useCandidates(t, filepath.Join(t.TempDir(), "no-sudo"))
	if err := Validate(context.Background(), "x"); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("a missing sudo: %v", err)
	}
}

// A sudo that speaks the user's language still has its refusal read as a
// wrong password: sudo is asked in the C locale.
func TestValidateReadsATranslatedSudo(t *testing.T) {
	fake(t, "secret")
	t.Setenv("FAKE_SUDO_GERMAN", "1")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	if err := Validate(context.Background(), "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("a wrong password to a German sudo: %v", err)
	}
}

// executable writes a regular, executable file in a temporary directory.
func executable(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBinarySkipsAMissingCandidate(t *testing.T) {
	good := executable(t, 0o755)
	useCandidates(t, filepath.Join(t.TempDir(), "missing"), good)
	got, err := Binary()
	if err != nil || got != good {
		t.Errorf("Binary() = %q, %v, want %q", got, err, good)
	}
}

func TestBinaryRejectsAFileNotOwnedByRoot(t *testing.T) {
	// The test runs as a user, so the file is not owned by uid 0.
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	useCandidates(t, executable(t, 0o755))
	OwnedByRoot = realOwnedByRoot
	if got, err := Binary(); err == nil {
		t.Errorf("Binary() = %q, want an error", got)
	}
}

func TestBinaryRejectsAFileOthersCanWrite(t *testing.T) {
	useCandidates(t, executable(t, 0o777))
	if got, err := Binary(); err == nil {
		t.Errorf("Binary() = %q, want an error", got)
	}
}

func TestBinaryRejectsAFileItsGroupCanWrite(t *testing.T) {
	useCandidates(t, executable(t, 0o775))
	if got, err := Binary(); err == nil {
		t.Errorf("Binary() = %q, want an error", got)
	}
}

func TestBinaryRejectsAFileNoOneCanRun(t *testing.T) {
	good := executable(t, 0o755)
	useCandidates(t, executable(t, 0o644), good)
	got, err := Binary()
	if err != nil || got != good {
		t.Errorf("Binary() = %q, %v, want %q", got, err, good)
	}
}

// A file whose execute bit is its group's alone cannot be run by its owner,
// so the kernel refuses it to the user running the test.
func TestBinaryRejectsAFileThisUserCannotRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may run any file with an execute bit")
	}
	good := executable(t, 0o755)
	useCandidates(t, executable(t, 0o010), good)
	got, err := Binary()
	if err != nil || got != good {
		t.Errorf("Binary() = %q, %v, want %q", got, err, good)
	}
}

func TestBinaryWithoutACandidateIsErrNotFound(t *testing.T) {
	useCandidates(t, filepath.Join(t.TempDir(), "missing"))
	if _, err := Binary(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Binary() error = %v, want ErrNotFound", err)
	}
}

func TestBinaryRejectsADirectory(t *testing.T) {
	useCandidates(t, t.TempDir())
	if got, err := Binary(); err == nil {
		t.Errorf("Binary() = %q, want an error", got)
	}
}

func TestBinaryFallsThroughARejectedCandidate(t *testing.T) {
	good := executable(t, 0o755)
	useCandidates(t, executable(t, 0o777), good)
	got, err := Binary()
	if err != nil || got != good {
		t.Errorf("Binary() = %q, %v, want %q", got, err, good)
	}
}

func TestDefaultCandidates(t *testing.T) {
	want := []string{"/usr/bin/sudo", "/bin/sudo", "/usr/local/bin/sudo", "/run/wrappers/bin/sudo"}
	if !reflect.DeepEqual(Candidates, want) {
		t.Errorf("Candidates = %q, want %q", Candidates, want)
	}
}

// A validation whose context ends after sudo cached the credentials leaves
// none cached: the user withdrew consent.
func TestValidateCancelledAfterCachingForgets(t *testing.T) {
	cache, log := fake(t, "secret")
	fifo := filepath.Join(filepath.Dir(cache), "wrote")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SUDO_WROTE", fifo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Validate(ctx, "secret") }()
	// The open returns once the fake has cached the credentials and writes.
	opened := make(chan *os.File, 1)
	go func() {
		f, err := os.Open(fifo)
		if err != nil {
			t.Error(err)
		}
		opened <- f
	}()
	var f *os.File
	select {
	case f = <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the fake never cached the credentials")
	}
	if f != nil {
		defer f.Close() // open until the cancel, so the fake's write lands
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("the fake did not cache: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("Validate returned %v", err)
	}
	if _, err := os.Stat(cache); err == nil {
		t.Error("the credentials stayed cached after the cancel")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "-k") {
		t.Errorf("sudo -k was not run: %q", calls)
	}
}

// A validation that ends normally keeps the credentials and never runs -k.
func TestValidateKeepsTheCacheWhenNotCancelled(t *testing.T) {
	_, log := fake(t, "secret")
	if err := Validate(context.Background(), "secret"); err != nil {
		t.Fatal(err)
	}
	if !Cached(context.Background()) {
		t.Error("not cached")
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "-k") {
		t.Errorf("sudo -k ran: %q", calls)
	}
}

// Forget drops the cached credentials.
func TestForgetDropsTheCache(t *testing.T) {
	cache, _ := fake(t, "secret")
	if err := os.WriteFile(cache, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	Forget(context.Background())
	if Cached(context.Background()) {
		t.Error("still cached")
	}
}
