package engine

import (
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
)

// NetworkState is how the core serves to other devices on the network, as
// the config's network: section asks and as far as it can.
type NetworkState struct {
	// On is whether the config turns networked mode on.
	On bool
	// URL is the page's address on the network,
	// https://nearhost.<domain>[:<web port>] with the port left out when it
	// is 443; empty while the page is not served.
	URL string
	// Certificates is where the network's certificates come from: "acme",
	// or "ca", which it falls back to when ACME has no usable DNS token.
	Certificates string
	// Problem is why the core serves less than the config asks: "the page
	// isn't served", "no DNS token", "DNS token looks wrong" or "waiting
	// for the firewall rule". Empty when nothing is wrong.
	Problem string
}

// The problems of NetworkState.
const (
	problemNoPage     = "the page isn't served"
	problemNoToken    = "no DNS token"
	problemBadToken   = "DNS token looks wrong"
	problemNoFirewall = "waiting for the firewall rule"
)

// plausibleToken is the shape of a DNS provider's API token. A token of
// any other shape is not given to the provider, whose errors would repeat
// it in the log.
var plausibleToken = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)

// redactedToken is what stands for the DNS token in errors and log lines.
const redactedToken = "<dns token>"

// NetworkState says how the core serves to the network now.
func (e *Engine) NetworkState() NetworkState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.netState
}

// networkURL is the page's address on the network domain at port.
func networkURL(domain string, port int) string {
	u := "https://nearhost." + strings.ToLower(domain)
	if port != 443 {
		u += ":" + strconv.Itoa(port)
	}
	return u
}

// networkFor works out the proxy's network options, and the state to
// report, for cfg with the page on webPort (0 for none). It asks the DNS
// token and firewall options, so it is called with no engine lock held.
// A core that is not networked gets nil. Whatever is missing, the core
// fails closed: with no usable DNS token it takes its certificates from
// the local CA, and without the page, where devices pair, or before the
// firewall lets it through, it does not listen on the network.
func (e *Engine) networkFor(cfg *config.Config, webPort int) (*proxy.NetworkOptions, NetworkState) {
	if cfg == nil || !cfg.NetworkOn() {
		return nil, NetworkState{}
	}
	n := cfg.Network
	opts := &proxy.NetworkOptions{
		Domain:       n.Domain,
		Certificates: n.CertSource(),
		Allow:        append([]string(nil), n.AllowRanges()...),
		Listen:       true,
	}
	var problems []string
	if webPort == 0 {
		opts.Listen = false
		problems = append(problems, problemNoPage)
	}
	if opts.Certificates == "acme" {
		token, err := "", errors.New("no DNS token option")
		if e.opts.DNSToken != nil {
			token, err = e.opts.DNSToken()
		}
		switch {
		case err != nil || token == "":
			opts.Certificates = "ca"
			problems = append(problems, problemNoToken)
		case !plausibleToken.MatchString(token):
			opts.Certificates = "ca"
			problems = append(problems, problemBadToken)
		default:
			opts.DNSToken = token
		}
	}
	if e.opts.Firewall != nil && !e.opts.Firewall() {
		opts.Listen = false
		problems = append(problems, problemNoFirewall)
	}
	state := NetworkState{On: true, Certificates: opts.Certificates}
	if webPort != 0 {
		state.URL = networkURL(n.Domain, webPort)
	}
	if len(problems) > 0 {
		state.Problem = problems[0]
	}
	return opts, state
}

// updateNetwork works out the network options for the engine's config and
// page as they are now, and gives them to the proxy when they changed,
// which it reports. They take effect at the proxy's next apply. The caller
// holds lifeMu, so the config does not change meanwhile, and applyMu, so
// no site is applied between the change and the caller's own apply.
func (e *Engine) updateNetwork() bool {
	e.mu.Lock()
	cfg, port := e.cfg, e.webPort
	e.mu.Unlock()
	opts, state := e.networkFor(cfg, port)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.netState = state
	if reflect.DeepEqual(opts, e.netOpts) {
		return false
	}
	e.netOpts = opts
	e.setNetwork(opts)
	return true
}

// applyNetwork gives the proxy the network options of the engine's config,
// and applies the sites again when they changed, so turning the network on
// or off takes effect at once, though no project changed. It does nothing
// to a proxy that has not started. The caller holds lifeMu.
func (e *Engine) applyNetwork() {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	if !e.updateNetwork() {
		return
	}
	e.mu.Lock()
	started := e.started
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if !started {
		return
	}
	if err := e.redactErr(e.applySites(sites)); err != nil {
		m := report.Errorf(report.NetworkApplyFailed, "Nearhost could not change how it serves to the network: %s", err).
			Because("The proxy keeps the configuration it had until a project starts or stops, or the config is saved again.")
		e.bus.Publish(Event{Message: m.Title, Report: m})
	}
}

// dnsTokenLocked is the DNS token the proxy has, "" for none.
func (e *Engine) dnsTokenLocked() string {
	if e.netOpts == nil {
		return ""
	}
	return e.netOpts.DNSToken
}

// redactText is s with the DNS token the proxy has replaced by
// redactedToken.
func (e *Engine) redactText(s string) string {
	e.mu.Lock()
	token := e.dnsTokenLocked()
	e.mu.Unlock()
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, redactedToken)
}

// redactErr is err, unless its text holds the DNS token the proxy has: the
// DNS provider's errors may repeat it. Then it is an error of the same text
// with the token replaced, which leaves out the chain, as any error in it
// could give the token away.
func (e *Engine) redactErr(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if safe := e.redactText(text); safe != text {
		return errors.New(safe)
	}
	return err
}

// generatedHostLocked is the name a project is served at under the
// network domain when none of its own hosts is there: its network label
// under the domain. It is "" while the core is not networked, when the
// project has a host there already, when the name is not a valid host, or
// when taken (the hosts of every published site, lowercased) has it. It is
// never written to the config.
func (e *Engine) generatedHostLocked(run *projectRun, taken map[string]bool) string {
	if e.netOpts == nil || len(run.web) == 0 {
		return ""
	}
	domain := strings.ToLower(e.netOpts.Domain)
	for _, w := range run.web {
		for _, h := range w.Hosts {
			h = strings.ToLower(h)
			if h == domain || strings.HasSuffix(h, "."+domain) {
				return ""
			}
		}
	}
	host := config.NetworkLabel(run.name) + "." + domain
	if hostname.Valid(host) != nil || taken[host] {
		return ""
	}
	return host
}
