package sudo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake points Path at the stand-in for sudo, with password as the password,
// and returns the file whose presence means the credentials are cached.
func fake(t *testing.T, password string) (cache, log string) {
	t.Helper()
	p, err := filepath.Abs("testdata/sudo")
	if err != nil {
		t.Fatal(err)
	}
	orig := Path
	Path = p
	t.Cleanup(func() { Path = orig })
	dir := t.TempDir()
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
	orig := Path
	Path = filepath.Join(t.TempDir(), "no-sudo")
	t.Cleanup(func() { Path = orig })
	if err := Validate(context.Background(), "x"); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("a missing sudo: %v", err)
	}
}

func TestNeededWhenSudoIsThere(t *testing.T) {
	fake(t, "secret")
	if got, want := Needed(), os.Geteuid() != 0; got != want {
		t.Errorf("Needed() = %v, want %v", got, want)
	}
	Path = filepath.Join(t.TempDir(), "no-sudo")
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
