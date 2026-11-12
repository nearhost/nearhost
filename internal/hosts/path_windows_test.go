//go:build windows

package hosts

import "testing"

func TestDefaultPathIsTheSystemHostsFileOnWindows(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Win`)
	if got := defaultPath(); got != `D:\Win\System32\drivers\etc\hosts` {
		t.Fatalf("defaultPath() = %q", got)
	}
}

func TestFlushDNSOnWindows(t *testing.T) {
	if err := flushDNS(); err != nil {
		t.Fatalf("flushDNS() = %v", err)
	}
}
