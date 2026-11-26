package proxy

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
)

//go:embed home/pair.html
var pairHTML string

//go:embed home/pair.js
var pairJS []byte

// pairPage is the pair form, parsed once at start-up.
var pairPage = template.Must(template.New("pair").Parse(pairHTML))

// pairCodePath matches /pair/{code}: one path segment, matched escaped.
var pairCodePath = regexp.MustCompile(`^/pair/[^/]+$`)

// maxDeviceName is how many runes of the name typed on the pair form are
// kept.
const maxDeviceName = 64

// pairCodeInvalid is the pair form's error, for a code that is wrong, used
// or expired.
func pairCodeInvalid() *htmlview.Data {
	d := htmlview.Prepare(report.Errorf(report.PairCodeInvalid, "That pairing code is wrong, or has been used or has expired").
		Step("Get a new code on the machine Nearhost runs on:", "nearhost pair"))
	return &d
}

// pairForm is what the pair form is rendered with.
type pairForm struct {
	// Code is the code from the path, sent as a hidden field; empty shows
	// a field to type it in.
	Code string
	// Error is the error the form shows, nil for none.
	Error *htmlview.Data
	// Machine is the name of the machine Nearhost runs on, "" when it is
	// not known.
	Machine string
}

// servePairing answers pairing's paths at the page's host while the core is
// networked and devices are set, and reports whether it did: /pair and /pair/{code} (the form,
// and its POST), /pair.js, /auth, and, with certificates from the local
// CA, /profile.mobileconfig. Anything else is left to the page.
func (h *Home) servePairing(w http.ResponseWriter, r *http.Request) bool {
	// A core that is not networked has no pairing: its page answers as it
	// always did, though the store may stay open for networking to start.
	if h.NetworkHost == "" {
		return false
	}
	path := r.URL.EscapedPath()
	if path == "/profile.mobileconfig" {
		if h.Certificates != "ca" || !h.atPairingHost(r) {
			return false
		}
		if allowMethods(w, r, false) {
			h.serveProfile(w, r)
		}
		return true
	}
	isPair := path == "/pair" || pairCodePath.MatchString(path)
	if !isPair && path != "/pair.js" && path != "/auth" {
		return false
	}
	d := currentDevices()
	if d == nil || !h.atPairingHost(r) {
		return false
	}
	switch {
	case path == "/pair" && r.Method == http.MethodPost:
		h.servePairPost(w, r, d)
	case !allowMethods(w, r, path == "/pair"):
	case isPair:
		code := ""
		if path != "/pair" {
			code = strings.TrimPrefix(r.URL.Path, "/pair/")
		}
		servePairForm(w, r, pairForm{Code: code, Machine: h.Machine}, http.StatusOK)
	case path == "/pair.js":
		hdr := w.Header()
		hdr.Set("Content-Type", "text/javascript; charset=utf-8")
		hdr.Set("Content-Length", strconv.Itoa(len(pairJS)))
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write(pairJS)
		}
	default:
		h.serveAuth(w, r, d)
	}
	return true
}

// allowMethods reports whether r's method is GET or HEAD, and otherwise
// answers 405, naming POST too when post.
func allowMethods(w http.ResponseWriter, r *http.Request, post bool) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if post {
		w.Header().Set("Allow", "GET, HEAD, POST")
	} else {
		w.Header().Set("Allow", "GET, HEAD")
	}
	refuse(w, r, report.ProxyMethodNotAllowed, http.StatusMethodNotAllowed)
	return false
}

