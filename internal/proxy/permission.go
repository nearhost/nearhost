package proxy

import (
	"context"
	"fmt"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"

	"github.com/nearhost/nearhost/internal/hostname"
)

// permissionModule is the Caddy on-demand permission module name registered
// below.
const permissionModule = "nearhost"

func init() {
	caddy.RegisterModule(Permission{})
}

// Permission decides which names get a certificate on demand. A pattern
// such as internal.*.local.example.io cannot be a certificate subject (a
// certificate's wildcard can only be its leftmost label), and every pattern
// is handled alike, so each name any pattern serves is issued its own
// certificate at its first handshake. Only names matching a served pattern
// are allowed, so a client cannot make the proxy issue certificates for
// names it does not serve.
type Permission struct {
	Patterns []string `json:"patterns"`
}

// CaddyModule implements caddy.Module.
func (Permission) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tls.permission." + permissionModule,
		New: func() caddy.Module { return new(Permission) },
	}
}

// CertificateAllowed implements caddytls.OnDemandPermission.
func (p *Permission) CertificateAllowed(_ context.Context, name string) error {
	for _, pattern := range p.Patterns {
		if hostname.Match(pattern, name) {
			return nil
		}
	}
	return fmt.Errorf("%s matches no served pattern: %w", name, caddytls.ErrPermissionDenied)
}

var _ caddytls.OnDemandPermission = (*Permission)(nil)
