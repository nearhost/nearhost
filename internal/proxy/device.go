package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/nearhost/nearhost/internal/devices"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/report/htmlview"
)

// deviceModule is the Caddy handler module name registered below.
const deviceModule = "nearhost_device"

// deviceCookie is the cookie that names a paired device at the page. The
// __Host- prefix keeps it to the page's own host.
const deviceCookie = "__Host-nearhost-device"

// siteCookie is the cookie that lets a paired device's browser into one
// site. It is host-only, as its __Host- prefix demands, and the guard
// strips it before the request goes on to the app.
const siteCookie = "__Host-nearhost-site"

// siteAuthPath is the path, reserved on every site of a networked core,
// that trades a one-time token from the page for a site cookie.
const siteAuthPath = "/.nearhost/auth"

// cookieMaxAge is the Max-Age of the device and site cookies: 400 days,
// the most browsers keep a cookie.
const cookieMaxAge = 400 * 24 * 60 * 60

// pairingCookie is the Set-Cookie value of the cookie name with value, as
// the device and site cookies are both set.
func pairingCookie(name, value string) string {
	return name + "=" + value + "; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=" + strconv.Itoa(cookieMaxAge)
}

// DeviceAuth is what the page and the device guard need of the paired
// devices: devices.Store, or a fake in tests.
type DeviceAuth interface {
	Device(token string) (devices.Device, bool)
	Pair(code, name, addr string) (string, error)
	NewAuthToken(deviceID, host string) string
	RedeemAuth(token, host string) (string, bool)
	SiteCookie(deviceID, host string) string
	CheckSite(value, host string) (string, bool)
	Allow(addr string) bool
	// Fail counts a failed attempt from addr, for a refused RedeemAuth;
	// Pair counts its own.
	Fail(addr string)
}

// deviceAuthHandle wraps what SetDevices stored, as atomic.Pointer needs a
// concrete type.
type deviceAuthHandle struct {
	d DeviceAuth
}

// deviceAuth holds what SetDevices stored last, nil while nothing is set.
// It is package-level, as the console is, because Caddy builds new
// handlers on every config load while the devices outlive them all.
var deviceAuth atomic.Pointer[deviceAuthHandle]

// SetDevices sets the paired devices the page pairs and the device guard
// checks, or clears them with nil: then the page has no pairing and the
// guard lets only local clients through.
func SetDevices(d DeviceAuth) {
	if d == nil {
		deviceAuth.Store(nil)
		return
	}
	deviceAuth.Store(&deviceAuthHandle{d: d})
}

// currentDevices returns the devices set now, nil while there are none.
func currentDevices() DeviceAuth {
	h := deviceAuth.Load()
	if h == nil {
		return nil
	}
	return h.d
}

// selfAddrs reports whether an address is one of this machine's: a request
// from leto to its own name arrives from leto's LAN address, and is as
// local as loopback. Tests replace it.
var selfAddrs = cachedInterfaceAddrs(10 * time.Second)

// cachedInterfaceAddrs returns a func that reports whether an address is
// one of this machine's interface addresses, IPv4-mapped ones unmapped, as
// net.InterfaceAddrs listed them at most ttl ago. A failed listing counts
// as none.
func cachedInterfaceAddrs(ttl time.Duration) func(netip.Addr) bool {
	var (
		mu    sync.Mutex
		addrs map[netip.Addr]bool
		at    time.Time
	)
	return func(a netip.Addr) bool {
		mu.Lock()
		defer mu.Unlock()
		if addrs == nil || time.Since(at) > ttl {
			addrs, at = map[netip.Addr]bool{}, time.Now()
			list, _ := net.InterfaceAddrs()
			for _, n := range list {
				var ip net.IP
				switch v := n.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if x, ok := netip.AddrFromSlice(ip); ok {
					addrs[x.Unmap()] = true
				}
			}
		}
		return addrs[a.Unmap().WithZone("")]
	}
}

// clientAddr is r's client address as Caddy saw the connection, its
// RemoteAddr, unmapped from IPv4-in-IPv6 and without a zone.
func clientAddr(r *http.Request) (netip.Addr, bool) {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

// requestHost is r's host without its port, in lower case.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

func init() {
	caddy.RegisterModule(DeviceGuard{})
}

// DeviceGuard is a Caddy HTTP handler on every site route of a networked
// core, right after the tag: a client in Local passes as it always has, and
// any other must come from a paired device, or is sent to Page to pair.
type DeviceGuard struct {
	// Page is the page's network origin, https://nearhost.<domain>:<port>.
	Page string `json:"page,omitempty"`
	// Local are the client ranges that need no device: loopback and the
	// site's own extra clients.
	Local []string `json:"local,omitempty"`

	// local is Local parsed, in Provision, with IPv4-mapped ranges as
	// IPv4.
	local []netip.Prefix
	// pageOK is whether Page is an origin with a port, so there is a page
	// to send an unpaired device to.
	pageOK bool
}

// CaddyModule implements caddy.Module.
func (DeviceGuard) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + deviceModule,
		New: func() caddy.Module { return new(DeviceGuard) },
	}
}

