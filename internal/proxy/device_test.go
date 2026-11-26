package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"github.com/nearhost/nearhost/internal/devices"
)

// fakeDevices is a DeviceAuth held in memory: devices by token, pair codes
// that work once, auth tokens that work once on their host, and a switch
// for Allow.
type fakeDevices struct {
	mu      sync.Mutex
	devices map[string]devices.Device
	codes   map[string]bool
	auths   map[string]string // token -> id|host
	refused bool              // Allow answers false
	fails   []string
	pairs   []string // the names Pair was given
	n       int
}

func newFakeDevices() *fakeDevices {
	return &fakeDevices{
		devices: map[string]devices.Device{"good-token": {Name: "iPad", ID: "dev1"}},
		codes:   map[string]bool{"ABCD2345": true},
		auths:   map[string]string{},
	}
}

func (f *fakeDevices) Device(token string) (devices.Device, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[token]
	return d, ok
}

func (f *fakeDevices) Pair(code, name, addr string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.codes[code] {
		f.fails = append(f.fails, addr)
		return "", devices.ErrCodeInvalid
	}
	delete(f.codes, code)
	f.n++
	tok := "token-" + string(rune('a'+f.n))
	f.devices[tok] = devices.Device{Name: name, ID: "new" + tok}
	f.pairs = append(f.pairs, name)
	return tok, nil
}

func (f *fakeDevices) NewAuthToken(deviceID, host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	tok := "auth-" + string(rune('a'+f.n))
	f.auths[tok] = deviceID + "|" + strings.ToLower(host)
	return tok
}

func (f *fakeDevices) RedeemAuth(token, host string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.auths[token]
	id, h, _ := strings.Cut(v, "|")
	if !ok || h != strings.ToLower(host) {
		return "", false
	}
	delete(f.auths, token)
	return id, true
}

func (f *fakeDevices) SiteCookie(deviceID, host string) string {
	return deviceID + "." + strings.ToLower(host)
}

func (f *fakeDevices) CheckSite(value, host string) (string, bool) {
	id, h, ok := strings.Cut(value, ".")
	if !ok || h != strings.ToLower(host) {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.ID == id {
			return id, true
		}
	}
	return "", false
}

func (f *fakeDevices) Allow(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.refused
}

func (f *fakeDevices) Fail(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, addr)
}

// setFakeDevices sets a fake DeviceAuth for the test, and makes no address
// this machine's, so the test's own addresses don't count as local.
func setFakeDevices(t *testing.T) *fakeDevices {
	t.Helper()
	f := newFakeDevices()
	SetDevices(f)
	stubSelfAddrs(t, func(netip.Addr) bool { return false })
	t.Cleanup(func() { SetDevices(nil) })
	return f
}

func stubSelfAddrs(t *testing.T, f func(netip.Addr) bool) {
	t.Helper()
	old := selfAddrs
	selfAddrs = f
	t.Cleanup(func() { selfAddrs = old })
}

const testPage = "https://nearhost.lab.example.dev:7443"

// nextRecorder is a next handler that records the requests it is sent.
type nextRecorder struct {
	calls   int
	cookies []string
}

func (n *nextRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	n.calls++
	n.cookies = append(n.cookies, strings.Join(r.Header.Values("Cookie"), "|"))
	w.WriteHeader(http.StatusTeapot)
	return nil
}

// guardRequest is a GET of target at app.lab.example.dev from remote.
func guardRequest(target, remote string) *http.Request {
	r := httptest.NewRequest("GET", "https://app.lab.example.dev"+target, nil)
	r.RemoteAddr = remote
	return r
}

// serveGuard sends r through g, provisioned, to a recording next.
func serveGuard(t *testing.T, g *DeviceGuard, r *http.Request) (*httptest.ResponseRecorder, *nextRecorder) {
	t.Helper()
	if err := g.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	next := new(nextRecorder)
	w := httptest.NewRecorder()
	if err := g.ServeHTTP(w, r, next); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w, next
}

func TestDeviceGuardLetsLoopbackThrough(t *testing.T) {
	setFakeDevices(t)
	for _, remote := range []string{"127.0.0.1:1", "[::1]:1", "[::ffff:127.0.0.1]:1"} {
		_, next := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/", remote))
		if next.calls != 1 {
			t.Errorf("%s: next called %d times, want 1", remote, next.calls)
		}
	}
}

func TestDeviceGuardTreatsSelfAddressAsLocal(t *testing.T) {
	setFakeDevices(t)
	self := netip.MustParseAddr("192.168.77.5")
	stubSelfAddrs(t, func(a netip.Addr) bool { return a == self })
	for _, remote := range []string{"192.168.77.5:1", "[::ffff:192.168.77.5]:1"} {
		_, next := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/", remote))
		if next.calls != 1 {
			t.Errorf("%s: next called %d times, want 1", remote, next.calls)
		}
	}
	w, next := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/", "192.168.77.6:1"))
	if next.calls != 0 || w.Code != http.StatusFound {
		t.Errorf("another address: next %d, status %d", next.calls, w.Code)
	}
}

