//go:build !windows

package netbind

import "net"

// loopbackHost is the host LoopbackProbe binds.
const loopbackHost = "127.0.0.1"

// Listen is net.Listen.
func Listen(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}

// ListenPacket is net.ListenPacket.
func ListenPacket(network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, address)
}
