package engine

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/testutil"
)

const testDomain = "lab.example.dev"

// testToken is a DNS token of a plausible shape.
const testToken = "cf_TOKEN-0123456789abcdefghij"

// networkSection is a network: section on testDomain with certificates
// from source.
func networkSection(source string) *config.Network {
	n := &config.Network{Domain: testDomain, Certificates: source}
	if source == "acme" {
		n.ACME = &config.NetworkACME{DNS: "cloudflare"}
	}
	return n
}

// proxyOps records what the engine tells the proxy: each SetNetwork, as
// "network <domain>" or "network nil", and each apply of the sites.
type proxyOps struct {
	mu   sync.Mutex
	ops  []string
	last *proxy.NetworkOptions
}

func (r *proxyOps) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ops)
}

func (r *proxyOps) network() *proxy.NetworkOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// watchProxy records the engine's SetNetwork and applies in ops; through
// passes them on to the real proxy as well.
func watchProxy(h *harness, through bool) *proxyOps {
	r := &proxyOps{}
	set, apply := h.eng.setNetwork, h.eng.applySites
	h.eng.mu.Lock()
	h.eng.setNetwork = func(n *proxy.NetworkOptions) {
		r.mu.Lock()
		if n == nil {
			r.ops = append(r.ops, "network nil")
		} else {
			r.ops = append(r.ops, "network "+n.Domain)
		}
		r.last = n
		r.mu.Unlock()
		set(n)
	}
	h.eng.mu.Unlock()
	h.eng.applySites = func(sites []proxy.Site) error {
		r.mu.Lock()
		r.ops = append(r.ops, "apply")
		r.mu.Unlock()
		if through {
			return apply(sites)
		}
		return nil
	}
	return r
}

// With the network on, a name under the network domain never reaches the
// hosts file: the network's DNS resolves it. With the network off it does.
func TestEnsureHostsSkipsNetworkDomain(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("network %v", on), func(t *testing.T) {
			p := localProjectRoutes("app", config.Route{Port: upstream(t, "app"), Role: config.RoleWeb, Label: "web", Hosts: []string{"api." + testDomain}})
			p.Alias = "app." + testDomain
			cfg := &config.Config{Projects: []config.Project{p}}
			if on {
				cfg.Network = networkSection("ca")
			}
			h := newHarness(t, cfg)
			mustStart(t, h, "app")
			calls := h.ensureCalls()
			if len(calls) == 0 {
				t.Fatal("EnsureHosts was not called")
			}
			last := calls[len(calls)-1]
			for _, name := range []string{"app." + testDomain, "api." + testDomain} {
				if got := slices.Contains(last, name); got == on {
					t.Errorf("EnsureHosts got %v; has %s = %v, want %v", last, name, got, !on)
				}
			}
		})
	}
}

// siteHosts are the hosts of the project's sites, in order.
func siteHosts(h *harness, project string) [][]string {
	var out [][]string
	for _, s := range h.eng.Sites() {
		if s.Project == project {
			out = append(out, s.Hosts)
		}
	}
	return out
}

// On a networked core a project is served under the network domain too, at
// a name made from its own, unless it already has a host there. The name
// is not written to the config.
func TestNetworkGeneratesAlias(t *testing.T) {
	app := localProject("My App", upstream(t, "app"))
	app.Alias = "my-app.test"
	x := localProject("x", upstream(t, "x"))
	x.Alias = "x." + testDomain
	h := newHarness(t, &config.Config{Projects: []config.Project{app, x}, Network: networkSection("ca")})
	mustStart(t, h, "My App")
	mustStart(t, h, "x")
	if got, want := siteHosts(h, "My App"), [][]string{{"my-app.test", "my-app." + testDomain}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("My App hosts = %v, want %v", got, want)
	}
	if got, want := siteHosts(h, "x"), [][]string{{"x." + testDomain}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("x hosts = %v, want %v", got, want)
	}
	for _, p := range h.eng.Config().Projects {
		for _, host := range p.AllHosts() {
			if host == "my-app."+testDomain {
				t.Errorf("the generated alias was written to the config: %+v", p)
			}
		}
	}
}

