//go:build unix

package netbind_test

import (
	"slices"
	"testing"

	"github.com/nearhost/nearhost/internal/netbind"
)

func TestServeAddrsIsTheWildcard(t *testing.T) {
	if got := netbind.ServeAddrs(443); !slices.Equal(got, []string{":443"}) {
		t.Errorf("ServeAddrs(443) = %q, want [:443]", got)
	}
}