func TestDeviceGuardLetsDockerClientsThrough(t *testing.T) {
	setFakeDevices(t)
	g := &DeviceGuard{Page: testPage, Local: []string{"127.0.0.0/8", "::1", "172.17.0.0/16", "::ffff:172.17.0.0/112", "10.9.9.9"}}
	_, next := serveGuard(t, g, guardRequest("/", "172.17.0.2:1"))
	if next.calls != 1 {
		t.Errorf("next called %d times, want 1", next.calls)
	}
	_, next = serveGuard(t, g, guardRequest("/", "10.9.9.9:1"))
	if next.calls != 1 {
		t.Errorf("single address: next called %d times, want 1", next.calls)
	}
	w, next := serveGuard(t, g, guardRequest("/", "172.18.0.2:1"))
	if next.calls != 0 || w.Code != http.StatusFound {
		t.Errorf("outside Local: next %d, status %d", next.calls, w.Code)
	}
}

func TestDeviceGuardRedirectsUnpaired(t *testing.T) {
	setFakeDevices(t)
	w, next := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/x?y=1", "100.79.1.1:1"))
	if next.calls != 0 {
		t.Fatal("an unpaired client reached the site")
	}
	want := "https://nearhost.lab.example.dev:7443/auth?to=https%3A%2F%2Fapp.lab.example.dev%2Fx%3Fy%3D1"
	if w.Code != http.StatusFound || w.Header().Get("Location") != want {
		t.Errorf("got %d %q, want 302 %q", w.Code, w.Header().Get("Location"), want)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

// The sign-in redirects send the origin as referrer, so the app's framed
// load after them still carries the page's origin for the frame rule.
func TestDeviceGuardRedirectSendsOrigin(t *testing.T) {
	setFakeDevices(t)
	w, _ := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/x", "100.79.1.1:1"))
	if w.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", w.Code)
	}
	if rp := w.Header().Get("Referrer-Policy"); rp != "strict-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin", rp)
	}
}

func TestDeviceGuardRedeemSendsOrigin(t *testing.T) {
	f := setFakeDevices(t)
	tok := f.NewAuthToken("dev1", "app.lab.example.dev")
	w, _ := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/.nearhost/auth?t="+tok+"&to=/a", "100.79.1.1:1"))
	if w.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", w.Code)
	}
	if rp := w.Header().Get("Referrer-Policy"); rp != "strict-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin", rp)
	}
}

// With no page to pair at, or no devices, an unpaired client gets 503 and
// never the site.
func TestDeviceGuardWithoutPageRefuses(t *testing.T) {
	setFakeDevices(t)
	for name, page := range map[string]string{"no page": "", "no web port": "https://nearhost.lab.example.dev"} {
		w, next := serveGuard(t, &DeviceGuard{Page: page}, guardRequest("/", "100.79.1.1:1"))
		if next.calls != 0 || w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: next %d, status %d, want 503", name, next.calls, w.Code)
		}
	}
	SetDevices(nil)
	w, next := serveGuard(t, &DeviceGuard{Page: testPage}, guardRequest("/", "100.79.1.1:1"))
	if next.calls != 0 || w.Code != http.StatusServiceUnavailable {
		t.Errorf("no devices: next %d, status %d, want 503", next.calls, w.Code)
	}
	// Loopback still passes.
	_, next = serveGuard(t, &DeviceGuard{}, guardRequest("/", "127.0.0.1:1"))
	if next.calls != 1 {
		t.Error("loopback was refused with no page")
	}
}

func TestDeviceGuardRefusesWebSocketWithout(t *testing.T) {
	setFakeDevices(t)
	r := guardRequest("/ws", "100.79.1.1:1")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w, next := serveGuard(t, &DeviceGuard{Page: testPage}, r)
	if next.calls != 0 || w.Code != http.StatusUnauthorized {
		t.Errorf("next %d, status %d, want 401", next.calls, w.Code)
	}
}

