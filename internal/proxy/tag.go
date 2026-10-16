package proxy

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// tagModule is the Caddy handler module name registered below.
const tagModule = "nearhost_tag"

// aliasField is the access log field that names the project a request was
// served for. The access writer files each line under it.
const aliasField = "nearhost_alias"

func init() {
	caddy.RegisterModule(Tag{})
}

// Tag is a Caddy HTTP handler that names the project in the request's
// access log line, as nearhost_alias. Caddy can only pick a logger by host
// for patterns whose wildcards are on the left, so the project is carried
// on the line instead, and one access logger serves every site.
type Tag struct {
	Alias string `json:"alias,omitempty"`
}

// CaddyModule implements caddy.Module.
func (Tag) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + tagModule,
		New: func() caddy.Module { return new(Tag) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (t *Tag) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if extra, ok := r.Context().Value(caddyhttp.ExtraLogFieldsCtxKey).(*caddyhttp.ExtraLogFields); ok {
		extra.Set(zap.String(aliasField, t.Alias))
	}
	return next.ServeHTTP(w, r)
}

var _ caddyhttp.MiddlewareHandler = (*Tag)(nil)
