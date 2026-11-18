package proxy

import (
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// gateModule is the Caddy handler module name registered below.
const gateModule = "nearhost_gate"

// forwardDownMsg is the log line of a forward's down period.
const forwardDownMsg = "the forward to the app's server is down; Nearhost is reconnecting"

// forwards is what is known of the forwards the proxy dials, by their
// address. up holds those that are up: an address that is not there is
// down, so a gate lets no request through to a forward that was never said
// to be up. told holds those whose down period a gate has logged, so the
// log has one line for it rather than one per request.
var forwards = struct {
	sync.RWMutex
	up, told map[string]bool
}{up: map[string]bool{}, told: map[string]bool{}}

// SetForwardUp says whether the forward the proxy dials at upstream
// (host:port) is up, and reports whether that changed. A site with
// Forwarded set sends a request on to its upstream only while it is up. It
// takes effect for the next request, with no config reload, so a caller
// that is about to release the forward's port says it is down first and
// the proxy never dials the port once it is free. A forward that comes up
// ends its down period: the next one is logged again.
func SetForwardUp(upstream string, up bool) bool {
	forwards.Lock()
	defer forwards.Unlock()
	if forwards.up[upstream] == up {
		return false
	}
	if up {
		forwards.up[upstream] = true
		delete(forwards.told, upstream)
	} else {
		delete(forwards.up, upstream)
	}
	return true
}

// ForgetForward drops what is known of the forward at upstream, whose port
// has been released: it is down, and a forward later at the same address
// starts afresh.
func ForgetForward(upstream string) {
	forwards.Lock()
	defer forwards.Unlock()
	delete(forwards.up, upstream)
	delete(forwards.told, upstream)
}

// ForwardUp reports whether the forward at upstream is up: whether a gate
// lets requests through to it.
func ForwardUp(upstream string) bool {
	forwards.RLock()
	defer forwards.RUnlock()
	return forwards.up[upstream]
}

// tellDown reports whether the down period of the forward at upstream is
// still to be logged, and notes that it has been.
func tellDown(upstream string) bool {
	forwards.Lock()
	defer forwards.Unlock()
	if forwards.told[upstream] {
		return false
	}
	forwards.told[upstream] = true
	return true
}

func init() {
	caddy.RegisterModule(Gate{})
}

// Gate is a Caddy HTTP handler placed before a site's reverse proxy when
// the site's upstream is a forward. It passes a request on only while
// SetForwardUp says the forward is up. Otherwise it answers the request
// itself, never calling the reverse proxy: 502, and for a browser asking for
// a page (see wantsPage), Nearhost's page saying it is reconnecting to the
// server. The first request it refuses in a down period is logged, at error
// level; the rest are not.
//
// The gate is checked for each request, and the state it reads is kept
// outside the config, so a forward that goes down or comes back needs no
// config reload: a reload takes time, leaves the requests that arrive
// meanwhile with the old routes, and drops new connections on every site.
// A reload for another reason keeps the gate's answer.
type Gate struct {
	// Upstream is the address of the forward, as the reverse proxy dials it.
	Upstream string `json:"upstream,omitempty"`
	// Project and Server name the app behind the forward on the page, as
	// for Down.
	Project string `json:"project,omitempty"`
	Server  string `json:"server,omitempty"`

	page   []byte
	logger *zap.Logger
}

// CaddyModule implements caddy.Module.
func (Gate) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + gateModule,
		New: func() caddy.Module { return new(Gate) },
	}
}

// Provision implements caddy.Provisioner. It renders the page once per
// config load.
func (g *Gate) Provision(ctx caddy.Context) error {
	g.logger = ctx.Logger()
	var err error
	g.page, err = renderDownPage(g.Project, 0, g.Server, true)
	return err
}

// ServeHTTP implements caddyhttp.MiddlewareHandler. With the forward down it
// returns before next, the reverse proxy, so nothing is dialled.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if g.Upstream != "" && ForwardUp(g.Upstream) {
		return next.ServeHTTP(w, r)
	}
	if g.logger != nil && tellDown(g.Upstream) {
		g.logger.Error(forwardDownMsg, zap.Object("request", caddyhttp.LoggableHTTPRequest{Request: r}), zap.Int("status", http.StatusBadGateway))
	}
	if wantsPage(r) {
		return writePage(w, r, http.StatusBadGateway, g.page)
	}
	w.WriteHeader(http.StatusBadGateway)
	return nil
}

var (
	_ caddy.Provisioner           = (*Gate)(nil)
	_ caddyhttp.MiddlewareHandler = (*Gate)(nil)
)
