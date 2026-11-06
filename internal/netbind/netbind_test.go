package netbind_test

import (
	"net"
	"strconv"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/oserr"
)

func TestListenRefusesATakenPort(t *testing.T) {
	holder, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := holder.Addr().(*net.TCPAddr).Port
	addr := ":" + strconv.Itoa(port)
	if l, err := netbind.Listen("tcp", addr); err == nil {
		l.Close()
		t.Fatal("Listen succeeded on a taken TCP port")
	} else if !oserr.AddrInUse(err) {
		t.Fatalf("Listen error = %v, want address in use", err)
	}
	holder.Close()
	l, err := netbind.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Listen after the holder closed: %v", err)
	}
	l.Close()
}

func TestListenPacketRefusesATakenPort(t *testing.T) {
	holder, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := holder.LocalAddr().(*net.UDPAddr).Port
	addr := ":" + strconv.Itoa(port)
	if pc, err := netbind.ListenPacket("udp", addr); err == nil {
		pc.Close()
		t.Fatal("ListenPacket succeeded on a taken UDP port")
	} else if !oserr.AddrInUse(err) {
		t.Fatalf("ListenPacket error = %v, want address in use", err)
	}
	holder.Close()
	pc, err := netbind.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("ListenPacket after the holder closed: %v", err)
	}
	pc.Close()
}
