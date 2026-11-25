package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/caddyserver/caddy/v2"
	"howett.net/plist"
)

const netPageHost = "nearhost.lab.example.dev"

// netHome is a networked home page as the config builds it.
func netHome() *Home {
	return &Home{Version: "1.2.3", WebPort: 7443, NetworkHost: netPageHost}
}

// serveNet provisions h and sends it r.
func serveNet(t *testing.T, h *Home, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(func() { setNetworkHost("") })
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	w := httptest.NewRecorder()
	if err := h.ServeHTTP(w, r, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return w
}

// netRequest is a request at the network page host from a LAN client.
func netRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = netPageHost + ":7443"
	r.RemoteAddr = "100.79.1.1:1"
	return r
}

func withDevice(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost-device", Value: token})
	return r
}

// pairPost is a same-origin POST of form to /pair.
func pairPost(form url.Values) *http.Request {
	r := netRequest("POST", "/pair", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://"+netPageHost+":7443")
	return r
}

func TestAuthRefusesForeignTo(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	for _, to := range []string{"https://evil.example/", "https://xlab.example.dev/", "http://app.lab.example.dev/", "//app.lab.example.dev/", "https://user@evil.example\\@app.lab.example.dev/", ""} {
		r := withDevice(netRequest("GET", "/auth?"+url.Values{"to": {to}}.Encode(), nil), "good-token")
		w := serveNet(t, netHome(), r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("to=%q: %d %q, want 400", to, w.Code, w.Header().Get("Location"))
		}
	}
	for _, c := range []struct{ to, prefix, rest string }{
		{"https://app.lab.example.dev/", "https://app.lab.example.dev/.nearhost/auth?t=", "&to=%2F"},
		{"https://App.Lab.Example.Dev:8443/x?y=1", "https://App.Lab.Example.Dev:8443/.nearhost/auth?t=", "&to=%2Fx%3Fy%3D1"},
		{"https://lab.example.dev", "https://lab.example.dev/.nearhost/auth?t=", "&to=%2F"},
	} {
		r := withDevice(netRequest("GET", "/auth?"+url.Values{"to": {c.to}}.Encode(), nil), "good-token")
		w := serveNet(t, netHome(), r)
		loc := w.Header().Get("Location")
		if w.Code != http.StatusFound || !strings.HasPrefix(loc, c.prefix) || !strings.HasSuffix(loc, c.rest) {
			t.Errorf("to=%q: %d %q", c.to, w.Code, loc)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("to=%q: headers %v", c.to, w.Header())
		}
	}
}

func TestAuthWithoutDeviceGoesToPair(t *testing.T) {
	secret := freshSecret(t)
	setFakeDevices(t)
	for name, r := range map[string]*http.Request{
		"no cookie":    netRequest("GET", "/auth?to=https%3A%2F%2Fapp.lab.example.dev%2F", nil),
		"wrong device": withDevice(netRequest("GET", "/auth?to=https%3A%2F%2Fapp.lab.example.dev%2F", nil), "bad"),
	} {
		w := serveNet(t, netHome(), r)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/pair" {
			t.Errorf("%s: %d %q, want 302 /pair", name, w.Code, w.Header().Get("Location"))
		}
	}
	// The page secret alone names no device.
	r := netRequest("GET", "/auth?to=https%3A%2F%2Fapp.lab.example.dev%2F", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	w := serveNet(t, netHome(), r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/pair" {
		t.Errorf("page secret: %d %q, want 302 /pair", w.Code, w.Header().Get("Location"))
	}
}

func TestPairSetsDeviceCookie(t *testing.T) {
	freshSecret(t)
	f := setFakeDevices(t)
	w := serveNet(t, netHome(), pairPost(url.Values{"code": {"ABCD2345"}, "name": {"iPad"}}))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("good code: %d %q, want 303 /", w.Code, w.Header().Get("Location"))
	}
	sc := w.Header().Get("Set-Cookie")
	if !strings.HasPrefix(sc, "__Host-nearhost-device=token-b; Path=/; Secure; HttpOnly; SameSite=Lax") || !strings.Contains(sc, "Max-Age=34560000") {
		t.Errorf("Set-Cookie = %q", sc)
	}
	if len(f.pairs) != 1 || f.pairs[0] != "iPad" {
		t.Errorf("paired %v", f.pairs)
	}

	w = serveNet(t, netHome(), pairPost(url.Values{"code": {"ABCD2345"}, "name": {"iPad"}}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "That pairing code has been used or has expired") || !strings.Contains(w.Body.String(), "<code>nearhost pair</code>") {
		t.Errorf("used code: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a bad code set a cookie")
	}

	r := pairPost(url.Values{"code": {"ABCD2345"}})
	r.Header.Set("Origin", "https://evil.example")
	if w := serveNet(t, netHome(), r); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d, want 403", w.Code)
	}

	f.refused = true
	f.codes["ZZZZ2345"] = true
	if w := serveNet(t, netHome(), pairPost(url.Values{"code": {"ZZZZ2345"}})); w.Code != http.StatusTooManyRequests {
		t.Errorf("refused: %d, want 429", w.Code)
	}
	if !f.codes["ZZZZ2345"] {
		t.Error("a refused client could still try a code")
	}
}

