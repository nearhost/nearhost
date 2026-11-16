package proxy

// TEMPORARY: what Windows says when certmagic's lock file operations meet.
// Removed before the branch merges.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/caddyserver/certmagic"
	"golang.org/x/sys/windows"
)

func ntOpen(path string, access, share, disposition uint32) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(`\??\` + path)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{ObjectName: name}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, &oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL, share,
		disposition, windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE, 0, 0)
	return h, err
}

// Replays certmagic's lock file operations, as NT calls, from goroutines
// that contend for one lock: create (O_CREATE|O_EXCL), read (os.Open),
// refresh (O_RDWR), and remove (DeleteFile). Every status other than the
// expected ones is counted.
func TestDiagLockFileStatusesOnWindows(t *testing.T) {
	if os.Getenv("NEARHOST_LOCK_DIAG") == "" {
		t.Skip("set NEARHOST_LOCK_DIAG")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "issue_cert_a.test.lock")
	var mu sync.Mutex
	counts := map[string]int{}
	note := func(what string, err error) {
		mu.Lock()
		counts[fmt.Sprintf("%s: %v (%#x)", what, err, statusOf(err))]++
		mu.Unlock()
	}
	rw := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE) // Go's share mode
	deadline := time.Now().Add(20 * time.Second)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				h, err := ntOpen(path, windows.GENERIC_WRITE, rw, windows.FILE_CREATE)
				if err == nil {
					windows.WriteFile(h, []byte(`{"created":"x"}`), nil, nil)
					windows.CloseHandle(h)
					// refresh, as keepLockfileFresh does
					if h, err := ntOpen(path, windows.GENERIC_READ|windows.GENERIC_WRITE, rw, windows.FILE_OPEN); err == nil {
						windows.CloseHandle(h)
					} else {
						note("refresh", err)
					}
					p, _ := windows.UTF16PtrFromString(path)
					if err := windows.DeleteFile(p); err != nil {
						note("delete", err)
					}
					continue
				}
				if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
					h, err := ntOpen(path, windows.GENERIC_READ, rw, windows.FILE_OPEN)
					if err == nil {
						windows.CloseHandle(h)
					} else if !errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) {
						note("read", err)
					}
					continue
				}
				note("create", err)
			}
		}()
	}
	wg.Wait()
	var lines []string
	for k, v := range counts {
		lines = append(lines, fmt.Sprintf("%6d %s", v, k))
	}
	sort.Strings(lines)
	t.Logf("unexpected statuses:\n%s", strings.Join(lines, "\n"))
}

func statusOf(err error) uint32 {
	var st windows.NTStatus
	if errors.As(err, &st) {
		return uint32(st)
	}
	var en windows.Errno
	if errors.As(err, &en) {
		return uint32(en)
	}
	return 0
}

// The same contention through certmagic's own storage, with its errors.
func TestDiagCertmagicLockContentionOnWindows(t *testing.T) {
	if os.Getenv("NEARHOST_LOCK_DIAG") == "" {
		t.Skip("set NEARHOST_LOCK_DIAG")
	}
	st := &certmagic.FileStorage{Path: t.TempDir()}
	var mu sync.Mutex
	counts := map[string]int{}
	ok := 0
	deadline := time.Now().Add(20 * time.Second)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err := st.Lock(ctx, "issue_cert_a.test")
				cancel()
				mu.Lock()
				if err != nil {
					msg := err.Error()
					if i := strings.Index(msg, `C:\`); i >= 0 {
						if j := strings.Index(msg[i:], ": "); j >= 0 {
							msg = msg[:i] + "…" + msg[i+j:]
						}
					}
					counts["lock: "+msg]++
				} else {
					ok++
				}
				mu.Unlock()
				if err != nil {
					continue
				}
				if err := st.Unlock(context.Background(), "issue_cert_a.test"); err != nil {
					mu.Lock()
					counts["unlock: "+err.Error()]++
					mu.Unlock()
					// leave it to the stale check... remove for the next round
					for i := 0; i < 100 && os.Remove(filepath.Join(st.Path, "locks", "issue_cert_a.test.lock")) != nil; i++ {
						time.Sleep(time.Millisecond)
					}
				}
			}
		}()
	}
	wg.Wait()
	var lines []string
	for k, v := range counts {
		lines = append(lines, fmt.Sprintf("%6d %s", v, k))
	}
	sort.Strings(lines)
	t.Logf("%d locks taken; failures:\n%s", ok, strings.Join(lines, "\n"))
}
