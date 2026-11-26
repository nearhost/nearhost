package proxy

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// deviceModule is the Caddy handler module name registered below.
const deviceModule = "nearhost_device"

func init() {
	caddy.RegisterModule(DeviceGuard{})
}

// DeviceGuard is a Caddy HTTP handler on every site route of a networked
// core, right after the tag: a client in Local passes as it always has, and
// any other must come from a paired device, or is sent to Page to pair.
// This is the stub the config loads with; it passes every request on.
type DeviceGuard struct {
	// Page is the page's network origin, https://nearhost.<domain>:<port>.
	Page string `json:"page,omitempty"`
	// Local are the client ranges that need no device: loopback and the
	// site's own extra clients.
	Local []string `json:"local,omitempty"`
}

// CaddyModule implements caddy.Module.
func (DeviceGuard) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + deviceModule,
		New: func() caddy.Module { return new(DeviceGuard) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (g *DeviceGuard) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	return next.ServeHTTP(w, r)
}

var _ caddyhttp.MiddlewareHandler = (*DeviceGuard)(nil)
