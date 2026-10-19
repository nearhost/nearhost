package proxy

import (
	"crypto/tls"
	"testing"
)

func TestHostsMatcherMatchesExactAndPatterns(t *testing.T) {
	m := &HostsMatcher{Hosts: []string{"app.local.x.io", "*.status.local.x.io", "internal.*.local.x.io"}}
	for sni, want := range map[string]bool{
		"APP.local.x.io":         true,
		"a.status.local.x.io":    true,
		"internal.e1.local.x.io": true,
		"other.local.x.io":       false,
		"status.local.x.io":      false,
		"":                       false,
	} {
		if got := m.Match(&tls.ClientHelloInfo{ServerName: sni}); got != want {
			t.Errorf("Match(%q) = %v, want %v", sni, got, want)
		}
	}
}