func TestDeviceGuardStripsSiteCookie(t *testing.T) {
	setFakeDevices(t)
	r := guardRequest("/", "100.79.1.1:1")
	r.Header.Set("Cookie", "__Host-nearhost-site=dev1.app.lab.example.dev; session=1")
	_, next := serveGuard(t, &DeviceGuard{Page: testPage}, r)
	if next.calls != 1 {
		t.Fatalf("a paired device's request did not reach the site")
	}
	if next.cookies[0] != "session=1" {
		t.Errorf("site saw Cookie %q, want session=1", next.cookies[0])
	}
	// Spaces or a tab before the = still name the site cookie to net/http,
	// so they must be stripped too.
	for _, c := range []string{
		"__Host-nearhost-site =dev1.app.lab.example.dev; session=1",
		"__Host-nearhost-site\t=dev1.app.lab.example.dev; session=1",
		"session=1;  __Host-nearhost-site =dev1.app.lab.example.dev",
	} {
		r := guardRequest("/", "100.79.1.1:1")
		r.Header.Set("Cookie", c)
		if len(r.CookiesNamed("__Host-nearhost-site")) != 1 {
			t.Fatalf("%q: net/http does not read the site cookie", c)
		}
		_, next := serveGuard(t, &DeviceGuard{Page: testPage}, r)
		if next.calls != 1 || next.cookies[0] != "session=1" {
			t.Errorf("%q: next %d, site saw Cookie %q", c, next.calls, next.cookies)
		}
	}
	// A cookie for another host, or of a device no longer paired, does
	// not pass.
	for _, v := range []string{"dev1.other.lab.example.dev", "gone.app.lab.example.dev"} {
		r := guardRequest("/", "100.79.1.1:1")
		r.AddCookie(&http.Cookie{Name: "__Host-nearhost-site", Value: v})
		w, next := serveGuard(t, &DeviceGuard{Page: testPage}, r)
		if next.calls != 0 || w.Code != http.StatusFound {
			t.Errorf("%s: next %d, status %d", v, next.calls, w.Code)
		}
	}
}

func TestDeviceGuardRedeemsOnce(t *testing.T) {
	f := setFakeDevices(t)
	tok := f.NewAuthToken("dev1", "app.lab.example.dev")
	g := &DeviceGuard{Page: testPage}
	w, next := serveGuard(t, g, guardRequest("/.nearhost/auth?t="+tok+"&to=/a", "100.79.1.1:1"))
	if next.calls != 0 {
		t.Error("the auth path reached the site")
	}
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/a" {
		t.Fatalf("first: %d %q, want 302 /a", w.Code, w.Header().Get("Location"))
	}
	sc := w.Header().Get("Set-Cookie")
	if !strings.HasPrefix(sc, "__Host-nearhost-site=dev1.app.lab.example.dev; Path=/; Secure; HttpOnly; SameSite=Lax") || !strings.Contains(sc, "Max-Age=34560000") {
		t.Errorf("Set-Cookie = %q", sc)
	}
	w, _ = serveGuard(t, g, guardRequest("/.nearhost/auth?t="+tok+"&to=/a", "100.79.1.1:1"))
	if w.Code != http.StatusForbidden {
		t.Errorf("second: %d, want 403", w.Code)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a used token set a cookie")
	}
	if len(f.fails) != 1 || f.fails[0] != "100.79.1.1" {
		t.Errorf("fails = %v, want one for 100.79.1.1", f.fails)
	}
	for _, to := range []string{"//evil.example/", "/\\evil.example/", "https://evil.example/", "a"} {
		tok := f.NewAuthToken("dev1", "app.lab.example.dev")
		r := guardRequest("/.nearhost/auth?to=x", "100.79.1.1:1")
		q := r.URL.Query()
		q.Set("t", tok)
		q.Set("to", to)
		r.URL.RawQuery = q.Encode()
		w, _ := serveGuard(t, g, r)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
			t.Errorf("to=%q: %d %q, want 302 /", to, w.Code, w.Header().Get("Location"))
		}
	}
	// Refused once Allow says no, even for a good token.
	f.refused = true
	tok = f.NewAuthToken("dev1", "app.lab.example.dev")
	w, _ = serveGuard(t, g, guardRequest("/.nearhost/auth?t="+tok+"&to=/a", "100.79.1.1:1"))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("refused client: %d, want 429", w.Code)
	}
}

func TestClientAddr(t *testing.T) {
	for remote, want := range map[string]string{
		"100.79.1.1:1":          "100.79.1.1",
		"[::ffff:10.0.0.1]:443": "10.0.0.1",
		"[fe80::1%eth0]:1":      "fe80::1",
		"[fd00::1]:1":           "fd00::1",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		got, ok := clientAddr(r)
		if !ok || got.String() != want {
			t.Errorf("clientAddr(%q) = %v, %v; want %s", remote, got, ok, want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "garbage"
	if _, ok := clientAddr(r); ok {
		t.Error("garbage parsed")
	}
}

// The page's device cookie never reaches a site either, should a site be
// served at the page's host (a pattern under the domain can match it).
func TestDeviceGuardStripsDeviceCookie(t *testing.T) {
	setFakeDevices(t)
	for _, client := range []string{"100.79.1.1:1", "127.0.0.1:1"} {
		r := guardRequest("/", client)
		r.Header.Set("Cookie", "__Host-nearhost-site=dev1.app.lab.example.dev; __Host-nearhost-device=secret; session=1")
		_, next := serveGuard(t, &DeviceGuard{Page: testPage}, r)
		if next.calls != 1 || next.cookies[0] != "session=1" {
			t.Errorf("%s: next %d, site saw Cookie %q", client, next.calls, next.cookies)
		}
	}
}
