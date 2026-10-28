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
