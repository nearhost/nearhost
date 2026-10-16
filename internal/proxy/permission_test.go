package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddytls"
)

func TestPermissionAllowsOnlyNamesMatchingAPattern(t *testing.T) {
	p := &Permission{Patterns: []string{"*.local.example.pro", "internal.*.local.example.pro"}}
	for name, want := range map[string]bool{
		"acme.local.example.pro":          true,
		"ACME.local.example.pro":          true,
		"internal.acme.local.example.pro": true,
		"local.example.pro":               false,
		"a.b.local.example.pro":           false,
		"evil.example.com":                false,
	} {
		err := p.CertificateAllowed(context.Background(), name)
		if (err == nil) != want {
			t.Errorf("%s: err = %v, want allowed = %v", name, err, want)
		}
		if err != nil && !errors.Is(err, caddytls.ErrPermissionDenied) {
			t.Errorf("%s: err = %v, want one wrapping ErrPermissionDenied", name, err)
		}
	}
}
