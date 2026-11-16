package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// The proxy's storage is the module below, built from the config the way
// Caddy builds it, and it stores, loads, locks and unlocks in the root.
func TestStorageModuleKeepsFilesInTheRoot(t *testing.T) {
	root := t.TempDir()
	cfg, err := BuildConfig(nil, Options{StorageRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Storage json.RawMessage `json:"storage"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	mod, err := caddy.GetModule("caddy.storage." + storageModule)
	if err != nil {
		t.Fatal(err)
	}
	conv := mod.New().(caddy.StorageConverter)
	if err := json.Unmarshal(parsed.Storage, conv); err != nil {
		t.Fatal(err)
	}
	st, err := conv.CertMagicStorage()
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := st.Store(ctx, "certificates/local/a.test/a.test.crt", []byte("cert")); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Load(ctx, "certificates/local/a.test/a.test.crt"); err != nil || string(got) != "cert" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "certificates", "local", "a.test", "a.test.crt")); err != nil {
		t.Fatalf("the stored file is not in the root: %v", err)
	}
	if _, err := st.Load(ctx, "missing"); !os.IsNotExist(err) {
		t.Fatalf("Load of a missing key = %v, want not-exist at once", err)
	}
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "locks", "issue_cert_a.test.lock")
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("no lock file: %v", err)
	}
	if err := st.Unlock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the lock file is still there after Unlock: %v", err)
	}
}

// A Lock call can wait a long time inside certmagic for the holder before
// its attempt fails on a lock file being removed. The retry window starts at
// that failure, not when Lock was called, so that late waiter still tries
// again.
func TestRetryLockWindowStartsAtTheFirstFailure(t *testing.T) {
	const window = 20 * time.Millisecond
	errSettling := errors.New("lock file being removed")
	calls := 0
	err := retryLock(context.Background(), window, func() error {
		calls++
		if calls == 1 {
			// Waiting for the holder, past the whole window.
			time.Sleep(3 * window)
			return errSettling
		}
		return nil
	}, func() bool { return true })
	if err != nil || calls != 2 {
		t.Fatalf("retryLock after a long wait = %v in %d attempts, want success on the second", err, calls)
	}
}

// Failures that go on past the window end the retry with the last attempt's
// own error.
func TestRetryLockReturnsTheErrorAtTheDeadline(t *testing.T) {
	const window = 50 * time.Millisecond
	errSettling := errors.New("lock file being removed")
	calls := 0
	start := time.Now()
	err := retryLock(context.Background(), window, func() error {
		calls++
		return errSettling
	}, func() bool { return true })
	if err != errSettling {
		t.Fatalf("retryLock = %v, want the attempt's own error", err)
	}
	if calls < 2 {
		t.Fatalf("retryLock made %d attempts, want it to retry within the window", calls)
	}
	if elapsed := time.Since(start); elapsed < window {
		t.Fatalf("retryLock gave up after %v, before the %v window", elapsed, window)
	}
}

// A failure that is not a lock file settling fails at once.
func TestRetryLockFailsAtOnceWhenNotSettling(t *testing.T) {
	errDenied := errors.New("access denied")
	calls := 0
	err := retryLock(context.Background(), time.Minute, func() error {
		calls++
		return errDenied
	}, func() bool { return false })
	if err != errDenied || calls != 1 {
		t.Fatalf("retryLock = %v in %d attempts, want the error after one", err, calls)
	}
}
