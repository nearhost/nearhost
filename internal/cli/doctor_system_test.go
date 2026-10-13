package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

func TestProbePortFindsATakenPort(t *testing.T) {
	port := testutil.ProxyPort(t)
	for _, network := range []string{"tcp", "udp"} {
		if err := probePort(network, port); err != nil {
			t.Fatalf("free %s port: %v", network, err)
		}
	}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := probePort("tcp", port); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("taken TCP port: %v", err)
	}
	if err := probePort("udp", port); err != nil {
		t.Fatalf("UDP is free while TCP is taken: %v", err)
	}
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if err := probePort("udp", port); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("taken UDP port: %v", err)
	}
}

func TestLinuxLowPorts(t *testing.T) {
	files := func(m map[string]string) func(string) ([]byte, error) {
		return func(name string) ([]byte, error) {
			if s, ok := m[name]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		}
	}
	const status, start = "/proc/self/status", "/proc/sys/net/ipv4/ip_unprivileged_port_start"
	plain := "Name:\tnearhost\nCapEff:\t0000000000000000\n"
	cases := []struct {
		name   string
		euid   int
		files  map[string]string
		level  level
		detail string
	}{
		{"root", 0, nil, levelOK, "running as root"},
		{"root with every capability", 0, map[string]string{status: "CapEff:\t000001ffffffffff\n", start: "1024\n"}, levelOK, "running as root"},
		{"root without the capability", 0, map[string]string{status: "CapEff:\t00000000a80401fb\n", start: "1024\n"}, levelFail, "running as root, but cap_net_bind_service has been dropped (as a container can do)"},
		{"root without the capability, sysctl lowered", 0, map[string]string{status: "CapEff:\t00000000a80401fb\n", start: "80\n"}, levelOK, "this system lets any user bind ports from 80 up"},
		{"capability", 1000, map[string]string{status: "Name:\tnearhost\nCapPrm:\t0\nCapEff:\t0000000000000400\n", start: "1024\n"}, levelOK, "the binary has cap_net_bind_service"},
		{"other capabilities only", 1000, map[string]string{status: "CapEff:\t0000000000000300\n", start: "1024\n"}, levelFail, "not root and the binary lacks cap_net_bind_service"},
		{"nothing", 1000, map[string]string{status: plain, start: "1024\n"}, levelFail, "not root and the binary lacks cap_net_bind_service"},
		{"sysctl lowered", 1000, map[string]string{status: plain, start: "80\n"}, levelOK, "this system lets any user bind ports from 80 up"},
		{"sysctl not low enough", 1000, map[string]string{status: plain, start: "443\n"}, levelFail, "not root and the binary lacks cap_net_bind_service"},
		{"unreadable", 1000, nil, levelWarn, "could not tell: read /proc/self/status: file does not exist"},
	}
	for _, tc := range cases {
		if lv, detail := linuxLowPorts(80, tc.euid, files(tc.files)); lv != tc.level || detail != tc.detail {
			t.Errorf("%s: got %v %q, want %v %q", tc.name, lv, detail, tc.level, tc.detail)
		}
	}
}
