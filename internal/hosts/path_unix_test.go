//go:build unix

package hosts

import "testing"

// On macOS /etc is a link to /private/etc, but Nearhost names the file
// /etc/hosts, so only that spelling is the system hosts file.
func TestSystemPathOnUnix(t *testing.T) {
	if got := SystemPath(); got != "/etc/hosts" {
		t.Fatalf("SystemPath() = %q", got)
	}
	if IsSystemPath("/private/etc/hosts") {
		t.Fatal("IsSystemPath accepted /private/etc/hosts")
	}
	if IsSystemPath("/ETC/HOSTS") {
		t.Fatal("IsSystemPath ignored case on Unix")
	}
}
