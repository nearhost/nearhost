//go:build unix

package netbind_test

import (
	"net"
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
)

func TestServeAddrsIsTheWildcard(t *testing.T) {
	if got := netbind.ServeAddrs(443); !slices.Equal(got, []string{":443"}) {
		t.Errorf("ServeAddrs(443) = %q, want [:443]", got)
	}
}

// Nearhost's page listens on loopback only, unlike the sites' ports: on
// 127.0.0.1, and on [::1] where the machine has it.
func TestLoopbackAddrsAreLoopbackOnUnix(t *testing.T) {
	want := []string{"127.0.0.1:6327", "[::1]:6327"}
	if l, err := net.Listen("tcp", "[::1]:0"); err != nil {
		want = want[:1] // no IPv6 loopback here
	} else {
		l.Close()
	}
	if got := netbind.LoopbackAddrs(6327); !slices.Equal(got, want) {
		t.Errorf("LoopbackAddrs(6327) = %q, want %q", got, want)
	}
}
