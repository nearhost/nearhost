package config

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/nearhost/nearhost/internal/hostname"
)

// Network is the network: section. With it, the core also serves its page and
// projects to paired devices on the local network, under Domain.
type Network struct {
	Enabled      *bool        `yaml:"enabled,omitempty"`      // default true when the section is present
	Domain       string       `yaml:"domain"`                 // a domain the user controls
	Certificates string       `yaml:"certificates,omitempty"` // "acme" | "ca"; default "ca"
	ACME         *NetworkACME `yaml:"acme,omitempty"`
	Allow        []string     `yaml:"allow,omitempty"` // CIDRs; default DefaultNetworkAllow
}

// NetworkACME is how certificates are obtained when Certificates is "acme".
type NetworkACME struct {
	DNS string `yaml:"dns"` // "cloudflare" only
}

// DefaultNetworkAllow is the set of client address ranges allowed when allow
// is not set: private IPv4, carrier-grade NAT (Tailscale) and IPv6 unique-local.
var DefaultNetworkAllow = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}

// NetworkOn reports whether networked mode is on: the section is present, has
// a domain, and is not switched off.
func (c *Config) NetworkOn() bool {
	n := c.Network
	return n != nil && n.Domain != "" && (n.Enabled == nil || *n.Enabled)
}

// CertSource is where certificates come from: "acme" or "ca".
func (n *Network) CertSource() string {
	if n.Certificates == "acme" {
		return "acme"
	}
	return "ca"
}

// AllowRanges is the CIDRs clients may connect from.
func (n *Network) AllowRanges() []string {
	if len(n.Allow) > 0 {
		return n.Allow
	}
	return DefaultNetworkAllow
}

// UnderNetworkDomain reports whether host is the network domain or a name
// beneath it, ignoring case. It is false while networked mode is off.
func (c *Config) UnderNetworkDomain(host string) bool {
	if !c.NetworkOn() {
		return false
	}
	d, h := strings.ToLower(c.Network.Domain), strings.ToLower(host)
	return h == d || strings.HasSuffix(h, "."+d)
}

// NetworkLabel is the first label of a project's name on the network domain:
// the project's .test alias without its .test.
func NetworkLabel(project string) string {
	return strings.TrimSuffix(AliasFor(project), ".test")
}

func (n *Network) validate() error {
	if n.Enabled != nil && !*n.Enabled {
		return nil
	}
	if n.Domain == "" {
		return invalid("network.domain", fmt.Errorf("network needs a domain, such as lab.example.com"))
	}
	if err := hostname.Valid(n.Domain); err != nil {
		return invalid("network.domain", err)
	}
	if hostname.IsPattern(n.Domain) {
		return invalid("network.domain", fmt.Errorf("network domain %q must be a plain name, not a pattern", n.Domain))
	}
	if n.Domain == "localhost" || strings.HasSuffix(n.Domain, ".localhost") || n.Domain == "test" || strings.HasSuffix(n.Domain, ".test") {
		return invalid("network.domain", fmt.Errorf("network domain %q must be a real domain you control, not a name that only resolves on this machine", n.Domain))
	}
	switch n.Certificates {
	case "", "ca":
	case "acme":
		if n.ACME == nil {
			return invalid("network.acme", fmt.Errorf("certificates: acme needs an acme: block with dns: cloudflare"))
		}
		if n.ACME.DNS != "cloudflare" {
			return invalid("network.acme.dns", fmt.Errorf("acme dns %q is not supported, only cloudflare is", n.ACME.DNS))
		}
	default:
		return invalid("network.certificates", fmt.Errorf("certificates %q must be acme or ca", n.Certificates))
	}
	for _, s := range n.Allow {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return invalid("network.allow", fmt.Errorf("allow entry %q is not a CIDR range such as 192.168.0.0/16", s))
		}
		min := 8
		if p.Addr().Is6() && !p.Addr().Is4In6() {
			min = 7
		}
		if p.Bits() < min {
			return invalid("network.allow", fmt.Errorf("allow entry %q is too wide: a range must be /%d or narrower, so a core is never open to the whole internet", s, min))
		}
	}
	return nil
}
