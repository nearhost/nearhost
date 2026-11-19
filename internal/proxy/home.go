package proxy

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/nearhost/nearhost/internal/hostname"
)

// homeModule is the Caddy handler module name registered below.
const homeModule = "nearhost_home"

// homeHost is the page's other host, beside hostname.PageHost, on the web
// port alone. The page answers there, but never gives or honours its cookie
// there (see setPageCookie).
const homeHost = "localhost"

// pageHosts lists the hosts the home page is served at on the web port:
// hostname.PageHost, its own, and homeHost.
func pageHosts() []string {
	return []string{hostname.PageHost, homeHost}
}

// homeCSP lets the page use its own inline styles and data: images and
// nothing else, so it can never make a request that leaves the machine,
// even if a later version of the page forgets.
const homeCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

//go:embed home/index.html
var homeHTML string

// homePage is parsed once, at start-up, so a broken page fails every test
// rather than a request. html/template escapes the version for the HTML
// text it lands in.
var homePage = template.Must(template.New("home").Parse(homeHTML))

func init() {
	caddy.RegisterModule(Home{})
}

// Home is a Caddy HTTP handler that serves Nearhost's own page, at
// https://nearhost.localhost:<web port> and https://localhost:<web port>
// and nowhere else. It is a module of its own, rather than a static
// response in the config, so the page can grow without touching routing.
// It answers every request itself and never calls the next handler.
type Home struct {
	// Version is the Nearhost version shown in the footer. Empty means dev.
	Version string `json:"version,omitempty"`
	// WebPort is the port the page is served on.
	WebPort int `json:"web_port,omitempty"`

	// page is the page with no documents listed, rendered in Provision.
	page []byte
}

// CaddyModule implements caddy.Module.
func (Home) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + homeModule,
		New: func() caddy.Module { return new(Home) },
	}
}

// Provision implements caddy.Provisioner. Without documents the page
// depends only on the version and the web port, so it is rendered once per
// config load, not per request.
func (h *Home) Provision(caddy.Context) error {
	page, err := h.render(nil)
	if err != nil {
		return err
	}
	h.page = page
	return nil
}

// render renders the page with trees listed under its links.
func (h *Home) render(trees []HomeTree) ([]byte, error) {
	v := h.Version
	if v == "" {
		v = "dev"
	}
	var buf bytes.Buffer
	if err := homePage.Execute(&buf, struct {
		Version string
		WebPort int
		Trees   []HomeTree
	}{v, h.WebPort, trees}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler. Paths below /d/ go to
// the documents handler SetDocuments stored, which sets its own headers; a
// successful answer there at hostname.PageHost also gives the browser the
// page cookie, as the link proves it was given a document. Otherwise only /
// exists, and only GET and HEAD read it. /?k=<secret> gives the cookie for
// the right secret and sends the browser on to /. / lists the documents'
// trees, rendered per request, only to a browser with the cookie; any other
// gets the page with no list, which shows nothing of the documents, not even
// whether any are. The cookie is given and read at hostname.PageHost alone
// (see setPageCookie): at localhost, /?k= redirects and sets nothing, and /
// never lists.
func (h *Home) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if isDocumentPath(r.URL.Path) {
		serveDocument(&cookieOnSuccess{ResponseWriter: w, r: r}, r)
		return nil
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return nil
	}
	hdr := w.Header()
	if q := r.URL.Query(); q.Has(pageKeyParam) {
		if secret, err := pageSecret(); err == nil && secretMatches(q.Get(pageKeyParam), secret) {
			setPageCookie(hdr, r)
		}
		// The secret leaves the address bar with the redirect, and is
		// neither sent on as a referrer nor kept in a cache.
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Cache-Control", "no-store")
		hdr.Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
		return nil
	}
	page := h.page
	if hasPageCookie(r) {
		if trees := homeTrees(); len(trees) > 0 {
			var err error
			if page, err = h.render(trees); err != nil {
				return err
			}
		}
	}
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(len(page)))
	hdr.Set("Content-Security-Policy", homeCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	// The footer names the running version, which changes on upgrade, and
	// the list depends on the cookie.
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Vary", "Cookie")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(page)
	return err
}

// cookieOnSuccess adds the page cookie to a document's response when its
// status is 2xx, and to nothing else: a 404 for an unknown id, a redirect
// or an error page gives the browser nothing.
type cookieOnSuccess struct {
	http.ResponseWriter
	// r is the request answered, which decides whether the cookie is set
	// at all (see setPageCookie).
	r     *http.Request
	wrote bool
}

// WriteHeader adds the cookie for a 2xx status before passing it on.
func (c *cookieOnSuccess) WriteHeader(status int) {
	if !c.wrote {
		c.wrote = true
		if status >= 200 && status < 300 {
			setPageCookie(c.Header(), c.r)
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

// Write sends the status 200 first when none was sent, as
// http.ResponseWriter does.
func (c *cookieOnSuccess) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap returns the response writer underneath, for
// http.ResponseController.
func (c *cookieOnSuccess) Unwrap() http.ResponseWriter { return c.ResponseWriter }

var (
	_ caddy.Provisioner           = (*Home)(nil)
	_ caddyhttp.MiddlewareHandler = (*Home)(nil)
)