// The name is capped at 64 runes and loses its control characters; with
// nothing left it is "device".
func TestPairCleansName(t *testing.T) {
	freshSecret(t)
	f := setFakeDevices(t)
	long := strings.Repeat("é", 100)
	for _, name := range []string{"Ed's\x00 iPad\n‮", long, "\x01\x02", ""} {
		f.codes["ABCD2345"] = true
		serveNet(t, netHome(), pairPost(url.Values{"code": {"ABCD2345"}, "name": {name}}))
	}
	want := []string{"Ed's iPad", strings.Repeat("é", 64), "device", "device"}
	if len(f.pairs) != len(want) {
		t.Fatalf("paired %q", f.pairs)
	}
	for i := range want {
		if f.pairs[i] != want[i] || utf8.RuneCountInString(f.pairs[i]) > 64 {
			t.Errorf("name %d = %q, want %q", i, f.pairs[i], want[i])
		}
	}
}

func TestPairForm(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	w := serveNet(t, netHome(), netRequest("GET", "/pair", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /pair: %d", w.Code)
	}
	for _, s := range []string{
		"Pair here, then add Nearhost to your Home Screen. It stays paired.",
		"Type the code <code>nearhost pair</code> printed.",
		`<script src="/pair.js"></script>`,
		`name="code"`,
		`name="name"`,
		`action="/pair"`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("GET /pair lacks %q", s)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") || !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if strings.Contains(body, "<script>") {
		t.Error("the form has an inline script")
	}

	w = serveNet(t, netHome(), netRequest("GET", "/pair/ABCD2345", nil))
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, `type="hidden" name="code" value="ABCD2345"`) {
		t.Errorf("GET /pair/ABCD2345: %d %s", w.Code, body)
	}

	w = serveNet(t, netHome(), netRequest("GET", "/pair.js", nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(w.Body.String(), "display-mode") {
		t.Errorf("GET /pair.js: %d %q", w.Code, w.Header().Get("Content-Type"))
	}

	// At localhost, and with no devices, there is nothing to pair.
	r := localhostRequest("GET", "/pair")
	if w := serveNet(t, netHome(), r); w.Code != http.StatusNotFound {
		t.Errorf("at localhost: %d, want 404", w.Code)
	}
	SetDevices(nil)
	if w := serveNet(t, netHome(), netRequest("GET", "/pair", nil)); w.Code != http.StatusNotFound {
		t.Errorf("no devices: %d, want 404", w.Code)
	}
}

func TestPageAcceptsDeviceCookie(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	fake := setFakeConsole(t)
	w := serveNet(t, netHome(), withDevice(netRequest("GET", "/", nil), "good-token"))
	if w.Code != http.StatusOK || w.Body.String() != "console" {
		t.Errorf("good device at /: %d %q", w.Code, w.Body.String())
	}
	w = serveNet(t, netHome(), withDevice(netRequest("GET", "/approve/abc", nil), "good-token"))
	if w.Code != http.StatusOK || len(fake.called()) != 2 {
		t.Errorf("good device at /approve/abc: %d, calls %v", w.Code, fake.called())
	}
	w = serveNet(t, netHome(), withDevice(netRequest("GET", "/approve/abc", nil), "bad"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Open Nearhost") {
		t.Errorf("wrong device: %d, want the locked page", w.Code)
	}
	w = serveNet(t, netHome(), withDevice(netRequest("GET", "/", nil), "bad"))
	if w.Body.String() == "console" {
		t.Error("a wrong device cookie reached the console")
	}
	// At localhost the device cookie counts for nothing.
	r := localhostRequest("GET", "/approve/abc")
	withDevice(r, "good-token")
	if w := serveNet(t, netHome(), r); w.Code != http.StatusForbidden {
		t.Errorf("device cookie at localhost: %d, want 403", w.Code)
	}
	if len(fake.called()) != 2 {
		t.Errorf("console calls %v", fake.called())
	}
}

func TestMobileconfigHasRootPayload(t *testing.T) {
	der := []byte{0x30, 0x82, 1, 2, 3}
	var p struct {
		PayloadType       string
		PayloadIdentifier string
		PayloadUUID       string
		PayloadVersion    int
		PayloadContent    []struct {
			PayloadType    string
			PayloadUUID    string
			PayloadContent []byte
		}
	}
	out := mobileconfig(der, "Nearhost Local CA")
	if _, err := plist.Unmarshal(out, &p); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if p.PayloadType != "Configuration" || p.PayloadIdentifier != "dev.nearhost.ca" || p.PayloadVersion != 1 {
		t.Errorf("profile = %+v", p)
	}
	if len(p.PayloadContent) != 1 || p.PayloadContent[0].PayloadType != "com.apple.security.root" || string(p.PayloadContent[0].PayloadContent) != string(der) {
		t.Fatalf("payloads = %+v", p.PayloadContent)
	}
	if p.PayloadUUID == "" || p.PayloadUUID == p.PayloadContent[0].PayloadUUID {
		t.Errorf("UUIDs %q %q", p.PayloadUUID, p.PayloadContent[0].PayloadUUID)
	}
	var again struct{ PayloadUUID string }
	plist.Unmarshal(mobileconfig(der, "x"), &again)
	if again.PayloadUUID == p.PayloadUUID {
		t.Error("UUIDs are not fresh")
	}
}

// writeRoot writes a self-signed root at storage's CA path, returning its
// DER.
func writeRoot(t *testing.T, storage string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := RootCertPath(storage)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return der
}

// The profile is served, without pairing, only with certificates from the
// local CA.
func TestProfileServedForLocalCA(t *testing.T) {
	freshSecret(t)
	setFakeDevices(t)
	storage := t.TempDir()
	der := writeRoot(t, storage)
	h := netHome()
	h.Certificates, h.StorageRoot = "ca", storage
	w := serveNet(t, h, netRequest("GET", "/profile.mobileconfig", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-apple-aspen-config" {
		t.Fatalf("ca: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var p struct {
		PayloadContent []struct{ PayloadContent []byte }
	}
	if _, err := plist.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.PayloadContent) != 1 || string(p.PayloadContent[0].PayloadContent) != string(der) {
		t.Errorf("profile does not hold the root: %v", err)
	}
	h = netHome()
	h.Certificates, h.StorageRoot = "acme", storage
	if w := serveNet(t, h, netRequest("GET", "/profile.mobileconfig", nil)); w.Code != http.StatusNotFound {
		t.Errorf("acme: %d, want 404", w.Code)
	}
}

// At the network host the page secret is never given or honoured: it can't
// be revoked, so only a paired device's cookie counts there.
func TestNetworkHostNeverGivesOrHonoursPageSecret(t *testing.T) {
	secret := freshSecret(t)
	setFakeDevices(t)
	fake := setFakeConsole(t)
	docs := 0
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		docs++
		w.Write([]byte("doc"))
	}), nil)

	// A document read by a paired device sets no page cookie.
	w := serveNet(t, netHome(), withDevice(netRequest("GET", "/d/abc123/x", nil), "good-token"))
	if w.Code != http.StatusOK || docs != 1 {
		t.Fatalf("paired device's document: %d, docs %d", w.Code, docs)
	}
	if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Errorf("document at the network host set %q", sc)
	}

	// Without a device, a document link alone reads nothing.
	for name, r := range map[string]*http.Request{
		"no cookie":    netRequest("GET", "/d/abc123/x", nil),
		"wrong device": withDevice(netRequest("GET", "/d/abc123/x", nil), "bad"),
		"page secret":  netRequest("GET", "/d/abc123/x", nil),
	} {
		if name == "page secret" {
			r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
		}
		w := serveNet(t, netHome(), r)
		if docs != 1 || w.Code != http.StatusForbidden || len(w.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s: %d, docs %d, Set-Cookie %q", name, w.Code, docs, w.Header().Values("Set-Cookie"))
		}
	}

	// /?k= with the right secret sets nothing.
	w = serveNet(t, netHome(), netRequest("GET", "/?k="+secret, nil))
	if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Errorf("/?k= at the network host set %q", sc)
	}

	// The page secret alone gets the locked page, not the console.
	r := netRequest("GET", "/approve/abc", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	if w := serveNet(t, netHome(), r); w.Code != http.StatusForbidden {
		t.Errorf("page secret at /approve/abc: %d, want 403", w.Code)
	}
	r = netRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
	if w := serveNet(t, netHome(), r); w.Body.String() == "console" {
		t.Error("the page secret reached the console at the network host")
	}
	if len(fake.called()) != 0 {
		t.Errorf("console calls %v", fake.called())
	}

	// At nearhost.localhost the page secret works as before.
	if w := serveNet(t, netHome(), withCookie("/approve/abc", secret)); w.Code != http.StatusOK {
		t.Errorf("page secret at the page host: %d, want 200", w.Code)
	}
}

