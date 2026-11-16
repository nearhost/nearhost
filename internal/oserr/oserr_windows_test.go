package oserr

import (
	"errors"
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

// A file removed while another handle that shares deletion has it open, as
// an antivirus scanner opens new files, stays until that handle closes, and
// cannot be opened or created meanwhile: Windows says ACCESS_DENIED.
func TestDeletePendingOnWindows(t *testing.T) {
	name := t.TempDir() + `\pending`
	if DeletePending(name) {
		t.Error("DeletePending of a missing file = true")
	}
	if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if DeletePending(name) {
		t.Error("DeletePending of a file nobody removed = true")
	}
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Open(name); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("opening a file pending deletion returned %v, want ACCESS_DENIED", err)
	}
	if !DeletePending(name) {
		t.Error("DeletePending of a removed file another handle holds = false")
	}
}
