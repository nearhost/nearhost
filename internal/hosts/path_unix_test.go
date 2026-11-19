//go:build unix

package hosts

import "testing"

// Only /etc/hosts, spelled as Nearhost spells it, is the system hosts file.
// A ".." can follow a link before it to another directory, a relative path
// depends on the working directory, and /private/etc/hosts is not what
// Nearhost passes.
func TestSystemPathOnUnix(t *testing.T) {
	if got := SystemPath(); got != "/etc/hosts" {
		t.Fatalf("SystemPath() = %q", got)
	}
	for _, p := range []string{
		"/etc/hosts/",
		"/etc/./hosts",
		"/etc/../etc/hosts",
		"/tmp/x/../../etc/hosts",
		"//etc/hosts",
		"hosts",
		"etc/hosts",
		"../etc/hosts",
		"/private/etc/hosts",
		"/ETC/HOSTS",
	} {
		if IsSystemPath(p) {
			t.Errorf("IsSystemPath(%q) = true, want false", p)
		}
	}
}
