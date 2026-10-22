package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
