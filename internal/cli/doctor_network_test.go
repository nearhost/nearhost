package cli

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

func netCfg(certs string) *config.Config {
	return &config.Config{Network: &config.Network{Domain: "lab.example.dev", Certificates: certs}}
}

func netBackend(resolved ...string) doctorBackend {
	var addrs []netip.Addr
	for _, r := range resolved {
		addrs = append(addrs, netip.MustParseAddr(r))
	}
	b := doctorBackend{
		goos:          "linux",
		selfAddrs:     func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.77.5")} },
		tokenPresent:  func() bool { return true },
		firewallReady: func() bool { return true },
	}
	b.env.lookup = func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }
	return b
}

func byName(cs []check) map[string]check {
	m := map[string]check{}
	for _, c := range cs {
		m[c.name] = c
	}
	return m
}

func leaf(issuerCN string) func(context.Context, string, string) (*x509.Certificate, error) {
	return func(context.Context, string, string) (*x509.Certificate, error) {
		return &x509.Certificate{Issuer: pkix.Name{CommonName: issuerCN}, NotAfter: time.Date(2027, 2, 23, 0, 0, 0, 0, time.UTC)}, nil
	}
}

func TestNetworkOffHasNoRows(t *testing.T) {
	if cs := checkNetwork(context.Background(), netBackend(), &config.Config{}, control.Status{}); len(cs) != 0 {
		t.Errorf("rows %v", cs)
	}
}

func TestNetworkNoToken(t *testing.T) {
	b := netBackend("192.168.77.5")
	b.tokenPresent = func() bool { return false }
	c := byName(checkNetwork(context.Background(), b, netCfg("acme"), control.Status{}))["Network DNS token"]
	if c.level != levelFail || c.code != report.NetworkNoToken || !strings.Contains(c.fix, "nearhost network token") {
		t.Errorf("%+v", c)
	}
	// ca mode needs no token.
	if _, has := byName(checkNetwork(context.Background(), b, netCfg("ca"), control.Status{}))["Network DNS token"]; has {
		t.Error("ca mode has a token row")
	}
}

func TestNetworkName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved []string
		ok       bool
		detail   string
	}{
		{"self", []string{"192.168.77.5"}, true, "192.168.77.5"},
		{"elsewhere", []string{"203.0.113.9"}, false, "203.0.113.9"},
		{"nothing", nil, false, "does not resolve"},
	} {
		c := byName(checkNetwork(context.Background(), netBackend(tc.resolved...), netCfg("ca"), control.Status{}))["Network name"]
		if tc.ok != (c.level == levelOK) || !strings.Contains(c.detail, tc.detail) {
			t.Errorf("%s: %+v", tc.name, c)
		}
		if !tc.ok && (c.level != levelWarn || c.code != report.NetworkDNS || !strings.Contains(c.fix, networkDocs)) {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
	b := netBackend()
	b.env.lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("timeout") }
	if c := byName(checkNetwork(context.Background(), b, netCfg("ca"), control.Status{}))["Network name"]; c.code != report.NetworkDNS {
		t.Errorf("%+v", c)
	}
}

func TestNetworkCertificate(t *testing.T) {
	ni := func(certs, problem string) control.Status {
		return control.Status{Network: &control.NetworkInfo{On: true, URL: "https://nearhost.lab.example.dev:8443", Certificates: certs, Problem: problem}}
	}
	run := func(cfg *config.Config, st control.Status, b doctorBackend) check {
		return byName(checkNetwork(context.Background(), b, cfg, st))["Network certificate"]
	}
	b := netBackend("192.168.77.5")
	b.servedCert = leaf("R11")
	if c := run(netCfg("acme"), ni("acme", ""), b); c.level != levelOK || !strings.Contains(c.detail, "expires 2027-02-23") {
		t.Errorf("acme: %+v", c)
	}
	b.servedCert = leaf("Some Other CA")
	if c := run(netCfg("acme"), ni("acme", ""), b); c.level != levelOK || !strings.Contains(c.detail, "Some Other CA") || !strings.Contains(c.detail, "expires 2027-02-23") {
		t.Errorf("other issuer: %+v", c)
	}
	b.servedCert = leaf(proxy.CAName + " - ECC Intermediate")
	if c := run(netCfg("acme"), ni("acme", ""), b); c.level != levelFail || c.code != report.NetworkCert {
		t.Errorf("acme with local CA: %+v", c)
	}
	if c := run(netCfg("ca"), ni("ca", ""), b); c.level != levelOK || !strings.Contains(c.detail, "profile.mobileconfig") || !strings.Contains(c.detail, ":8443/profile.mobileconfig") {
		t.Errorf("ca: %+v", c)
	}
	if c := run(netCfg("acme"), ni("acme", "ACME failed: bad token"), b); c.level != levelFail || !strings.Contains(c.detail, "bad token") {
		t.Errorf("problem: %+v", c)
	}
	b.servedCert = func(context.Context, string, string) (*x509.Certificate, error) { return nil, errors.New("refused") }
	if c := run(netCfg("acme"), ni("acme", ""), b); c.level != levelFail || c.code != report.NetworkCert {
		t.Errorf("handshake fails: %+v", c)
	}
	// No core: an info line, not a failure.
	if c := run(netCfg("acme"), control.Status{}, b); c.level != levelInfo || !strings.Contains(c.detail, "running core") {
		t.Errorf("no core: %+v", c)
	}
}

func TestNetworkFirewallOnWindowsOnly(t *testing.T) {
	b := netBackend("192.168.77.5")
	b.firewallReady = func() bool { return false }
	if _, has := byName(checkNetwork(context.Background(), b, netCfg("ca"), control.Status{}))["Network firewall"]; has {
		t.Error("firewall row off Windows")
	}
	b.goos = "windows"
	c := byName(checkNetwork(context.Background(), b, netCfg("ca"), control.Status{}))["Network firewall"]
	if c.level != levelFail || c.code != report.NetworkFirewall || !strings.Contains(c.fix, "nearhost network firewall") {
		t.Errorf("%+v", c)
	}
	b.firewallReady = func() bool { return true }
	if c := byName(checkNetwork(context.Background(), b, netCfg("ca"), control.Status{}))["Network firewall"]; c.level != levelOK {
		t.Errorf("%+v", c)
	}
}
