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
// may bind port, a port below 1024, and why. A process with
// cap_net_bind_service may, which root has unless it was dropped (as a
// container can do) and another user has when the binary was given it; so may
// any process on a system whose first unprivileged port has been lowered to
// port or below.
func linuxLowPorts(port, euid int, readFile func(string) ([]byte, error)) (level, string) {
	has, unknown := netBindCapability(readFile)
	if euid == 0 && (has || unknown != "") {
		return levelOK, "running as root"
	}
	if data, err := readFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
		if first, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && first <= port {
			return levelOK, fmt.Sprintf("this system lets any user bind ports from %d up", first)
		}
	}
	switch {
	case unknown != "":
		return levelWarn, "could not tell: " + unknown
	case has:
		return levelOK, "the binary has cap_net_bind_service"
	case euid == 0:
		return levelFail, "running as root, but cap_net_bind_service has been dropped (as a container can do)"
	}
	return levelFail, "not root and the binary lacks cap_net_bind_service"
}

// netBindCapability reports whether this process has cap_net_bind_service in
// its effective set, or why that could not be told.
func netBindCapability(readFile func(string) ([]byte, error)) (has bool, unknown string) {
	const status = "/proc/self/status"
	data, err := readFile(status)
	if err != nil {
		return false, fmt.Sprintf("read %s: %v", status, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return false, fmt.Sprintf("%s has CapEff %q", status, strings.TrimSpace(value))
		}
		return caps&(1<<capNetBindService) != 0, ""
	}
	return false, status + " has no CapEff line"
}
