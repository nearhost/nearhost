package proxy

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// downModule is the Caddy handler module name registered below.
const downModule = "nearhost_down"

// thisMachine names the machine Nearhost runs on, for a project with no
// server of its own.
const thisMachine = "this machine"

//go:embed home/down.html
var downHTML string

// downPage is parsed once, at start-up, so a broken page fails every test
// rather than a request. html/template escapes the names for the HTML text
// they land in.
var downPage = template.Must(template.New("down").Parse(downHTML))

func init() {
	caddy.RegisterModule(Down{})
}

// Down is a Caddy HTTP handler for the https server's error routes. When a
// site's app does not answer, the reverse proxy fails with 502, 503 or 504
// and Caddy runs the error routes; there a browser asking for a page gets
// Nearhost's page saying so, with that status, in place of an empty body.
// Any other request, and any other error, gets the bare status, as it would
// with no error route. An app that answers with 502 itself is no error, so
// its answer never reaches this handler.
//
// It is a module of its own, rather than a static response in the config,
// because a static response's body goes through Caddy's placeholders, which
// would expand braces in a project's name.
type Down struct {
	// Project is the name of the project whose app did not answer.
	Project string `json:"project,omitempty"`
	// Port is the port the app listens on, on its server.
	Port int `json:"port,omitempty"`
	// Server is the server the app runs on. Empty means this machine.
	Server string `json:"server,omitempty"`
	// LogOnly makes the handler only log the error and leave the bare
	// status, for the error route that catches errors on hosts with no
	// site: once a server has error routes, Caddy logs the errors they
	// handle only at debug level.
	LogOnly bool `json:"log_only,omitempty"`

	page   []byte
	logger *zap.Logger
}

// CaddyModule implements caddy.Module.
func (Down) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + downModule,
		New: func() caddy.Module { return new(Down) },
	}
}

// Provision implements caddy.Provisioner. The page depends only on the
// handler's fields, so it is rendered once per config load.
func (d *Down) Provision(ctx caddy.Context) error {
	d.logger = ctx.Logger()
	if d.LogOnly {
		return nil
	}
	var err error
	d.page, err = renderDownPage(d.Project, d.Port, d.Server, false)
	return err
}

// renderDownPage renders Nearhost's page for project's app, which listens
// on port on server (empty for this machine): that nothing answered there,
// or with reconnecting, that Nearhost is reconnecting to the server.
func renderDownPage(project string, port int, server string, reconnecting bool) ([]byte, error) {
	if server == "" {
		server = thisMachine
	}
	var buf bytes.Buffer
	err := downPage.Execute(&buf, struct {
		Project      string
		Port         int
		Server       string
		Reconnecting bool
	}{project, port, server, reconnecting})
	return buf.Bytes(), err
}

// ServeHTTP implements caddyhttp.MiddlewareHandler. It logs the error at
// the level Caddy uses for a server with no error routes, since Caddy logs
// an error that an error route handled only at debug level. Then it serves the page
// for a 502, 503 or 504 to a GET or HEAD that accepts HTML and asks for no
// upgrade, and passes anything else, and everything in LogOnly mode, to
// next, which writes the bare status.
func (d *Down) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	err, _ := r.Context().Value(caddyhttp.ErrorCtxKey).(error)
	status := http.StatusInternalServerError
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if herr, ok := errors.AsType[caddyhttp.HandlerError](err); ok {
		status = herr.StatusCode
		if herr.Err != nil {
			msg = herr.Err.Error()
		}
	}
	if d.logger != nil {
		level := zapcore.DebugLevel
		if status >= 500 {
			level = zapcore.ErrorLevel
		}
		if c := d.logger.Check(level, msg); c != nil {
			c.Write(zap.Object("request", caddyhttp.LoggableHTTPRequest{Request: r}), zap.Int("status", status))
		}
	}
	if d.LogOnly || !downStatus(status) || !wantsPage(r) {
		return next.ServeHTTP(w, r)
	}
	return writePage(w, r, status, d.page)
}

// writePage answers r with status and page, one of Nearhost's own pages,
// with no body for a HEAD.
func writePage(w http.ResponseWriter, r *http.Request, status int, page []byte) error {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(len(page)))
	hdr.Set("Content-Security-Policy", homeCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(page)
	return err
}

// downStatus reports whether status is one the reverse proxy fails with
// when the app does not answer.
func downStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// wantsPage reports whether r is a browser asking for a page: a GET or
// HEAD that accepts HTML and asks for no upgrade, such as a WebSocket. A
// script's fetch, an API client, a request with a body and an upgrade get
// the bare status.
func wantsPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	return strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Accept"), ",")), "text/html")
}

var (
	_ caddy.Provisioner           = (*Down)(nil)
	_ caddyhttp.MiddlewareHandler = (*Down)(nil)
)
