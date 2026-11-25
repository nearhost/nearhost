package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// TestInstanceLockLeavesThePidReadable checks that the LockFileEx lock sits
// past the pid, so a second engine can read who holds the lock, and that a
// release lets the next one take it.
func TestInstanceLockLeavesThePidReadable(t *testing.T) {
	dir := t.TempDir()
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, instanceLockName))
	if err != nil {
		t.Fatalf("read the held lock file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file holds %q, want %d", got, os.Getpid())
	}
	_, err = lockInstance(dir)
	m, ok := report.As(err)
	if !ok || !errors.Is(err, ErrAlreadyRunning) || m.Data["pid"] != os.Getpid() {
		t.Fatalf("second lock = %v, want an instance error naming pid %d", err, os.Getpid())
	}
	release()
	again, err := lockInstance(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}
