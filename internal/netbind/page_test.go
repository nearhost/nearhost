package netbind

import (
	"slices"
	"testing"
)

// Nearhost's page listens on loopback, except on macOS for a non-root
// process and a port below 1024, which macOS lets it bind on the wildcard
// address alone.
func TestPageAddrsFor(t *testing.T) {
	standIPv6Loopback(t, true)
	loopback := func(port int) []string { return LoopbackAddrs(port) }
	for _, tc := range []struct {
		name  string
		goos  string
		euid  int
		port  int
		wants func(int) []string
	}{
		{"macOS, low port, not root", "darwin", 501, 444, ServeAddrs},
		{"macOS, port 1023, not root", "darwin", 501, 1023, ServeAddrs},
		{"macOS, port 1024, not root", "darwin", 501, 1024, loopback},
		{"macOS, default port", "darwin", 501, 6327, loopback},
		{"macOS, port 0 (the system picks one), not root", "darwin", 501, 0, loopback},
		{"macOS, low port, root", "darwin", 0, 444, loopback},
		{"Linux, low port, not root", "linux", 1000, 444, loopback},
		{"Linux, default port", "linux", 1000, 6327, loopback},
		{"Windows, low port", "windows", -1, 444, loopback},
	} {
		if got, want := pageAddrsFor(tc.goos, tc.euid, tc.port), tc.wants(tc.port); !slices.Equal(got, want) {
			t.Errorf("%s: pageAddrsFor(%q, %d, %d) = %q, want %q", tc.name, tc.goos, tc.euid, tc.port, got, want)
		}
	}
}
