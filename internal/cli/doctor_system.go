package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// probePort reports whether port can be bound on network ("tcp" or "udp"). It
// binds every interface, as the proxy does, and lets go at once.
func probePort(network string, port int) error {
	addr := fmt.Sprintf(":%d", port)
	if network == "udp" {
		pc, err := net.ListenPacket(network, addr)
		if err != nil {
			return err
		}
		return pc.Close()
	}
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return l.Close()
}

// capNetBindService is the bit of CAP_NET_BIND_SERVICE in a capability set.
const capNetBindService = 10

// linuxLowPorts reports whether a Linux process with the given effective user
// may bind port, a port below 1024, and why. Root may; so may a process whose
// binary was given cap_net_bind_service, and any process on a system whose
// first unprivileged port has been lowered to port or below.
func linuxLowPorts(port, euid int, readFile func(string) ([]byte, error)) (level, string) {
	if euid == 0 {
		return levelOK, "running as root"
	}
	if data, err := readFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
		if first, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && first <= port {
			return levelOK, fmt.Sprintf("this system lets any user bind ports from %d up", first)
		}
	}
	const status = "/proc/self/status"
	data, err := readFile(status)
	if err != nil {
		return levelWarn, fmt.Sprintf("could not tell: read %s: %v", status, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return levelWarn, fmt.Sprintf("could not tell: %s has CapEff %q", status, strings.TrimSpace(value))
		}
		if caps&(1<<capNetBindService) != 0 {
			return levelOK, "the binary has cap_net_bind_service"
		}
		return levelFail, "not root and the binary lacks cap_net_bind_service"
	}
	return levelWarn, "could not tell: " + status + " has no CapEff line"
}
