package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestLockFileExcludesOthersOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml.lock")
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// A second open of the file asks for the same byte without waiting, so
	// the answer does not depend on when a goroutine is scheduled.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
	if err != windows.ERROR_LOCK_VIOLATION {
		t.Fatalf("a second lock while the first is held: %v, want ERROR_LOCK_VIOLATION", err)
	}
}

func TestLockFileWaitsOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml.lock")
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	go func() {
		again, err := lockFile(path)
		if err != nil {
			t.Error(err)
		} else {
			again()
		}
		close(locked)
	}()
	select {
	case <-locked:
		t.Fatal("a second lock was taken while the first was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("the second lock was not taken after the first was released")
	}
}

func TestLockFileCreatesDirOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.yaml.lock")
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