// A generated name another project already serves is left out, so two
// projects never claim one host.
func TestNetworkGeneratedAliasNeverClashes(t *testing.T) {
	a := localProject("a", upstream(t, "a"))
	b := localProject("b", upstream(t, "b"))
	b.Alias = "a." + testDomain
	h := newHarness(t, &config.Config{Projects: []config.Project{a, b}, Network: networkSection("ca")})
	mustStart(t, h, "a")
	mustStart(t, h, "b")
	if got, want := siteHosts(h, "a"), [][]string{{"a.test"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("a hosts = %v, want %v", got, want)
	}
}

// Turning the network on in the config gives the proxy its network
// options, then applies the sites, though no project changed.
func TestApplyNetworkOnReappliesSites(t *testing.T) {
	h := newHarness(t, &config.Config{})
	ops := watchProxy(h, false)
	if err := h.eng.Apply(bg, &config.Config{Network: networkSection("ca")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := ops.list(), []string{"network " + testDomain, "apply"}; !slices.Equal(got, want) {
		t.Errorf("proxy saw %v, want %v", got, want)
	}
	if n := ops.network(); n == nil || n.Domain != testDomain || n.Certificates != "ca" {
		t.Errorf("network options = %+v", n)
	}
	if st := h.eng.NetworkState(); !st.On || st.Problem != "the page isn't served" {
		t.Errorf("NetworkState = %+v", st)
	}
	// The same config again changes nothing.
	if err := h.eng.Apply(bg, &config.Config{Network: networkSection("ca")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := ops.list(); len(got) != 2 {
		t.Errorf("an unchanged network was applied again: %v", got)
	}
}

// lanAddr is an IPv4 address of this machine other than loopback, or ""
// when it has none.
func lanAddr() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	return ""
}

// reachable reports whether a TCP connection to addr is accepted.
func reachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Removing the network from the config narrows the listeners at once: the
// proxy is told it is not networked and the sites are applied again, and
// the page's port stops answering on the machine's LAN address.
func TestApplyNetworkOffNarrowsListeners(t *testing.T) {
	web := testutil.ProxyPort(t)
	h := buildHarness(t, &config.Config{Network: networkSection("ca")}, func(o *Options) { o.WebPort = web })
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := h.eng.NetworkState(); !st.On || st.Problem != "" || st.URL != fmt.Sprintf("https://nearhost.%s:%d", testDomain, web) {
		t.Fatalf("NetworkState = %+v", st)
	}
	lan := lanAddr()
	page := net.JoinHostPort(lan, fmt.Sprint(web))
	if lan != "" && !reachable(page) {
		t.Fatalf("the networked page is not reachable at %s", page)
	}
	ops := watchProxy(h, true)
	if err := h.eng.Apply(bg, &config.Config{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := ops.list(), []string{"network nil", "apply"}; !slices.Equal(got, want) {
		t.Errorf("proxy saw %v, want %v", got, want)
	}
	if st := h.eng.NetworkState(); st != (NetworkState{}) {
		t.Errorf("NetworkState after off = %+v", st)
	}
	if local := net.JoinHostPort("127.0.0.1", fmt.Sprint(web)); !reachable(local) {
		t.Errorf("the page does not answer at %s after the network was turned off", local)
	}
	if lan == "" {
		t.Log("no LAN address to check the listeners from")
		return
	}
	if reachable(page) {
		t.Errorf("the page still answers at %s after the network was turned off", page)
	}
}

// certificates: acme with no DNS token falls back to the local CA, so the
// page is still served, and says why.
func TestACMEWithoutTokenIsAProblem(t *testing.T) {
	for _, c := range []struct {
		name    string
		token   func() (string, error)
		problem string
	}{
		{"missing", func() (string, error) { return "", errors.New("no token file") }, "no DNS token"},
		{"empty", func() (string, error) { return "", nil }, "no DNS token"},
		{"none asked", nil, "no DNS token"},
		{"implausible", func() (string, error) { return "not a token\n", nil }, "DNS token looks wrong"},
		{"short", func() (string, error) { return "abc", nil }, "DNS token looks wrong"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := buildHarness(t, &config.Config{}, func(o *Options) {
				o.WebPort = testutil.ProxyPort(t)
				o.DNSToken = c.token
			})
			if err := h.eng.Start(bg); err != nil {
				t.Fatalf("Start: %v", err)
			}
			ops := watchProxy(h, false)
			if err := h.eng.Apply(bg, &config.Config{Network: networkSection("acme")}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			st := h.eng.NetworkState()
			if st.Problem != c.problem || st.Certificates != "ca" || !st.On {
				t.Errorf("NetworkState = %+v, want problem %q with ca", st, c.problem)
			}
			n := ops.network()
			if n == nil || n.Certificates != "ca" || n.DNSToken != "" || !n.Listen {
				t.Errorf("network options = %+v, want ca with no token, listening", n)
			}
		})
	}
}

// A good token goes to the proxy, and the core gets its certificates from
// ACME.
func TestACMEWithTokenGoesToTheProxy(t *testing.T) {
	h := buildHarness(t, &config.Config{}, func(o *Options) {
		o.WebPort = testutil.ProxyPort(t)
		o.DNSToken = func() (string, error) { return testToken, nil }
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ops := watchProxy(h, false)
	if err := h.eng.Apply(bg, &config.Config{Network: networkSection("acme")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := h.eng.NetworkState(); st.Problem != "" || st.Certificates != "acme" {
		t.Errorf("NetworkState = %+v", st)
	}
	if n := ops.network(); n == nil || n.Certificates != "acme" || n.DNSToken != testToken {
		t.Errorf("network options = %+v", n)
	}
}

// The DNS token never shows in an error the engine publishes: an apply
// that fails with it in the text says <dns token> instead.
func TestProxyErrorsHideTheDNSToken(t *testing.T) {
	h := buildHarness(t, &config.Config{}, func(o *Options) {
		o.WebPort = testutil.ProxyPort(t)
		o.DNSToken = func() (string, error) { return testToken, nil }
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events, unsubscribe := h.eng.Events().Subscribe(256)
	defer unsubscribe()
	h.eng.applySites = func([]proxy.Site) error {
		return fmt.Errorf("load proxy config: cloudflare: token %s refused", testToken)
	}
	cfg := &config.Config{Network: networkSection("acme"), Projects: []config.Project{localProject("a", upstream(t, "a"))}}
	cfg.Projects[0].Enabled = false
	if err := h.eng.Apply(bg, cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	err := h.eng.StartProject(bg, "a")
	if err == nil {
		t.Fatal("StartProject succeeded with the proxy failing")
	}
	if strings.Contains(err.Error(), testToken) || !strings.Contains(err.Error(), "<dns token>") {
		t.Errorf("StartProject error = %v", err)
	}
	if st := h.eng.Status("a"); strings.Contains(st.Message, testToken) {
		t.Errorf("status shows the token: %q", st.Message)
	}
	h.eng.proxyLog("", "cloudflare said no to "+testToken)
	unsubscribe()
	seen := false
	for ev := range events {
		text := ev.Message
		if ev.Report != nil {
			text += " " + ev.Report.Error()
		}
		if strings.Contains(text, testToken) {
			t.Errorf("event shows the token: %+v", ev)
		}
		if strings.Contains(text, "<dns token>") {
			seen = true
		}
	}
	if !seen {
		t.Error("no event said the apply failed")
	}
}

// Networked mode needs the page, where devices pair: without it the core
// does not listen on the network.
func TestNetworkWithoutPageHoldsListen(t *testing.T) {
	h := newHarness(t, &config.Config{})
	ops := watchProxy(h, false)
	if err := h.eng.Apply(bg, &config.Config{Network: networkSection("ca")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := h.eng.NetworkState(); st.Problem != "the page isn't served" || st.URL != "" {
		t.Errorf("NetworkState = %+v", st)
	}
	if n := ops.network(); n == nil || n.Listen {
		t.Errorf("network options = %+v, want not listening", n)
	}
}

// Until the firewall lets the core through, it does not listen on the
// network, and says why.
func TestFirewallNotReadyHoldsListen(t *testing.T) {
	h := buildHarness(t, &config.Config{}, func(o *Options) {
		o.WebPort = testutil.ProxyPort(t)
		o.Firewall = func() bool { return false }
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ops := watchProxy(h, false)
	if err := h.eng.Apply(bg, &config.Config{Network: networkSection("ca")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := h.eng.NetworkState(); st.Problem != "waiting for the firewall rule" {
		t.Errorf("NetworkState = %+v", st)
	}
	if n := ops.network(); n == nil || n.Listen {
		t.Errorf("network options = %+v, want not listening", n)
	}
}

// The page's network address leaves out the port when it is 443.
func TestNetworkURL(t *testing.T) {
	for port, want := range map[int]string{443: "https://nearhost." + testDomain, 6327: "https://nearhost." + testDomain + ":6327"} {
		if got := networkURL(testDomain, port); got != want {
			t.Errorf("networkURL(%d) = %q, want %q", port, got, want)
		}
	}
}

// A project named nearhost is never given nearhost.<domain>, the page's
// own host on the network, whose device cookie would then reach the app.
func TestNetworkNeverGeneratesThePageHost(t *testing.T) {
	p := localProject("nearhost", upstream(t, "nh"))
	h := newHarness(t, &config.Config{Projects: []config.Project{p}, Network: networkSection("ca")})
	mustStart(t, h, "nearhost")
	if got, want := siteHosts(h, "nearhost"), [][]string{{"nearhost.test"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("nearhost hosts = %v, want %v", got, want)
	}
}

// A network that fails to come up leaves the proxy's options as they were,
// says why in NetworkState and in an event whose explanation matches what
// the proxy serves, and is tried again by the next Apply, even of the same
// config. (The proxy's own restore of its previous config is
// TestFailedMoveRestoresThePreviousConfig.)
func TestFailedNetworkStartKeepsServingAndRetries(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		why  string
	}{
		{"restored", errors.New("load proxy config: listen tcp :6327: address in use; the previous config serves again"), "serves as it did before"},
		{"not serving", fmt.Errorf("load proxy config: boom: %w", proxy.ErrNotServing), "serves nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := buildHarness(t, &config.Config{}, func(o *Options) { o.WebPort = testutil.ProxyPort(t) })
			if err := h.eng.Start(bg); err != nil {
				t.Fatalf("Start: %v", err)
			}
			ops := watchProxy(h, false)
			fail := true
			h.eng.applySites = func([]proxy.Site) error {
				ops.mu.Lock()
				ops.ops = append(ops.ops, "apply")
				ops.mu.Unlock()
				if fail {
					return c.err
				}
				return nil
			}
			events, unsubscribe := h.eng.Events().Subscribe(256)
			defer unsubscribe()
			cfg := &config.Config{Network: networkSection("ca")}
			if err := h.eng.Apply(bg, cfg); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := ops.list(), []string{"network " + testDomain, "apply", "network nil"}; !slices.Equal(got, want) {
				t.Errorf("proxy saw %v, want %v", got, want)
			}
			if st := h.eng.NetworkState(); !strings.HasPrefix(st.Problem, "networked mode couldn't start: ") {
				t.Errorf("NetworkState = %+v", st)
			}
			unsubscribe()
			said := false
			for ev := range events {
				if ev.Report != nil && ev.Report.Code == report.NetworkApplyFailed {
					said = true
					if !strings.Contains(ev.Report.Why, c.why) {
						t.Errorf("the failure says %q, want %q", ev.Report.Why, c.why)
					}
				}
			}
			if !said {
				t.Error("no network.apply-failed event")
			}
			fail = false
			if err := h.eng.Apply(bg, cfg); err != nil {
				t.Fatalf("Apply again: %v", err)
			}
			if got, want := ops.list()[3:], []string{"network " + testDomain, "apply"}; !slices.Equal(got, want) {
				t.Errorf("the retry: proxy saw %v, want %v", got, want)
			}
			if st := h.eng.NetworkState(); st.Problem != "" || !st.On {
				t.Errorf("NetworkState after the retry = %+v", st)
			}
		})
	}
}
