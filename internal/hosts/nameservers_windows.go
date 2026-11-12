//go:build windows

package hosts

import (
	"errors"
	"net/netip"
	"unsafe"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
)

// noNameserverMessage is what DNSLookup reports when the system lists no
// nameserver.
const noNameserverMessage = "no nameserver on any network adapter"

// clientConfig lists the DNS servers of every adapter that is up, in the
// order Windows reports them. Windows has no resolv.conf. Tests replace it.
var clientConfig = func() (*dns.ClientConfig, error) {
	addrs, err := adapterNameservers()
	if err != nil {
		return nil, err
	}
	return &dns.ClientConfig{Servers: nameserversFrom(addrs), Port: "53", Timeout: 5, Attempts: 2}, nil
}

// adapterNameservers returns the DNS server addresses of the adapters that
// are up.
func adapterNameservers() ([]netip.Addr, error) {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(15 * 1024)
	var buf []byte
	for range 4 {
		buf = make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == nil {
			return serversOf(first), nil
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, err
		}
	}
	return nil, errors.New("the adapter list kept growing")
}

// serversOf walks the adapter list. The addresses are copied out, so the
// caller may drop the buffer.
func serversOf(first *windows.IpAdapterAddresses) []netip.Addr {
	var out []netip.Addr
	for a := first; a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp {
			continue
		}
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if d.Address.Sockaddr == nil {
				continue
			}
			sa, err := (*windows.RawSockaddrAny)(unsafe.Pointer(d.Address.Sockaddr)).Sockaddr()
			if err != nil {
				continue
			}
			switch sa := sa.(type) {
			case *windows.SockaddrInet4:
				out = append(out, netip.AddrFrom4(sa.Addr))
			case *windows.SockaddrInet6:
				out = append(out, netip.AddrFrom16(sa.Addr))
			}
		}
	}
	return out
}
