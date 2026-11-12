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
