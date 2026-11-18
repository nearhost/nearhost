//go:build unix

package hosts

import "github.com/miekg/dns"

// resolvConf is where clientConfig finds the nameservers. Tests replace it.
var resolvConf = "/etc/resolv.conf"

// noNameserverMessage is what DNSLookup reports when the system lists no
// nameserver.
const noNameserverMessage = "no nameserver in /etc/resolv.conf"

// clientConfig reads the nameservers from resolv.conf. Tests replace it.
var clientConfig = func() (*dns.ClientConfig, error) {
	return dns.ClientConfigFromFile(resolvConf)
}

// resolvedConf is where systemd-resolved lists the nameservers its stub at
// 127.0.0.53 forwards to. It exists only on Linux, and only while
// systemd-resolved runs. Tests replace it.
var resolvedConf = "/run/systemd/resolve/resolv.conf"

// upstreamConfig reads the nameservers that a stub resolver on this machine
// forwards to. Tests replace it.
var upstreamConfig = func() (*dns.ClientConfig, error) {
	return dns.ClientConfigFromFile(resolvedConf)
}
