//go:build unix

// os.Chmod(f, 0) does not make a file unreadable on Windows: it only sets
// the read-only attribute, so the file would still be included there.

package transport

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultHostsIncludeSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	writeSSHFile(t, home, "config", "Include conf.d/*\nHost mini\n")
	writeSSHFile(t, home, "conf.d/a-locked", "Host secret\n")
	writeSSHFile(t, home, "conf.d/b-real", "Host vps\n")
	if err := os.Chmod(filepath.Join(home, ".ssh", "conf.d", "a-locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	hosts, err := DefaultHosts()
	want := []string{"vps", "mini"}
	if err != nil || !reflect.DeepEqual(hosts, want) {
		t.Fatalf("got %v, %v; want %v", hosts, err, want)
	}
}
