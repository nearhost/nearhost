package proxy

import (
	"errors"
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// gateModule is the Caddy handler module name registered below.
const gateModule = "nearhost_gate"

// errForwardDown is the error a gate fails a request with: the site's
// forward is not up, so nothing is dialled. The down page tells it from an
// app that does not answer.
var errForwardDown = errors.New("the forward to the app's server is down; Nearhost is reconnecting")

// forwards are the forwards that are up, by the address the proxy dials
// them at. An address that is not here is down: a gate lets no request
// through to a forward that was never said to be up.
var forwards = struct {
	sync.RWMutex
	up map[string]bool
}{up: map[string]bool{}}

// SetForwardUp says whether the forward the proxy dials at upstream
// (host:port) is up, and reports whether that changed. A site with
// Forwarded set sends a request on to its upstream only while it is up. It
// takes effect for the next request, with no config reload, so a caller
// that is about to release the forward's port says it is down first and
// the proxy never dials the port once it is free.
func SetForwardUp(upstream string, up bool) bool {
	forwards.Lock()
	defer forwards.Unlock()
	if forwards.up[upstream] == up {
		return false
	}
	if up {
		forwards.up[upstream] = true
	} else {
		delete(forwards.up, upstream)
	}
	return true
}

// forwardUp reports whether the forward at upstream is up.
func forwardUp(upstream string) bool {
	forwards.RLock()
	defer forwards.RUnlock()
	return forwards.up[upstream]
}

func init() {
	caddy.RegisterModule(Gate{})
}

// Gate is a Caddy HTTP handler placed before a site's reverse proxy when
// the site's upstream is a forward. It passes a request on only while
// SetForwardUp says the forward is up, and otherwise fails it with 502,
// which the error routes answer with Nearhost's page (see Down). With no
// Upstream it fails every request: the route of a site that is down.
//
// The gate is checked for each request, so a forward that goes down stops
// being dialled at once, without waiting for a config reload, which takes
// time and leaves requests that arrive meanwhile with the old routes.
type Gate struct {
	// Upstream is the address of the forward, as the reverse proxy dials it.
	Upstream string `json:"upstream,omitempty"`
}

// CaddyModule implements caddy.Module.
func (Gate) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + gateModule,
		New: func() caddy.Module { return new(Gate) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if g.Upstream == "" || !forwardUp(g.Upstream) {
		return caddyhttp.Error(http.StatusBadGateway, errForwardDown)
	}
	return next.ServeHTTP(w, r)
}

var _ caddyhttp.MiddlewareHandler = (*Gate)(nil)
