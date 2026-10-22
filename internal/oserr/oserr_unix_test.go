//go:build unix

package oserr

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// crossDeviceCode is what rename fails with across filesystems.
var crossDeviceCode = syscall.EXDEV

func TestNothingListeningAtAMissingSocket(t *testing.T) {
	// t.TempDir is too long for a socket path on macOS.
	dir, err := os.MkdirTemp("/tmp", "oserr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	_, err = net.Dial("unix", filepath.Join(dir, "missing.sock"))
	if !NothingListening(err) {
		t.Errorf("NothingListening(%v) = false", err)
	}
}