// Provision implements caddy.Provisioner: it parses Local, and checks Page.
func (g *DeviceGuard) Provision(caddy.Context) error {
	g.local = g.local[:0]
	for _, s := range g.Local {
		p, err := parseRange(s)
		if err != nil {
			return fmt.Errorf("%s: local range %q: %w", deviceModule, s, err)
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		g.local = append(g.local, p.Masked())
	}
	u, err := url.Parse(g.Page)
	g.pageOK = err == nil && u.Scheme == "https" && u.Hostname() != "" && u.Port() != ""
	return nil
}

// parseRange parses a client range as Caddy's remote_ip takes it: a CIDR,
// or a single address.
func parseRange(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// isLocal reports whether a, unmapped, needs no device: loopback, one of
// this machine's addresses, or in Local.
func (g *DeviceGuard) isLocal(a netip.Addr) bool {
	if a.IsLoopback() || selfAddrs(a) {
		return true
	}
	for _, p := range g.local {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ServeHTTP implements caddyhttp.MiddlewareHandler. A local client passes.
// Otherwise /.nearhost/auth trades a token from the page for the site
// cookie, a valid site cookie passes (stripped), a WebSocket upgrade is
// refused with 401, and anything else is sent to the page's /auth, which
// sends a paired device back with a token, or an unpaired one to pair.
// With no page or no devices, every client but a local one is refused.
func (g *DeviceGuard) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	addr, ok := clientAddr(r)
	if ok && g.isLocal(addr) {
		stripSiteCookie(r)
		return next.ServeHTTP(w, r)
	}
	d := currentDevices()
	hdr := w.Header()
	hdr.Set("Cache-Control", "no-store")
	if d == nil || !g.pageOK || !ok {
		htmlview.Write(w, r, report.Errorf(report.PairUnavailable, "This site is open to paired devices only, and they can't pair now").
			Because("Nearhost's page, where devices pair, is not being served. Open the site on the machine Nearhost runs on."), http.StatusServiceUnavailable, "Nearhost")
		return nil
	}
	host := requestHost(r)
	client := addr.String()
	if r.URL.Path == siteAuthPath {
		g.redeem(w, r, d, host, client)
		return nil
	}
	for _, c := range r.CookiesNamed(siteCookie) {
		if _, ok := d.CheckSite(c.Value, host); ok {
			hdr.Del("Cache-Control")
			stripSiteCookie(r)
			return next.ServeHTTP(w, r)
		}
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		htmlview.Write(w, r, report.Errorf(report.PairNeeded, "This site is open to paired devices only").
			Because("Open the site in the browser first, so Nearhost can sign this device in."), http.StatusUnauthorized, "Nearhost")
		return nil
	}
	full := "https://" + r.Host + r.URL.RequestURI()
	// Only the origin goes on as referrer, so a framed sign-in still ends
	// with the page's origin for the frame rule; the full address does not.
	hdr.Set("Referrer-Policy", "strict-origin")
	hdr.Set("Location", g.Page+"/auth?to="+url.QueryEscape(full))
	w.WriteHeader(http.StatusFound)
	return nil
}

// redeem answers /.nearhost/auth?t=<token>&to=<path>: a token the page
// made for this host gets the site cookie and is sent on to its path; any
// other is refused, and counted against the client.
func (g *DeviceGuard) redeem(w http.ResponseWriter, r *http.Request, d DeviceAuth, host, client string) {
	hdr := w.Header()
	hdr.Set("Referrer-Policy", "no-referrer")
	if !d.Allow(client) {
		tooManyTries(w, r)
		return
	}
	q := r.URL.Query()
	id, ok := d.RedeemAuth(q.Get("t"), host)
	if !ok {
		d.Fail(client)
		htmlview.Write(w, r, report.Errorf(report.PairLinkUsed, "That sign-in link has been used or has expired").
			Because("Open the site again on this device to get a new one."), http.StatusForbidden, "Nearhost")
		return
	}
	hdr.Add("Set-Cookie", pairingCookie(siteCookie, d.SiteCookie(id, host)))
	// The redirect overrides the frame's referrer policy for the load it
	// leads to: the page's origin must reach the app, the token never.
	hdr.Set("Referrer-Policy", "strict-origin")
	hdr.Set("Location", localPath(q.Get("to")))
	w.WriteHeader(http.StatusFound)
}

// tooManyTries answers a client Allow refuses.
func tooManyTries(w http.ResponseWriter, r *http.Request) {
	htmlview.Write(w, r, report.Errorf(report.PairTooManyTries, "Too many wrong tries from this device").
		Because("Wait ten minutes, then try again."), http.StatusTooManyRequests, "Nearhost")
}

// localPath is to when it is a path on this host: it starts with a single
// /, and holds no backslash or control character, which a browser might
// read as another host or a header might split on. Anything else is /.
func localPath(to string) string {
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.ContainsRune(to, '\\') {
		return "/"
	}
	for _, c := range to {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	return to
}

// stripSiteCookie removes the site cookie, and the page's device cookie
// (sent along should a site be served at the page's host), from r's Cookie
// headers, so the app never sees either, and removes a header left empty.
func stripSiteCookie(r *http.Request) {
	lines := r.Header.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		var parts []string
		for _, part := range strings.Split(line, ";") {
			// Trimmed as net/http trims a cookie's name, so whatever
			// CookiesNamed read as the site cookie is stripped.
			name, _, _ := strings.Cut(part, "=")
			if n := textproto.TrimString(name); strings.TrimSpace(part) == "" || n == siteCookie || n == deviceCookie {
				continue
			}
			parts = append(parts, strings.TrimSpace(part))
		}
		if len(parts) > 0 {
			kept = append(kept, strings.Join(parts, "; "))
		}
	}
	r.Header.Del("Cookie")
	for _, k := range kept {
		r.Header.Add("Cookie", k)
	}
}

var (
	_ caddy.Provisioner           = (*DeviceGuard)(nil)
	_ caddyhttp.MiddlewareHandler = (*DeviceGuard)(nil)
)
