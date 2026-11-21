//go:build !windows

package control

import (
	"os"
	"testing"

	"github.com/nearhost/nearhost/internal/testutil"
)

func TestSocketMode(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), newFake())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
}
