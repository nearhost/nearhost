package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nearhost/nearhost/internal/report"
)

// apiPrefix is where the page serves the API: the control routes the
// network may reach, at /api/v1/….
const apiPrefix = "/api"

// apiCSP is the policy of every answer under /api/: nothing in it is a
// document, so it may load nothing and be framed by nothing.
const apiCSP = "default-src 'none'; frame-ancestors 'none'"

// apiTokenHeader is the header a write to the API carries the form token
// in (see CSRFToken). A form from another site cannot set it.
const apiTokenHeader = "X-Nearhost-CSRF"

// apiBodyBytes bounds the body of a request to the API.
const apiBodyBytes = 1 << 20

// apiRecheck is how often a request the gate has passed on is checked for
// its credential again while it runs, so that an event stream ends soon
// after its device is revoked or the page secret changes. Tests shorten it.
var apiRecheck = 3 * time.Second

// apiWatchers counts the credential watchers running (see watchCredential),
// so tests can see that none outlives its request.
var apiWatchers atomic.Int64

// apiHandler wraps what SetAPI stored, as atomic.Pointer needs a concrete
// type.
type apiHandler struct {
	h http.Handler
}

// api holds what SetAPI stored last, nil while nothing is set. It is
// package-level, as console is, because Caddy builds a new Home on every
// config load while the API outlives them all.
var api atomic.Pointer[apiHandler]

// SetAPI sets the handler for the API at /api, or clears it with nil. Home
// sends it, after the gate (see serveAPI), every request whose escaped path
// is /api or begins /api/, with the path as it came; the handler strips the
// prefix itself.
func SetAPI(h http.Handler) {
	if h == nil {
		api.Store(nil)
		return
	}
	api.Store(&apiHandler{h: h})
}

// currentAPI returns the API set now, nil while there is none.
func currentAPI() http.Handler {
	a := api.Load()
	if a == nil {
		return nil
	}
	return a.h
}

// isAPIPath reports whether the escaped path p is the API's: /api itself
// or below /api/, and not /apix or /api.js.
func isAPIPath(p string) bool {
	return p == apiPrefix || strings.HasPrefix(p, apiPrefix+"/")
}

// apiSignInMessage is the 401's message: the same for every path, so it
// says nothing of whether one exists.
func apiSignInMessage() *report.Message {
	return report.Errorf(report.ProxyAPISignIn, "Sign in to Nearhost's page first").
		Because("Open Nearhost's page on this machine, or pair this device with it, and try again.")
}

// serveAPI sends r, a request for a path under /api, through the gate to
// the handler SetAPI stored. Every answer carries the API's headers, set
// before anything else, so the handler's answers do too; it may add to
// them. With no handler stored the path is not found, as an unknown path
// of the API is. Otherwise it needs the page cookie, at hostname.PageHost,
// or a paired device's cookie at a page host: without, 401, the same for
// every path. Only GET and HEAD read; any other method is a write, which
// needs the page's own origin, the form token in X-Nearhost-CSRF and a
// JSON body, or gets 403. An OPTIONS is a write too, but a preflight
// carries no credentials, so it gets the 401 first; either way the answer
// has no CORS headers. GET /api/v1/session is answered here, not by the
// handler (see serveSession). The
// body is capped at apiBodyBytes; a handler that reads past the cap gets
// an *http.MaxBytesError.
//
// The ResponseWriter is passed on as it came, so the handler's event
// stream can flush and set its write deadlines. The request's context is
// cancelled if its credential stops being valid while it runs (see
// watchCredential).
func (h *Home) serveAPI(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", apiCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Vary", "Cookie")
	hdr.Set("Referrer-Policy", "same-origin")
	a := currentAPI()
	if a == nil {
		report.WriteProblemStatus(w, report.Errorf(report.ControlNoRoute, "no such route: %s", r.URL.Path), http.StatusNotFound, nil)
		return
	}
	if !h.pageAuthorized(r) {
		report.WriteProblemStatus(w, apiSignInMessage(), http.StatusUnauthorized, nil)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && (!sameOrigin(r) || !apiTokenMatches(r) || !isJSON(r)) {
		report.WriteProblemStatus(w, report.Errorf(report.ProxyAPIWriteRefused, "The change was refused").
			Because("A change through Nearhost's API must come from Nearhost's page itself, with its token, as JSON."), http.StatusForbidden, nil)
		return
	}
	if r.URL.EscapedPath() == apiSessionPath {
		h.serveSession(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, apiBodyBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := h.watchCredential(r, cancel)
	defer stop()
	a.ServeHTTP(w, r.WithContext(ctx))
}

// watchCredential checks r's credential every apiRecheck while r runs and
// calls cancel, ending r's context, once it is no longer valid: a paired
// device revoked, or the page secret changed. A request passes the gate
// once, so without this an event stream would go on after its device is
// revoked. The returned stop ends the watch and returns once it has.
func (h *Home) watchCredential(r *http.Request, cancel context.CancelFunc) (stop func()) {
	quit := make(chan struct{})
	exited := make(chan struct{})
	apiWatchers.Add(1)
	go func() {
		defer close(exited)
		defer apiWatchers.Add(-1)
		t := time.NewTicker(apiRecheck)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if !h.pageAuthorized(r) {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-exited
	}
}

// apiSessionPath is the one path under /api the gate answers itself.
const apiSessionPath = apiPrefix + "/v1/session"

// Session is the body of GET /api/v1/session: what the app needs to know
// about itself, including the token its writes carry.
type Session struct {
	// CSRF is the form token, for X-Nearhost-CSRF.
	CSRF string `json:"csrf"`
	// Device is the name of the paired device making the request, empty
	// for the page cookie.
	Device string `json:"device"`
	// Networked reports whether the core is in networked mode.
	Networked bool `json:"networked"`
	// Machine is this machine's name, empty when unknown.
	Machine string `json:"machine"`
}

// serveSession answers GET and HEAD with the Session of r, which has
// passed the gate; any other method is a 405 naming those two.
func (h *Home) serveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		report.WriteProblemStatus(w, report.Errorf(report.ProxyMethodNotAllowed, "%s does not take %s; it takes GET, HEAD", apiSessionPath, r.Method), http.StatusMethodNotAllowed, nil)
		return
	}
	token, err := CSRFToken()
	if err != nil {
		report.WriteProblemStatus(w, report.Errorf(report.Unexpected, "The session could not be read: %v", err), http.StatusInternalServerError, nil)
		return
	}
	s := Session{CSRF: token, Networked: h.NetworkHost != "", Machine: h.Machine}
	if dev, ok := h.pairedDevice(r); ok {
		s.Device = dev.Name
	}
	body, err := json.Marshal(s)
	if err != nil {
		report.WriteProblemStatus(w, report.Errorf(report.Unexpected, "The session could not be read: %v", err), http.StatusInternalServerError, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write(body)
	}
}

// apiTokenMatches reports whether r carries the form token, once, in
// X-Nearhost-CSRF, compared in constant time.
func apiTokenMatches(r *http.Request) bool {
	got := r.Header.Values(apiTokenHeader)
	if len(got) != 1 {
		return false
	}
	want, err := CSRFToken()
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[0]), []byte(want)) == 1
}

// isJSON reports whether r's one Content-Type is application/json, in any
// case and with any parameters. No form a page on another site can send
// without asking first is.
func isJSON(r *http.Request) bool {
	ct := r.Header.Values("Content-Type")
	if len(ct) != 1 {
		return false
	}
	t, _, err := mime.ParseMediaType(ct[0])
	return err == nil && t == "application/json"
}
