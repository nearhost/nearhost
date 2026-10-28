package proxy

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// homeModule is the Caddy handler module name registered below.
const homeModule = "nearhost_home"

// homeHost is where the home page is served: on the https port while no
// project serves it, and on the web port always.
const homeHost = "localhost"

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
// https://localhost while no project serves localhost, and at
// https://localhost:<web port> always. It is a module of
// its own, rather than a static response in the config, so the page can
// grow without touching routing. It answers every request itself and never
// calls the next handler.
type Home struct {
	// Version is the Nearhost version shown in the footer. Empty means dev.
	Version string `json:"version,omitempty"`
	// WebPort is the port of its own the page is also served on. 0 means
	// none.
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
// the documents handler SetDocuments stored, which sets its own headers.
// Otherwise only / exists, and only GET and HEAD read it. It lists the
// documents' trees, rendered per request, while there are any.
func (h *Home) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if isDocumentPath(r.URL.Path) {
		serveDocument(w, r)
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
	page := h.page
	if trees := homeTrees(); len(trees) > 0 {
		var err error
		if page, err = h.render(trees); err != nil {
			return err
		}
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(len(page)))
	hdr.Set("Content-Security-Policy", homeCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	// The footer names the running version, which changes on upgrade.
	hdr.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(page)
	return err
}

var (
	_ caddy.Provisioner           = (*Home)(nil)
	_ caddyhttp.MiddlewareHandler = (*Home)(nil)
)
