package engine

import (
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
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
func (e *Engine) updateNetwork() (prev *proxy.NetworkOptions, changed bool) {
	e.mu.Lock()
	cfg, port := e.cfg, e.webPort
	e.mu.Unlock()
	opts, state := e.networkFor(cfg, port)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.netState = state
	if reflect.DeepEqual(opts, e.netOpts) {
		return e.netOpts, false
	}
	prev, e.netOpts = e.netOpts, opts
	e.rememberTokenLocked(opts)
	e.setNetwork(opts)
	return prev, true
}

// rememberTokenLocked notes the DNS token of opts, so it is redacted from
// then on.
func (e *Engine) rememberTokenLocked(opts *proxy.NetworkOptions) {
	if opts == nil || opts.DNSToken == "" {
		return
	}
	if e.dnsTokens == nil {
		e.dnsTokens = map[string]bool{}
	}
	e.dnsTokens[opts.DNSToken] = true
}

// applyNetwork gives the proxy the network options of the engine's config,
// and applies the sites again when they changed, so turning the network on
// or off takes effect at once, though no project changed. It does nothing
// to a proxy that has not started. The caller holds lifeMu.
//
// If the proxy cannot load the new options, it gives it back the ones it
// had, so what is applied next matches what the proxy serves and the next
// Apply, even of the same config, tries again; NetworkState says why.
func (e *Engine) applyNetwork() {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	prev, changed := e.updateNetwork()
	if !changed {
		return
	}
	e.mu.Lock()
	started := e.started
	sites := e.sitesLocked(nil)
	e.mu.Unlock()
	if !started {
		return
	}
	raw := e.applySites(sites)
	if raw == nil {
		return
	}
	err := e.redactErr(raw)
	e.mu.Lock()
	what := "start"
	if e.netOpts == nil {
		what = "stop"
	}
	e.netOpts = prev
	e.setNetwork(prev)
	e.netState.Problem = "networked mode couldn't " + what + ": " + err.Error()
	e.mu.Unlock()
	why := "Nearhost serves as it did before the change. Save the config again to try again."
	if errors.Is(raw, proxy.ErrNotServing) {
		why = "Nearhost serves nothing, here or on the network, until the config is saved again or a project starts or stops."
	}
	m := report.Errorf(report.NetworkApplyFailed, "Nearhost could not change how it serves to the network: %s", err).Because("%s", why)
	e.bus.Publish(Event{Message: m.Title, Report: m})
}

// redactText is s with every DNS token the proxy has been given replaced
// by redactedToken: one taken back after a failed load may still show in
// the log of a certificate request under way.
func (e *Engine) redactText(s string) string {
	e.mu.Lock()
	tokens := slices.Collect(maps.Keys(e.dnsTokens))
	e.mu.Unlock()
	for _, token := range tokens {
		s = strings.ReplaceAll(s, token, redactedToken)
	}
	return s
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
// project has a host there already, when the name is the page's own
// (nearhost.<domain>) or is not a valid host, or when taken (the hosts of every published site, lowercased) has it. It is
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
	// nearhost.<domain> is the page's own host: an app served there would
	// get the page's device cookie.
	if host == "nearhost."+domain || hostname.Valid(host) != nil || taken[host] {
		return ""
	}
	return host
}
