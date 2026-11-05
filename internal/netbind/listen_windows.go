package netbind

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

// loopbackHost is the host LoopbackProbe binds: none, which is every address.
const loopbackHost = ""

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE, ^SO_REUSEADDR in winsock2.h. It has
// no constant in x/sys.
const soExclusiveAddrUse = ^int(4)

// exclusive sets SO_EXCLUSIVEADDRUSE on a socket before it is bound, so the
// bind fails while any other socket holds the port.
func exclusive(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse, 1)
	}); err != nil {
		return err
	}
	return serr
}

// Listen is net.Listen with the port bound exclusively.
func Listen(network, address string) (net.Listener, error) {
	lc := net.ListenConfig{Control: exclusive}
	return lc.Listen(context.Background(), network, address)
}

// ListenPacket is net.ListenPacket with the port bound exclusively.
func ListenPacket(network, address string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: exclusive}
	return lc.ListenPacket(context.Background(), network, address)
}