// servePairForm renders the pair form with f and status.
func servePairForm(w http.ResponseWriter, r *http.Request, f pairForm, status int) {
	var buf bytes.Buffer
	if err := pairPage.Execute(&buf, f); err != nil {
		refuse(w, r, report.PageRenderFailed, http.StatusInternalServerError)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(buf.Len()))
	hdr.Set("Content-Security-Policy", consoleCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	// The code in the path is not sent on as a referrer.
	hdr.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}

// servePairPost pairs the device that sent the form: from the page's own
// origin, by a client Allow admits, with a good code. It gets the device
// cookie and is sent to /. A bad code gets the form again, saying so.
func (h *Home) servePairPost(w http.ResponseWriter, r *http.Request, d DeviceAuth) {
	if !sameOrigin(r) {
		forbid(w, r)
		return
	}
	addr, ok := clientAddr(r)
	if !ok {
		forbid(w, r)
		return
	}
	client := addr.String()
	if !d.Allow(client) {
		w.Header().Set("Cache-Control", "no-store")
		tooManyTries(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, consoleFormBytes)
	if err := r.ParseForm(); err != nil {
		forbid(w, r)
		return
	}
	token, err := d.Pair(r.PostForm.Get("code"), deviceName(r.PostForm.Get("name")), client)
	switch {
	case errors.Is(err, devices.ErrCodeInvalid):
		servePairForm(w, r, pairForm{Error: pairCodeInvalid(), Machine: h.Machine}, http.StatusBadRequest)
		return
	case err != nil:
		emit("", "A device could not be paired: "+err.Error())
		w.Header().Set("Cache-Control", "no-store")
		htmlview.Write(w, r, report.Errorf(report.Unexpected, "Nearhost could not save the paired device").
			Because("Try again. If it keeps failing, check Nearhost's log on the machine it runs on."), http.StatusInternalServerError, "Nearhost")
		return
	}
	hdr := w.Header()
	hdr.Add("Set-Cookie", pairingCookie(deviceCookie, token))
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

// deviceName is name as typed on the pair form, with its control and
// format characters dropped, trimmed, and cut to maxDeviceName runes;
// "device" when nothing is left.
func deviceName(name string) string {
	var b strings.Builder
	n := 0
	for _, c := range name {
		if c == utf8.RuneError || unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			continue
		}
		if n == maxDeviceName {
			break
		}
		b.WriteRune(c)
		n++
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return "device"
}

// serveAuth answers /auth?to=<url> for a paired device: to must be an
// https address under the network domain, and the device is sent there
// with a one-time token for that host, at /.nearhost/auth. A browser with
// no paired device is sent to /pair.
func (h *Home) serveAuth(w http.ResponseWriter, r *http.Request, d DeviceAuth) {
	hdr := w.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	dev, ok := h.pairedDevice(r)
	if !ok {
		hdr.Set("Location", "/pair")
		w.WriteHeader(http.StatusFound)
		return
	}
	to, err := url.Parse(r.URL.Query().Get("to"))
	if err != nil || to.Scheme != "https" || to.User != nil || to.Opaque != "" || !h.underNetworkDomain(to.Hostname()) {
		htmlview.Write(w, r, report.Errorf(report.PairBadTarget, "Nearhost signs devices in to sites under its own domain only").
			Because("The address asked for is not one of Nearhost's sites. Open the site from its own address instead."), http.StatusBadRequest, "Nearhost")
		return
	}
	token := d.NewAuthToken(dev.ID, strings.ToLower(to.Hostname()))
	hdr.Set("Location", "https://"+to.Host+siteAuthPath+"?"+url.Values{"t": {token}, "to": {to.RequestURI()}}.Encode())
	w.WriteHeader(http.StatusFound)
}

// underNetworkDomain reports whether host is the network domain, this
// page's network host without its "nearhost.", or a name beneath it,
// ignoring case. It is false while the core is not networked, and for a
// host that is not a valid name, such as an IPv6 address with a zone that
// ends in the domain.
func (h *Home) underNetworkDomain(host string) bool {
	domain, ok := strings.CutPrefix(strings.ToLower(h.NetworkHost), "nearhost.")
	if !ok || domain == "" {
		return false
	}
	host = strings.ToLower(host)
	if hostname.Valid(host) != nil || hostname.IsPattern(host) {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// serveProfile answers /profile.mobileconfig: a configuration profile that
// installs the local CA's root on an iPhone or iPad. It is not behind
// pairing: the root is public, and a device must trust it before it can
// open the pair page without a warning.
func (h *Home) serveProfile(w http.ResponseWriter, r *http.Request) {
	root, err := RootCert(h.StorageRoot)
	if err != nil {
		refuse(w, r, report.ProxyNotFound, http.StatusNotFound)
		return
	}
	name := root.Subject.CommonName
	if name == "" {
		name = "Nearhost Local CA"
	}
	body := mobileconfig(root.Raw, name)
	hdr := w.Header()
	hdr.Set("Content-Type", "application/x-apple-aspen-config")
	hdr.Set("Content-Disposition", `attachment; filename="nearhost.mobileconfig"`)
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}
