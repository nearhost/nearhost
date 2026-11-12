//go:build windows

package hosts

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// flushDNS clears the DNS client cache so a changed hosts file takes effect
// at once. Tests replace it to count flushes.
var flushDNS = flushResolverCache

// flushResolverCache calls DnsFlushResolverCache, and falls back to
// ipconfig /flushdns when dnsapi.dll does not export it.
func flushResolverCache() error {
	proc := windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")
	if err := proc.Find(); err != nil {
		return exec.Command("ipconfig", "/flushdns").Run()
	}
	// The call returns a BOOL: zero means the flush failed.
	if r, _, callErr := proc.Call(); r == 0 {
		return callErr
	}
	return nil
}