// lanAtPageHost is a request at nearhost.localhost from a LAN client, as an
// unpaired device could send by naming that host in its SNI and Host.
func lanAtPageHost(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "nearhost.localhost:7443"
	r.RemoteAddr = "100.79.1.1:1"
	return r
}

// A client that is not loopback gets no document, no page cookie and no
// console at nearhost.localhost or localhost, networked or not: the page
// secret is for this machine's browsers alone.
func TestPageHostRefusesClientsOffLoopback(t *testing.T) {
	secret := freshSecret(t)
	setFakeDevices(t)
	fake := setFakeConsole(t)
	docs := 0
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		docs++
		w.Write([]byte("doc"))
	}), nil)
	for name, h := range map[string]func() *Home{
		"networked": netHome,
		"local":     func() *Home { return &Home{Version: "1.2.3", WebPort: 7443} },
	} {
		for _, host := range []string{"nearhost.localhost:7443", "localhost:7443"} {
			r := lanAtPageHost("GET", "/d/abc123/x")
			r.Host = host
			w := serveNet(t, h(), r)
			if w.Code == http.StatusOK || len(w.Header().Values("Set-Cookie")) != 0 {
				t.Errorf("%s %s /d/: %d, Set-Cookie %q, want no document and no cookie", name, host, w.Code, w.Header().Values("Set-Cookie"))
			}
		}
		w := serveNet(t, h(), lanAtPageHost("GET", "/?k="+secret))
		if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
			t.Errorf("%s /?k= from the LAN set %q", name, sc)
		}
		r := lanAtPageHost("GET", "/approve/abc")
		r.AddCookie(&http.Cookie{Name: "__Host-nearhost", Value: secret})
		if w := serveNet(t, h(), r); w.Code != http.StatusForbidden {
			t.Errorf("%s page cookie from the LAN at /approve/abc: %d, want 403", name, w.Code)
		}
	}
	if docs != 0 {
		t.Errorf("%d documents served off loopback", docs)
	}
	if len(fake.called()) != 0 {
		t.Errorf("console calls %v", fake.called())
	}
}
