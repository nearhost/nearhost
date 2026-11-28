package proxy

import (
	"bytes"
	"encoding/json"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

var shellPaths = []struct{ path, contentType string }{
	{"/manifest.webmanifest", "application/manifest+json"},
	{"/sw.js", "text/javascript; charset=utf-8"},
	{"/icon-180.png", "image/png"},
	{"/icon-192.png", "image/png"},
	{"/icon-512.png", "image/png"},
}

// localRequest is a loopback request for host.
func localRequest(method, host, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = host + ":7443"
	r.RemoteAddr = "127.0.0.1:1"
	return r
}

func TestShellAtEveryPageHost(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	h := netHome()
	reqs := map[string]func(p string) *http.Request{
		"nearhost.localhost": func(p string) *http.Request { return localRequest("GET", "nearhost.localhost", p) },
		"localhost":          func(p string) *http.Request { return localRequest("GET", "localhost", p) },
		"network":            func(p string) *http.Request { return netRequest("GET", p, nil) },
	}
	for name, mk := range reqs {
		for _, sp := range shellPaths {
			w := serveNet(t, h, mk(sp.path))
			if w.Code != http.StatusOK {
				t.Errorf("%s %s: %d", name, sp.path, w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != sp.contentType {
				t.Errorf("%s %s: Content-Type %q, want %q", name, sp.path, got, sp.contentType)
			}
			if c := w.Header().Values("Set-Cookie"); len(c) > 0 {
				t.Errorf("%s %s sets cookies: %v", name, sp.path, c)
			}
			if w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s %s: headers %v", name, sp.path, w.Header())
			}
		}
	}
}

func TestShellNeedsNoCookie(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	for _, sp := range shellPaths {
		w := serveNet(t, netHome(), netRequest("GET", sp.path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s unpaired: %d", sp.path, w.Code)
		}
		if len(w.Header().Values("Set-Cookie")) > 0 {
			t.Errorf("%s sets a cookie", sp.path)
		}
		for _, v := range w.Header().Values("Vary") {
			if strings.Contains(strings.ToLower(v), "cookie") {
				t.Errorf("%s varies on %q", sp.path, v)
			}
		}
	}
}

func TestShellMethods(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	for _, sp := range shellPaths {
		w := serveNet(t, netHome(), netRequest("POST", sp.path, nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s: %d Allow %q", sp.path, w.Code, w.Header().Get("Allow"))
		}
		w = serveNet(t, netHome(), netRequest("HEAD", sp.path, nil))
		if w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d with %d bytes", sp.path, w.Code, w.Body.Len())
		}
	}
}

func TestShellNotAtOtherHosts(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	r := netRequest("GET", "/sw.js", nil)
	r.Host = "web.test"
	w := serveNet(t, netHome(), r)
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "nearhost-shell") {
		t.Error("the worker is served at another host")
	}
}

func TestManifest(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	get := func(machine string) map[string]any {
		h := netHome()
		h.Machine = machine
		w := serveNet(t, h, netRequest("GET", "/manifest.webmanifest", nil))
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("manifest: %v\n%s", err, w.Body)
		}
		return m
	}
	m := get("leto")
	if m["name"] != "Nearhost on leto" {
		t.Errorf("name = %v", m["name"])
	}
	for k, want := range map[string]string{
		"short_name": "Nearhost", "display": "standalone", "start_url": "/", "scope": "/",
		"background_color": "#0F1210", "theme_color": "#0F1210",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %s", k, m[k], want)
		}
	}
	if get("")["name"] != "Nearhost" {
		t.Errorf("name without a machine = %v", get("")["name"])
	}
	sizes := map[string]bool{}
	for _, i := range m["icons"].([]any) {
		im := i.(map[string]any)
		if im["type"] != "image/png" {
			t.Errorf("icon type %v", im["type"])
		}
		sizes[im["sizes"].(string)] = true
	}
	if !sizes["192x192"] || !sizes["512x512"] || len(sizes) != 2 {
		t.Errorf("icons = %v", sizes)
	}
}

func TestIcons(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	for _, size := range []int{180, 192, 512} {
		w := serveNet(t, netHome(), netRequest("GET", "/icon-"+strconv.Itoa(size)+".png", nil))
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatalf("%d: %v", size, err)
		}
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Errorf("%d: bounds %v", size, b)
		}
		at := func(fx, fy float64) color.RGBA {
			r, g, b, _ := img.At(int(fx*float64(size)), int(fy*float64(size))).RGBA()
			return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xFF}
		}
		for name, c := range map[string]struct {
			x, y float64
			want color.RGBA
		}{
			"ground": {2.0 / 64, 2.0 / 64, color.RGBA{0x0F, 0x12, 0x10, 0xFF}},
			"dot":    {32.0 / 64, 46.0 / 64, color.RGBA{0xEC, 0xEF, 0xE8, 0xFF}},
			"arch":   {18.0 / 64, 44.0 / 64, color.RGBA{0xD6, 0xF2, 0x6B, 0xFF}},
		} {
			if got := at(c.x, c.y); got != c.want {
				t.Errorf("%d %s pixel = %v, want %v", size, name, got, c.want)
			}
		}
	}
}

// The page's own worker is retired: /sw.js is a worker that takes over at
// once from the old one a browser still has, deletes the old one's caches
// and answers nothing. It never unregisters, as the registration at / is
// the web app's too, and the offline page it fell back to is gone.
func TestRetiredWorker(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	body := serveNet(t, netHome(), netRequest("GET", "/sw.js", nil)).Body.String()
	for _, want := range []string{"skipWaiting()", "'activate'", "'nearhost-shell-'", "caches.delete"} {
		if !strings.Contains(body, want) {
			t.Errorf("the retired worker lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"'fetch'", "respondWith", "unregister", "/offline.html"} {
		if strings.Contains(body, bad) {
			t.Errorf("the retired worker has %q:\n%s", bad, body)
		}
	}
	if w := serveNet(t, netHome(), netRequest("GET", "/offline.html", nil)); w.Code != http.StatusNotFound {
		t.Errorf("GET /offline.html: %d, want 404", w.Code)
	}
}

func TestHeadTags(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	home := serveNet(t, netHome(), localRequest("GET", "nearhost.localhost", "/")).Body.String()
	pair := serveNet(t, netHome(), netRequest("GET", "/pair", nil)).Body.String()
	for name, body := range map[string]string{"home": home, "pair": pair} {
		for _, want := range []string{
			`rel="manifest"`, `rel="apple-touch-icon"`, "viewport-fit=cover",
			`name="theme-color" content="#0F1210"`, `name="apple-mobile-web-app-title" content="Nearhost"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page lacks %q", name, want)
			}
		}
	}
	// Nothing registers the retired worker again, which would take the
	// registration from the web app's.
	if strings.Contains(pair, "serviceWorker") || strings.Contains(string(pairJS), "serviceWorker") {
		t.Error("the pair page registers a worker")
	}
}

func TestCSPAllowsManifest(t *testing.T) {
	for name, csp := range map[string]string{"home": homeCSP, "console": consoleCSP} {
		for _, want := range []string{"manifest-src 'self'", "worker-src 'self'", "frame-ancestors 'none'", "img-src 'self' data:"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s CSP lacks %q: %s", name, want, csp)
			}
		}
	}
}
