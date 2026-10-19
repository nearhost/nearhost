package proxy

import (
	"crypto/tls"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"

	"github.com/nearhost/nearhost/internal/hostname"
)

// hostsMatcherModule is the Caddy handshake matcher name registered below.
const hostsMatcherModule = "nearhost_hosts"

func init() {
	caddy.RegisterModule(HostsMatcher{})
}

// HostsMatcher matches a TLS handshake whose server name is one of Hosts,
// each an exact name or a pattern such as internal.*.local.example.io. It
// exists because Caddy's own sni matcher only takes left-most wildcards, so
// it cannot tell which handshakes a site with such a pattern is served to.
// An empty server name never matches.
type HostsMatcher struct {
	Hosts []string `json:"hosts"`
}

// CaddyModule implements caddy.Module.
func (HostsMatcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tls.handshake_match." + hostsMatcherModule,
		New: func() caddy.Module { return new(HostsMatcher) },
	}
}

// Match implements caddytls.ConnectionMatcher. hostname.Match compares
// without regard to case and takes an exact host as a pattern with no *.
func (m *HostsMatcher) Match(hello *tls.ClientHelloInfo) bool {
	if hello.ServerName == "" {
		return false
	}
	for _, h := range m.Hosts {
		if hostname.Match(h, hello.ServerName) {
			return true
		}
	}
	return false
}

var _ caddytls.ConnectionMatcher = (*HostsMatcher)(nil)
