package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// pageClient is a client with a cookie jar of its own that dials the
// proxy's port for every request, with localhost as SNI.
func (h *harness) pageClient(t *testing.T, port int) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: h.pool, ServerName: homeHost},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Jar: jar, Timeout: 10 * time.Second}
}

// get GETs path on localhost:port with c, and returns the final status and
// body.
func get(t *testing.T, c *http.Client, port int, path string) (int, string) {
	t.Helper()
	resp, err := c.Get(fmt.Sprintf("https://localhost:%d%s", port, path))
	if err != nil {
		t.Fatalf("GET %s on port %d: %v", path, port, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The page lists the documents only to a browser that was given the cookie,
// by /?k= or by a document's link, and the proxy's logs never hold the
// secret.
func TestPageListNeedsCookie(t *testing.T) {
	secret := freshSecret(t)
	h := startProxyOpts(t, []Site{{Alias: "a.test", Hosts: []string{"a.test"}, Upstream: addr(named(t, "project"))}}, Options{WebPort: freePort(t)})
	waitUntil(t, 10*time.Second, func() bool {
		resp, err := h.pageClient(t, h.webPort).Get(fmt.Sprintf("https://localhost:%d/", h.webPort))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, "Nearhost's page")
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/d/abc123/plan.md" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "the plan")
	}), func() []HomeTree {
		return []HomeTree{{Server: "apollo", Root: "/srv/app", Docs: []HomeDoc{{Path: "plan.md", URL: "/d/abc123/plan.md"}}}}
	})

	port := h.webPort
	c := h.pageClient(t, port)
	if code, body := get(t, c, port, "/"); code != 200 || strings.Contains(body, "abc123") {
		t.Fatalf("port %d with no cookie: status %d, list shown: %v", port, code, strings.Contains(body, "abc123"))
	}
	if code, body := get(t, c, port, "/?k="+wrongSecret(secret)); code != 200 || strings.Contains(body, "abc123") {
		t.Fatalf("port %d after a wrong key: status %d, list shown: %v", port, code, strings.Contains(body, "abc123"))
	}
	if code, body := get(t, c, port, "/?k="+secret); code != 200 || !strings.Contains(body, "/d/abc123/plan.md") {
		t.Fatalf("port %d after the right key: status %d, list shown: %v", port, code, strings.Contains(body, "abc123"))
	}

	// A browser that only ever opened a document's link sees the list
	// too.
	c = h.pageClient(t, port)
	if code, _ := get(t, c, port, "/d/nope/plan.md"); code != 404 {
		t.Fatalf("port %d: an unknown document answered %d", port, code)
	}
	if _, body := get(t, c, port, "/"); strings.Contains(body, "abc123") {
		t.Fatalf("port %d: an unknown document's link gave the list", port)
	}
	if code, body := get(t, c, port, "/d/abc123/plan.md"); code != 200 || body != "the plan" {
		t.Fatalf("port %d: the document answered %d %q", port, code, body)
	}
	if _, body := get(t, c, port, "/"); !strings.Contains(body, "/d/abc123/plan.md") {
		t.Fatalf("port %d: a document's link did not give the list", port)
	}

	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	for _, l := range h.rec.lines {
		if strings.Contains(l, secret) {
			t.Error("a log line holds the page secret")
		}
	}
}
