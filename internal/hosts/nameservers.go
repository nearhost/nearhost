package hosts

import (
	"net/netip"
	"slices"
)

// placeholderNameservers is the site-local range Windows lists for an adapter
// that has no IPv6 nameserver (fec0:0:0:ffff::1 to ::3). Nothing answers there.
var placeholderNameservers = netip.MustParsePrefix("fec0:0:0:ffff::/48")

// nameserversFrom turns the addresses an OS lists as nameservers into the
// strings DNSLookup dials. It drops the placeholder range, unspecified
// addresses and IPv6 link-local ones, which need a zone to dial and the
// adapter table gives none, and keeps the first of any duplicates in order.
func nameserversFrom(addrs []netip.Addr) []string {
	var out []string
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if a.IsUnspecified() || a.IsLinkLocalUnicast() && a.Is6() || placeholderNameservers.Contains(a) {
			continue
		}
		s := a.String()
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
