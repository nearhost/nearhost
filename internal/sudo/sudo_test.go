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
	"testing"
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

func TestNeededWhenSudoIsThere(t *testing.T) {
	fake(t, "secret")
	if got, want := Needed(), os.Geteuid() != 0; got != want {
		t.Errorf("Needed() = %v, want %v", got, want)
	}
	Candidates = []string{filepath.Join(t.TempDir(), "no-sudo")}
	if Needed() {
		t.Error("needed without a sudo to run")
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
