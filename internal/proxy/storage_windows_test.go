package proxy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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

// pendingDeletion holds name pending deletion, as DeleteFile does between
// marking a file for deletion and closing its handle, until release is
// called or the test ends.
func pendingDeletion(t *testing.T, name string) (release func()) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	del := byte(1)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, 1); err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { windows.CloseHandle(h) }) }
	t.Cleanup(release)
	return release
}

// An unlock removes the lock file with DeleteFile, which leaves it pending
// deletion for a moment; a goroutine waiting for the lock that opens it, or
// one creating it anew, in that moment is refused (ACCESS_DENIED), and
// certmagic's lock acquisition fails outright. That failure is not retried:
// the name got no certificate until the next reload. Lock waits for the
// removal to finish instead.
func TestStorageLockWaitsForALockFileBeingRemovedOnWindows(t *testing.T) {
	root := t.TempDir()
	st, _ := Storage{Root: root}.CertMagicStorage()
	ctx := context.Background()
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "locks", "issue_cert_a.test.lock")
	release := pendingDeletion(t, lock)
	if !oserr.DeletePending(lock) {
		t.Fatal("the lock file is not pending deletion")
	}

	// What Windows does to certmagic's own storage.
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := (&certmagic.FileStorage{Path: root}).Lock(short, "issue_cert_a.test"); err == nil || short.Err() != nil {
		t.Fatalf("certmagic's Lock of a lock file pending deletion returned %v, want it to fail at once", err)
	}

	// The handle lets go once Lock has found the file pending deletion.
	lockWaits = release
	t.Cleanup(func() { lockWaits = func() {} })
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatalf("Lock of a lock file pending deletion: %v", err)
	}
	if err := st.Unlock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
}

// A wait for a removal to finish ends with the context: cancelled while Lock
// waits, Lock returns without another attempt, though the lock file stays
// pending deletion and the retry window has long to run.
func TestStorageLockGivesUpWithItsContextOnWindows(t *testing.T) {
	root := t.TempDir()
	st, _ := Storage{Root: root}.CertMagicStorage()
	if err := st.Lock(context.Background(), "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	pendingDeletion(t, filepath.Join(root, "locks", "issue_cert_a.test.lock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits := 0
	lockWaits = func() { waits++; cancel() }
	t.Cleanup(func() { lockWaits = func() {} })
	if err := st.Lock(ctx, "issue_cert_a.test"); err == nil {
		t.Fatal("Lock succeeded while the lock file was pending deletion")
	}
	if waits != 1 {
		t.Fatalf("Lock waited %d times, want it to stop at the cancel during its first wait", waits)
	}
}

// certmagic's Lock waits for a held lock, polling every second, and a waiter
// can be kept there far longer than the retry window. When the holder's
// unlock then leaves the lock file pending deletion just as the waiter looks
// again, the waiter still tries again: the window counts only the
// pauses between attempts, not the wait inside one.
func TestStorageLockRetriesAfterALongWaitOnWindows(t *testing.T) {
	root := t.TempDir()
	st, _ := Storage{Root: root}.CertMagicStorage()
	ctx := context.Background()
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "locks", "issue_cert_a.test.lock")

	const window = 100 * time.Millisecond
	lockRetryWindow = window
	started := make(chan struct{}, 1)
	lockStarts = func() { started <- struct{}{} }
	releases := make(chan func(), 1)
	lockWaits = func() {
		select {
		case release := <-releases:
			release()
		default:
		}
	}
	t.Cleanup(func() { lockRetryWindow, lockStarts, lockWaits = inUseWait, func() {}, func() {} })

	waiter, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- st.Lock(waiter, "issue_cert_a.test") }()
	<-started
	// The waiter is blocked on the held lock for longer than the window...
	time.Sleep(3 * window)
	// ...when the holder's unlock leaves the file pending deletion. The
	// waiter's next look fails on it, and its retry lets the file go.
	releases <- pendingDeletion(t, lock)
	if err := <-result; err != nil {
		t.Fatalf("Lock that waited past the window, then met a lock file pending deletion: %v", err)
	}
	if err := st.Unlock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
}
