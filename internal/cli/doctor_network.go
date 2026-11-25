package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

const (
	networkDocs   = "nearhost.dev/docs/other-devices/"
	certTimeout   = 3 * time.Second
)

// checkNetwork reports what networked mode needs: the DNS token (acme only),
// the name nearhost.<domain> pointing at this machine, the served certificate
// and, on Windows, the firewall rule. It returns nothing when the config does
// not turn networked mode on. st is the running core's status, or the zero
// Status when no core answered.
func checkNetwork(ctx context.Context, b doctorBackend, cfg *config.Config, st control.Status) []check {
	if cfg == nil || !cfg.NetworkOn() {
		return nil
	}
	n := cfg.Network
	page := "nearhost." + strings.ToLower(n.Domain)
	var checks []check
	if n.CertSource() == "acme" && b.tokenPresent != nil && !b.tokenPresent() {
		checks = append(checks, problem("Network DNS token", levelFail, report.NetworkNoToken,
			"no DNS token, so Nearhost cannot get a certificate for "+page,
			"run `nearhost network token` and paste a Cloudflare token with Zone → DNS → Edit"))
	}
	if c, ok := checkNetworkName(ctx, b, n.Domain, page); ok {
		checks = append(checks, c)
	}
	if c, ok := checkNetworkCert(ctx, b, n, page, st.Network); ok {
		checks = append(checks, c)
	}
	if b.goos == "windows" && b.firewallReady != nil {
		if b.firewallReady() {
			checks = append(checks, check{name: "Network firewall", detail: "open for networked mode"})
		} else {
			checks = append(checks, problem("Network firewall", levelFail, report.NetworkFirewall,
				"Windows Firewall blocks other devices from reaching Nearhost",
				"run `nearhost network firewall` in a terminal"))
		}
	}
	return checks
}

// checkNetworkName checks that nearhost.<domain> resolves to this machine.
func checkNetworkName(ctx context.Context, b doctorBackend, domain, page string) (check, bool) {
	if b.env.lookup == nil || b.selfAddrs == nil {
		return check{}, false
	}
	const name = "Network name"
	fix := "point `*." + domain + "` at this machine's LAN address in your DNS (public DNS, Pi-hole, Tailscale split DNS or your router). See " + networkDocs
	addrs, err := b.env.lookup(ctx, page)
	if err != nil || len(addrs) == 0 {
		detail := page + " does not resolve"
		if err != nil {
			detail += ": " + printable(err.Error())
		}
		return problem(name, levelWarn, report.NetworkDNS, detail, fix), true
	}
	self := b.selfAddrs()
	for _, a := range addrs {
		if slices.Contains(self, a.Unmap()) {
			return check{name: name, detail: page + " resolves to " + a.Unmap().String()}, true
		}
	}
	var got []string
	for _, a := range addrs {
		got = append(got, a.Unmap().String())
	}
	return problem(name, levelWarn, report.NetworkDNS,
		page+" resolves to "+strings.Join(got, ", ")+", which is not an address of this machine", fix), true
}

// checkNetworkCert reads the certificate the core serves for the page, over a
// handshake to loopback, and says where it came from.
func checkNetworkCert(ctx context.Context, b doctorBackend, n *config.Network, page string, info *control.NetworkInfo) (check, bool) {
	const name = "Network certificate"
	mode := n.CertSource()
	if info != nil && info.Problem != "" {
		return problem(name, levelFail, report.NetworkCert, printable(info.Problem), certFix(mode)), true
	}
	port := 0
	if info != nil {
		port = urlPort(info.URL)
	}
	if info == nil || port == 0 || b.servedCert == nil {
		return problem(name, levelInfo, report.DoctorNotChecked, "not checked: needs a running core that serves the page", ""), true
	}
	leaf, err := b.servedCert(ctx, net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), page)
	local := err == nil && isLocalCA(leaf)
	switch {
	case mode == "ca" && err != nil:
		return problem(name, levelFail, report.NetworkCert, "could not read the certificate: "+printable(err.Error()), certFix(mode)), true
	case mode == "ca":
		return check{name: name, detail: "local CA (install the profile on each device from " + strings.TrimSuffix(info.URL, "/") + "/profile.mobileconfig)"}, true
	case err != nil:
		return problem(name, levelFail, report.NetworkCert, "could not read the certificate: "+printable(err.Error()), certFix(mode)), true
	case local:
		return problem(name, levelFail, report.NetworkCert,
			"the page is served with the local CA's certificate, not one from Let's Encrypt", certFix(mode)), true
	}
	issuer := "Let's Encrypt"
	if !slices.Contains(leaf.Issuer.Organization, "Let's Encrypt") {
		issuer = printable(issuerName(leaf))
	}
	return check{name: name, detail: issuer + ", expires " + leaf.NotAfter.Format("2006-01-02")}, true
}

func certFix(mode string) string {
	if mode == "acme" {
		return "check the DNS token with `nearhost network token`, then read the core's log for the certificate error"
	}
	return "read the core's log for the certificate error"
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		return c.Issuer.CommonName
	}
	return strings.Join(c.Issuer.Organization, ", ")
}

func isLocalCA(c *x509.Certificate) bool {
	return strings.HasPrefix(c.Issuer.CommonName, proxy.CAName) || slices.ContainsFunc(c.Issuer.Organization, func(o string) bool {
		return strings.HasPrefix(o, proxy.CAName)
	})
}

// urlPort is the port of an https URL, 443 when it has none, 0 when u is not
// a URL.
func urlPort(u string) int {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return 0
	}
	if s := p.Port(); s != "" {
		var port int
		if _, err := fmt.Sscan(s, &port); err != nil {
			return 0
		}
		return port
	}
	return 443
}

// servedCert is the production backend's read of the leaf at addr for sni.
// The chain is not verified: only the certificate's issuer and expiry are
// read, not trusted.
func servedCert(ctx context.Context, addr, sni string) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, certTimeout)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: sni, InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no certificate served")
	}
	return certs[0], nil
}

// selfAddrs are the addresses of this machine's interfaces.
func selfAddrs() []netip.Addr {
	addrs, _ := net.InterfaceAddrs()
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out
}

// dnsTokenPresent reports whether a DNS token is set, in the token file or
// NEARHOST_DNS_TOKEN.
func dnsTokenPresent() bool {
	if os.Getenv("NEARHOST_DNS_TOKEN") != "" {
		return true
	}
	path, err := config.DNSTokenPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(data)) != ""
}
