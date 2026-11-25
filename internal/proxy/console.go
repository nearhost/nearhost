package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"

	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
)

// consoleCSP is the policy of every response of the console: the page's
// own policy, plus its own scripts and event stream, and forms that post
// back to it alone.
const consoleCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// csrfLabel is what the page secret signs to make the form token.
const csrfLabel = "nearhost page form"

// csrfField is the form field every form on the page carries the token in.
const csrfField = "csrf"

// consoleFormBytes bounds the body of a POST to the console. Its forms
// hold a token and a choice or two.
const consoleFormBytes = 64 << 10

// consoleHandler wraps what SetConsole stored, as atomic.Pointer needs a
// concrete type.
type consoleHandler struct {
	h http.Handler
}

// console holds what SetConsole stored last, nil while nothing is set. It
// is package-level, as documents is, because Caddy builds a new Home on
// every config load while the console outlives them all.
var console atomic.Pointer[consoleHandler]

// SetConsole sets the handler for the page's status and approvals, or
// clears it with nil. Home sends it, after the gate, GET and HEAD for /,
// /approve/{id}, /change/{name}, /events and /page.js, and POST for
// /approve/{id} and /change/{name}.
func SetConsole(h http.Handler) {
	if h == nil {
		console.Store(nil)
		return
	}
	console.Store(&consoleHandler{h: h})
}

// currentConsole returns the console set now, nil while there is none.
func currentConsole() http.Handler {
	c := console.Load()
	if c == nil {
		return nil
	}
	return c.h
}

// CSRFToken is the token every form on the page carries as the field
// "csrf": hex(HMAC-SHA256(page secret, "nearhost page form")).
func CSRFToken() (string, error) {
	secret, err := pageSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(csrfLabel))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// consoleActionPath matches the console's paths that take a POST: one
// approval or one project's change, a single path segment each.
var consoleActionPath = regexp.MustCompile(`^/(approve|change)/[^/]+$`)

// keyNext matches the next parameter of /?k=: the only places the keyed
// address sends the browser on to, other than /.
var keyNext = regexp.MustCompile(`^/(approve|change)/[^/?#]+$`)

// consoleRoute reports whether path is one of the console's, other than /,
// and whether it takes a POST.
func consoleRoute(path string) (ok, post bool) {
	switch {
	case consoleActionPath.MatchString(path):
		return true, true
	case path == "/events", path == "/page.js":
		return true, false
	}
	return false, false
}

// lockedHTML is the page that says how to get the cookie.
var lockedHTML = htmlview.Render(report.Errorf(report.ProxyLocked, "Open Nearhost's page from this machine first").
	Because("Run nearhost status here and open the page address it prints, or click the page address in nearhost. Then open this link again."), "Nearhost")

// serveLocked answers a request for a console path that did not pass the
// gate. A GET or HEAD gets the page that says how to get the cookie, the
// same for every path, so it tells nothing of what was asked for; anything
// else gets a plain 403.
func serveLocked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		forbid(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(len(lockedHTML)))
	hdr.Set("Content-Security-Policy", homeCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Vary", "Cookie")
	w.WriteHeader(http.StatusForbidden)
	if r.Method != http.MethodHead {
		w.Write(lockedHTML)
	}
}

// forbid answers a plain 403.
func forbid(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	refuse(w, r, report.ProxyForbidden, http.StatusForbidden)
}

// sameOrigin reports whether r, a POST, comes from the page itself: its
// Origin is the page's own, scheme, host and port, or, from a browser that
// sends no Origin, Sec-Fetch-Site says same-origin. Origin null, as a
// sandboxed document or a redirect sends, is never the page.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	switch len(origins) {
	case 0:
		return r.Header.Get("Sec-Fetch-Site") == "same-origin"
	case 1:
		return origins[0] == "https://"+r.Host
	}
	return false
}

// tokenMatches reports whether r's form, its body alone and not the query,
// carries the form token in the field csrf, compared in constant time. It
// parses the form, so the console finds it parsed.
func tokenMatches(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, consoleFormBytes)
	if err := r.ParseForm(); err != nil {
		return false
	}
	got := r.PostForm[csrfField]
	if len(got) != 1 {
		return false
	}
	want, err := CSRFToken()
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[0]), []byte(want)) == 1
}

// serveConsole sends r, a request for one of the console's paths with a
// method it takes, through the gate to con. It needs the page cookie, at
// hostname.PageHost, or a paired device's cookie at a page host; a POST
// needs the page's own origin and the form token too. What passes gets the
// console's headers, which con may add to.
func (h *Home) serveConsole(con http.Handler, w http.ResponseWriter, r *http.Request) {
	if !h.pageAuthorized(r) {
		serveLocked(w, r)
		return
	}
	if r.Method == http.MethodPost && (!sameOrigin(r) || !tokenMatches(w, r)) {
		forbid(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", consoleCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Vary", "Cookie")
	con.ServeHTTP(w, r)
}

// keyRedirect is where /?k= sends the browser on to: next when it is an
// approval or a project's change, otherwise /.
func keyRedirect(next string) string {
	if keyNext.MatchString(next) {
		return next
	}
	return "/"
}
