package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// frameModule is the Caddy handler module name registered below.
const frameModule = "nearhost_frame"

func init() {
	caddy.RegisterModule(FrameRule{})
}

// FrameRule is a Caddy HTTP handler on the site and down routes of a
// networked core, right after the device guard: for a frame load the page
// itself made, it removes the app's anti-framing headers
// (X-Frame-Options and CSP frame-ancestors) so the page can show the
// project. Every other request keeps the app's headers.
type FrameRule struct {
	// Page is the page's network origin, the same as DeviceGuard.Page.
	Page string `json:"page,omitempty"`
}

// CaddyModule implements caddy.Module.
func (FrameRule) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + frameModule,
		New: func() caddy.Module { return new(FrameRule) },
	}
}

// frameAllowed reports whether a response may drop its anti-framing
// headers: the request is a frame load, and either the page opened it (the
// referer's origin is page) or it is a navigation inside a frame already
// showing the same site (Sec-Fetch-Site: same-origin).
func frameAllowed(r *http.Request, page string) bool {
	if r.Header.Get("Sec-Fetch-Dest") != "iframe" {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host == "" {
		return false
	}
	return ref.Scheme+"://"+ref.Host == page
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (f *FrameRule) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if frameAllowed(r, f.Page) {
		w = &frameWriter{ResponseWriter: w}
	}
	return next.ServeHTTP(w, r)
}

// frameWriter removes the anti-framing headers once, as the response
// starts.
type frameWriter struct {
	http.ResponseWriter
	done bool
}

func (f *frameWriter) fix() {
	if f.done {
		return
	}
	f.done = true
	h := f.Header()
	h.Del("X-Frame-Options")
	// Each line is a policy of its own, all of them enforced, so each is
	// rewritten alone: merged, a later line's directive would be ignored
	// after an earlier one of the same name, loosening the app's policy.
	lines := h.Values("Content-Security-Policy")
	var out []string
	changed := false
	for _, line := range lines {
		var kept []string
		dropped := false
		for _, d := range strings.Split(line, ";") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if f := strings.Fields(d); strings.EqualFold(f[0], "frame-ancestors") {
				dropped = true
				continue
			}
			kept = append(kept, d)
		}
		switch {
		case !dropped:
			out = append(out, line)
		case len(kept) > 0:
			out = append(out, strings.Join(kept, "; "))
		}
		changed = changed || dropped
	}
	if !changed {
		return
	}
	if len(out) == 0 {
		h.Del("Content-Security-Policy")
		return
	}
	h["Content-Security-Policy"] = out
}

// WriteHeader implements http.ResponseWriter.
func (f *frameWriter) WriteHeader(code int) {
	// Informational responses (1xx, but 101) are not the final headers.
	if code >= 200 || code == http.StatusSwitchingProtocols {
		f.fix()
	}
	f.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (f *frameWriter) Write(b []byte) (int, error) {
	f.fix()
	return f.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController, and Caddy, reach the real writer for
// Flush and Hijack.
func (f *frameWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }

var _ caddyhttp.MiddlewareHandler = (*FrameRule)(nil)
