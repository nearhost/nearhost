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

// A name is matched only when its most specific owner is in Hosts: an
// exact host in Except outranks a pattern in Hosts, and a pattern in Hosts
// outranks a less specific one in Except.
func TestHostsMatcherLeavesOutNamesExceptOwns(t *testing.T) {
	m := &HostsMatcher{
		Hosts:  []string{"*.local.x.io"},
		Except: []string{"api.local.x.io", "*.*.x.io"},
	}
	for sni, want := range map[string]bool{
		"other.local.x.io": true,
		"API.local.x.io":   false,
		"api.local.x.io":   false,
		"a.b.x.io":         false,
		"local.x.io":       false,
	} {
		if got := m.Match(&tls.ClientHelloInfo{ServerName: sni}); got != want {
			t.Errorf("Match(%q) = %v, want %v", sni, got, want)
		}
	}
}
