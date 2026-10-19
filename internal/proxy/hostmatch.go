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
//
// Except are the hosts of every site outside the policy's group. A name
// belongs to the site whose host is its most specific match, so a pattern
// in Hosts must not match a name that a more specific host in Except owns:
// that site does not let the policy's clients in.
type HostsMatcher struct {
	Hosts  []string `json:"hosts"`
	Except []string `json:"except,omitempty"`
}

// CaddyModule implements caddy.Module.
func (HostsMatcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tls.handshake_match." + hostsMatcherModule,
		New: func() caddy.Module { return new(HostsMatcher) },
	}
}

// Match implements caddytls.ConnectionMatcher. It finds the most specific
// host, by hostname.Less as the routes are ordered, among Hosts and Except
// that matches the server name, and matches only when that host is in
// Hosts. hostname.Match compares without regard to case and takes an exact
// host as a pattern with no *.
func (m *HostsMatcher) Match(hello *tls.ClientHelloInfo) bool {
	if hello.ServerName == "" {
		return false
	}
	best, found, ours := "", false, false
	consider := func(hosts []string, in bool) {
		for _, h := range hosts {
			if hostname.Match(h, hello.ServerName) && (!found || hostname.Less(h, best)) {
				best, found, ours = h, true, in
			}
		}
	}
	consider(m.Hosts, true)
	consider(m.Except, false)
	return ours
}

var _ caddytls.ConnectionMatcher = (*HostsMatcher)(nil)
