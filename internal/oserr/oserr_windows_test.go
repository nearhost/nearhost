package oserr

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// crossDeviceCode is what rename fails with across volumes.
var crossDeviceCode = windows.ERROR_NOT_SAME_DEVICE

// The errors below are built by hand: CI cannot take away the IPv6 loopback
// or reserve a port range, so it cannot produce them for real.

func TestNoLoopbackFamilyOnWindows(t *testing.T) {
	for _, code := range []windows.Errno{windows.WSAEADDRNOTAVAIL, windows.WSAEAFNOSUPPORT} {
		err := &net.OpError{Op: "listen", Net: "tcp6", Err: os.NewSyscallError("bind", code)}
		if !NoLoopbackFamily(err) {
			t.Errorf("NoLoopbackFamily(%v) = false", err)
		}
	}
}

func TestAddrInUseForAnAccessDeniedBindOnWindows(t *testing.T) {
	// A port in a range Hyper-V or WinNAT has reserved, or held by a socket
	// with SO_EXCLUSIVEADDRUSE, fails to bind with WSAEACCES.
	err := &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", windows.WSAEACCES)}
	if !AddrInUse(err) {
		t.Errorf("AddrInUse(%v) = false", err)
	}
}

// Go opens files without FILE_SHARE_DELETE, so a reader stops a removal.
func TestRemovingAFileAnotherHandleHoldsIsInUseOnWindows(t *testing.T) {
	name := t.TempDir() + `\held`
	if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Remove(name); !InUse(err) {
		t.Errorf("removing a file that is open returned %v, want a sharing violation", err)
	}
}
