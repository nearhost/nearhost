package oserr

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// crossDeviceCode is what rename fails with across volumes.
var crossDeviceCode = windows.ERROR_NOT_SAME_DEVICE

func TestNoLoopbackFamilyOnWindows(t *testing.T) {
	err := &net.OpError{Op: "listen", Net: "tcp6", Err: os.NewSyscallError("bind", windows.WSAEADDRNOTAVAIL)}
	if !NoLoopbackFamily(err) {
		t.Errorf("NoLoopbackFamily(%v) = false", err)
	}
}

func TestNotAFileForADirectoryOnWindows(t *testing.T) {
	_, err := os.ReadFile(t.TempDir())
	if !NotAFile(err) {
		t.Errorf("NotAFile(%v) = false for reading a directory", err)
	}
}
