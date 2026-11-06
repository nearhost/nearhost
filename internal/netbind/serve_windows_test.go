package netbind_test

import (
	"net"
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
	"github.com/nearhost/nearhost/internal/oserr"
)

func TestServeAddrsIsLoopbackOnWindows(t *testing.T) {
	want := []string{"127.0.0.1:443", "[::1]:443"}
	if got := netbind.ServeAddrs(443); !slices.Equal(got, want) {
		t.Errorf("ServeAddrs(443) = %q, want %q", got, want)
	}
}

func TestServeAddrsSeeALoopbackHolderOnWindows(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	port := holder.Addr().(*net.TCPAddr).Port
	taken := false
	for _, addr := range netbind.ServeAddrs(port) {
		if l, err := netbind.Listen("tcp", addr); err != nil {
			taken = taken || oserr.AddrInUse(err)
		} else {
			l.Close()
		}
	}
	if !taken {
		t.Errorf("binding ServeAddrs(%d) missed a holder on 127.0.0.1", port)
	}
}
