package proxy

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/oserr"
)

// holdOpen opens name without sharing any access, as a reader that Go opens
// a file with stops a removal, and closes it after d.
func holdOpen(t *testing.T, name string, d time.Duration) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(d, func() { windows.CloseHandle(h) })
}

// A goroutine waiting for a certificate's lock reads the lock file. An
// unlock that lands on that read must wait for it: a lock file left behind
// is kept fresh by its owner's goroutine for good, and that name gets no
// certificate again.
func TestStorageUnlockWaitsForAReaderOnWindows(t *testing.T) {
	root := t.TempDir()
	st, _ := Storage{Root: root}.CertMagicStorage()
	ctx := context.Background()
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "locks", "issue_cert_a.test.lock")

	// What Windows does to certmagic's own storage.
	holdOpen(t, lock, 300*time.Millisecond)
	if err := (&certmagic.FileStorage{Path: root}).Unlock(ctx, "issue_cert_a.test"); !oserr.InUse(err) {
		t.Fatalf("certmagic's Unlock beside a reader returned %v, want a sharing violation", err)
	}

	if err := st.Unlock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatalf("Unlock beside a reader: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the lock file is still there after Unlock: %v", err)
	}
}

// A certificate read while another handle holds the file, as while it is
// being replaced, waits for the handle.
func TestStorageLoadWaitsForAWriterOnWindows(t *testing.T) {
	root := t.TempDir()
	st, _ := Storage{Root: root}.CertMagicStorage()
	ctx := context.Background()
	key := "certificates/local/localhost/localhost.json"
	if err := st.Store(ctx, key, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, filepath.Join(root, filepath.FromSlash(key)), 300*time.Millisecond)
	if _, err := (&certmagic.FileStorage{Path: root}).Load(ctx, key); !oserr.InUse(err) {
		t.Fatalf("certmagic's Load beside a writer returned %v, want a sharing violation", err)
	}
	if got, err := st.Load(ctx, key); err != nil || string(got) != "{}" {
		t.Fatalf("Load beside a writer = %q, %v", got, err)
	}
}
