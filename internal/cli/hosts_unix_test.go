//go:build unix

package cli

import (
	"testing"
)

// An elevated run refuses every spelling of /etc/hosts but the one Nearhost
// passes: the kernel resolves ".." after the link before it, so
// /tmp/x/../../etc/hosts can open another file.
func TestElevatedFileCheckRefusesOtherSpellingsOnUnix(t *testing.T) {
	runElevated(t)
	for _, p := range []string{"/etc/hosts/", "/etc/./hosts", "/etc/../etc/hosts", "/tmp/x/../../etc/hosts", "/private/etc/hosts"} {
		if err := checkElevatedFile(p); err == nil {
			t.Errorf("checkElevatedFile(%q) accepted it", p)
		}
	}
	t.Chdir("/etc")
	if err := checkElevatedFile("hosts"); err == nil {
		t.Error(`checkElevatedFile("hosts") from /etc accepted it`)
	}
}
