package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/net/html"

	"github.com/nearhost/nearhost/internal/report"
)

// webApp is the web app SetApp stored: its files, its index.html, the
// policy the index is sent with and its documents.css, worked out once when
// it is set.
type webApp struct {
	fsys  fs.FS
	index []byte
	csp   string
	// documentsCSS is the stylesheet documents are themed with (see
	// documentTheme), empty when the app has none.
	documentsCSS []byte
}

// appDocumentsCSS is the file at the root of the app that styles themed
// documents.
const appDocumentsCSS = "documents.css"

// app holds what SetApp stored last, nil while nothing is set. It is
// package-level, as the console is, because Caddy builds a new Home on
// every config load while the app outlives them all.
var app atomic.Pointer[webApp]

// SetApp sets the web app the page hosts serve, the build of the pwa
// module, or clears it with nil. An app without an index.html is no app.
// While one is set, Home serves it, to any browser at the page hosts, for
// every GET and HEAD that the shell's /?k= redirect, documents, pairing and
// the API leave, other than the server's own paths (see appServerPath): a
// path naming one of its files gets that file, and any other its
// index.html, so the app's own routes load from a link or a reload.
func SetApp(fsys fs.FS) {
	if fsys == nil {
		app.Store(nil)
		return
	}
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		app.Store(nil)
		return
	}
	css, _ := fs.ReadFile(fsys, appDocumentsCSS)
	app.Store(&webApp{fsys: fsys, index: index, csp: appCSP(index), documentsCSS: css})
}

// currentApp returns the app set now, nil while there is none.
func currentApp() *webApp {
	return app.Load()
}

// file returns the app's file at p, a request's path, and whether it has
// one there. A directory is no file.
func (a *webApp) file(p string) ([]byte, bool) {
	name := strings.TrimPrefix(p, "/")
	if name == "" || !fs.ValidPath(name) {
		return nil, false
	}
	if name == "index.html" {
		return a.index, true
	}
	data, err := fs.ReadFile(a.fsys, name)
	return data, err == nil
}

// appCSP is the policy the app runs under, allowing each inline script in
// index by its hash and every other script only from the page's own
// origin. The browser enforces it alongside any the index carries in a
// meta tag; frame-ancestors works only here, in the header.
func appCSP(index []byte) string {
	var hashes strings.Builder
	for _, s := range inlineScripts(index) {
		sum := sha256.Sum256(s)
		hashes.WriteString(" 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'")
	}
	return "default-src 'none'; script-src 'self'" + hashes.String() +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// inlineScripts returns the text of each script element in doc without a
// src, as the browser hashes it: raw, with its line endings made \n.
func inlineScripts(doc []byte) [][]byte {
	var out [][]byte
	z := html.NewTokenizer(bytes.NewReader(doc))
	var text []byte
	inScript, inline := false, false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken:
			name, more := z.TagName()
			if string(name) != "script" {
				continue
			}
			inScript, inline, text = true, true, nil
			for more {
				var key []byte
				key, _, more = z.TagAttr()
				if string(key) == "src" {
					inline = false
				}
			}
		case html.TextToken:
			if inScript {
				text = append(text, z.Text()...)
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "script" && inScript {
				if inline {
					out = append(out, text)
				}
				inScript = false
			}
		}
	}
}

// appServerPath reports whether p, a request's escaped path, is the
// server's own and never the app's: the API, pairing, the device check,
// documents and the well-known and Nearhost paths. Whatever answers there,
// it is never the app's index.html.
func appServerPath(p string) bool {
	for _, prefix := range []string{"/api", "/pair"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	switch p {
	case "/pair.js", "/auth", "/profile.mobileconfig":
		return true
	}
	for _, prefix := range []string{"/d/", "/.well-known/", "/.nearhost/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// immutablePrefix is where the build puts the files named for their
// content, which never change at their path.
const immutablePrefix = "/_app/immutable/"

// appTypes are the content types of the build's files, which the system's
// table may not know.
var appTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
}

// appType is the content type of the app's file at p.
func appType(p string) string {
	ext := strings.ToLower(path.Ext(p))
	if t, ok := appTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// serveApp answers r from a, the app set now, and reports whether it
// answered. It leaves the server's own paths, and /?k=, to the rest of
// ServeHTTP. A path naming the app's file gets the file; one under /_app/
// that names none is not found, as a missing build file must never be
// answered with a page; any other gets index.html.
func (h *Home) serveApp(a *webApp, w http.ResponseWriter, r *http.Request) bool {
	if appServerPath(r.URL.EscapedPath()) {
		return false
	}
	if r.URL.Path == "/" && r.URL.Query().Has(pageKeyParam) {
		return false
	}
	hdr := w.Header()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hdr.Set("Allow", "GET, HEAD")
		refuse(w, r, report.ProxyMethodNotAllowed, http.StatusMethodNotAllowed)
		return true
	}
	body, ok := a.file(r.URL.Path)
	name := r.URL.Path
	switch {
	case ok:
	case strings.HasPrefix(r.URL.Path, "/_app/"):
		refuse(w, r, report.ProxyNotFound, http.StatusNotFound)
		return true
	default:
		body, name = a.index, "/index.html"
	}
	hdr.Set("Content-Type", appType(name))
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	// Every answer carries the policy, so a page of the app loaded any
	// way runs under it.
	hdr.Set("Content-Security-Policy", a.csp)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "same-origin")
	if strings.HasPrefix(name, immutablePrefix) {
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// The index names the build's files, which change with the core.
		hdr.Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
	return true
}
