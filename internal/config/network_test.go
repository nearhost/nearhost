package config

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

func parseNet(t *testing.T, y string) *Config {
	t.Helper()
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNetworkDefaults(t *testing.T) {
	c := parseNet(t, "network: {domain: lab.example.dev}\n")
	if !c.NetworkOn() {
		t.Error("a section with a domain must be on")
	}
	if got := c.Network.CertSource(); got != "ca" {
		t.Errorf("CertSource = %q, want ca", got)
	}
	if got := c.Network.AllowRanges(); !slices.Equal(got, DefaultNetworkAllow) {
		t.Errorf("AllowRanges = %v, want %v", got, DefaultNetworkAllow)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if parseNet(t, "network: {domain: lab.example.dev, enabled: false}\n").NetworkOn() {
		t.Error("enabled: false must be off")
	}
	if parseNet(t, "projects: []\n").NetworkOn() {
		t.Error("no section must be off")
	}
	if (&Config{Network: &Network{}}).NetworkOn() {
		t.Error("no domain must be off")
	}
}

func TestNetworkValidate(t *testing.T) {
	cases := []struct{ name, yaml, field string }{
		{"empty domain", "network: {domain: \"\"}", "network.domain"},
		{"bad domain", "network: {domain: lab..x}", "network.domain"},
		{"localhost", "network: {domain: localhost}", "network.domain"},
		{"wildcard", "network: {domain: '*.example.dev'}", "network.domain"},
		{"certificates", "network: {domain: lab.example.dev, certificates: letsencrypt}", "network.certificates"},
		{"acme without block", "network: {domain: lab.example.dev, certificates: acme}", "network.acme"},
		{"acme dns", "network: {domain: lab.example.dev, certificates: acme, acme: {dns: route53}}", "network.acme.dns"},
		{"not a cidr", "network: {domain: lab.example.dev, allow: [not-a-cidr]}", "network.allow"},
		{"all v4", "network: {domain: lab.example.dev, allow: [0.0.0.0/0]}", "network.allow"},
		{"too wide v4", "network: {domain: lab.example.dev, allow: [10.0.0.0/7]}", "network.allow"},
		{"all v6", "network: {domain: lab.example.dev, allow: ['::/0']}", "network.allow"},
		{"too wide v6", "network: {domain: lab.example.dev, allow: ['fc00::/6']}", "network.allow"},
		{"v6 over mapped v4", "network: {domain: lab.example.dev, allow: ['::/8']}", "network.allow"},
		{"all mapped v4", "network: {domain: lab.example.dev, allow: ['::ffff:0:0/96']}", "network.allow"},
		{"too wide mapped v4", "network: {domain: lab.example.dev, allow: ['::ffff:10.0.0.0/103']}", "network.allow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseNet(t, tc.yaml+"\n").Validate()
			m, ok := report.As(err)
			if !ok || m.Code != report.ConfigInvalid {
				t.Fatalf("Validate = %v, want %q", err, report.ConfigInvalid)
			}
			if m.Data["field"] != tc.field {
				t.Errorf("field = %v, want %s (%v)", m.Data["field"], tc.field, err)
			}
		})
	}
	ok := "network: {domain: lab.example.dev, certificates: acme, acme: {dns: cloudflare}, allow: [10.0.0.0/8, 'fc00::/7', 192.168.1.0/24]}\n"
	if err := parseNet(t, ok).Validate(); err != nil {
		t.Errorf("a valid section: %v", err)
	}
	if err := parseNet(t, "network: {domain: lab.example.dev, allow: ['::ffff:10.0.0.0/104']}\n").Validate(); err != nil {
		t.Errorf("a mapped /8: %v", err)
	}
	if err := parseNet(t, "network: {domain: '', enabled: false}\n").Validate(); err != nil {
		t.Errorf("a disabled section is not checked for a domain: %v", err)
	}
}

func TestHostsFileNamesSkipNetworkDomain(t *testing.T) {
	mk := func(alias string) Project {
		return Project{Name: alias, Server: LocalServer, Alias: alias, Enabled: true, Routes: []Route{{Port: 3000, Role: RoleWeb}}}
	}
	c := &Config{Projects: []Project{mk("a.test"), mk("b.lab.example.dev"), mk("lab.example.dev")}}
	all := []string{"a.test", "b.lab.example.dev", "lab.example.dev"}
	if got := c.HostsFileNames(); !slices.Equal(got, all) {
		t.Fatalf("without network = %v, want %v", got, all)
	}
	c.Network = &Network{Domain: "Lab.Example.dev"}
	if got := c.HostsFileNames(); !slices.Equal(got, []string{"a.test"}) {
		t.Errorf("with network = %v, want [a.test]", got)
	}
	if !c.UnderNetworkDomain("B.lab.example.dev") || c.UnderNetworkDomain("xlab.example.dev") || c.UnderNetworkDomain("a.test") {
		t.Error("UnderNetworkDomain mismatched")
	}
}

func TestUnknownKeysSeesNetwork(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("network: {domian: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := UnknownKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "domian" {
		t.Fatalf("UnknownKeys = %+v", got)
	}
}

func TestNetworkLabel(t *testing.T) {
	if got := NetworkLabel("My App"); got != "my-app" {
		t.Errorf("NetworkLabel = %q", got)
	}
}

// nearhost.<domain> is the page's own host on the network: no project may
// take it, by alias or route host, in any case, while the section is there.
func TestNetworkPageHostIsReserved(t *testing.T) {
	project := func(alias, host string) string {
		y := "projects:\n- name: app\n  server: local\n  path: /p/app\n  alias: " + alias + "\n  routes:\n  - port: 3000\n    role: web\n"
		if host != "" {
			y += "    hosts: [" + host + "]\n"
		}
		return y
	}
	cases := []struct{ name, yaml, field string }{
		{"alias", project("nearhost.lab.example.dev", ""), "projects.alias"},
		{"alias in capitals", project("Nearhost.Lab.Example.Dev", ""), "projects.alias"},
		{"route host", project("app.test", "nearhost.lab.example.dev"), "projects.routes.hosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, section := range []string{"network: {domain: lab.example.dev}\n", "network: {domain: lab.example.dev, enabled: false}\n"} {
				err := parseNet(t, section+tc.yaml).Validate()
				m, ok := report.As(err)
				if !ok || m.Code != report.ConfigInvalid {
					t.Fatalf("Validate = %v, want %q", err, report.ConfigInvalid)
				}
				if m.Data["field"] != tc.field {
					t.Errorf("field = %v, want %s (%v)", m.Data["field"], tc.field, err)
				}
				if want := "nearhost.lab.example.dev is Nearhost's own page on the network; choose another name"; !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
			if err := parseNet(t, tc.yaml).Validate(); err != nil {
				t.Errorf("without a network section: %v", err)
			}
		})
	}
}
