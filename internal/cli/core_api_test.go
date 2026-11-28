package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/pwa"
	"golang.org/x/net/html"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/testutil"
)

// With its page served, the core serves the API on the page host: with the
// page's cookie, the status reads as on the socket, a change made through
// the API reaches the engine, and its event streams to an open
// /api/v1/events at once. The stream ends when the core stops, and the API
// answers nothing after.
func TestCoreServesTheAPI(t *testing.T) {
	engines := make(chan *engine.Engine, 1)
	u := newUpRun(t, &config.Config{Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.engineOf(engines)
	u.start()
	u.waitFor("Serving until interrupted")
	eng := <-engines
	base, err := eng.PageURL()
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := proxy.PageKeyURL(base)
	if err != nil {
		t.Fatal(err)
	}
	c := pageBrowser(t, eng.WebPort())
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		resp, err := c.Get(keyed)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "the page to take its key")

	getJSON := func(path string, v any) {
		t.Helper()
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("GET %s = %d %q\n%s", path, resp.StatusCode, resp.Header.Get("Content-Type"), data)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("GET %s: %v\n%s", path, err, data)
		}
	}
	var st control.Status
	getJSON("/api/v1/status", &st)
	if st.Page != base || len(st.Projects) != 1 || st.Projects[0].Name != "web" {
		t.Errorf("status through the API = %+v, want the page %s and the project web", st, base)
	}
	var session proxy.Session
	getJSON("/api/v1/session", &session)
	if session.CSRF == "" {
		t.Fatalf("the session has no token: %+v", session)
	}

	// The stream is opened after the newest envelope, so only what comes
	// after it arrives. Its client has no timeout: the stream is long.
	sc := *c
	sc.Timeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/events?after=%d", base, u.hubSeq(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := sc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /api/v1/events = %d %q", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	envs := make(chan control.Envelope, 64)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var e control.Envelope
			if json.Unmarshal([]byte(data), &e) == nil {
				envs <- e
			}
		}
	}()

	post, err := http.NewRequest(http.MethodPost, base+"/api/v1/projects/web/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Origin", base)
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("X-Nearhost-CSRF", session.CSRF)
	resp, err := c.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST /api/v1/projects/web/stop = %d\n%s", resp.StatusCode, data)
	}
	timeout := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case e := <-envs:
			got = e.Kind == control.KindEvent && e.Event != nil && e.Event.Project == "web"
		case <-ended:
			t.Fatal("the stream ended before the project's event")
		case <-timeout:
			t.Fatal("the project's event did not stream within 5s")
		}
	}

	if err := u.stop(); err != nil {
		t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was still open 5s after the core stopped")
	}
	if resp, err := c.Get(base + "/api/v1/status"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /api/v1/status after the core stopped = %d, want no answer or 404", resp.StatusCode)
		}
	}
}

// With web_app on, the page host serves the web app from the pwa module to
// any browser, cookie or not; with it off, or with a module that carries
// no build, the page is today's, and the latter says why once.
func TestCoreServesTheWebApp(t *testing.T) {
	index, err := fs.ReadFile(pwa.FS(), "index.html")
	if err != nil {
		t.Fatalf("the pwa module has no build: %v", err)
	}
	on := true
	for _, tc := range []struct {
		name    string
		webApp  *bool
		built   bool
		wantApp bool
	}{
		{"on", &on, true, true},
		{"off", nil, true, false},
		{"unbuilt", &on, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := webAppBuilt
			webAppBuilt = func() bool { return tc.built }
			t.Cleanup(func() { webAppBuilt = old })
			engines := make(chan *engine.Engine, 1)
			u := newUpRun(t, &config.Config{WebApp: tc.webApp, Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
			u.noHosts = true
			u.engineOf(engines)
			u.start()
			u.waitFor("Serving until interrupted")
			eng := <-engines
			base, err := eng.PageURL()
			if err != nil {
				t.Fatal(err)
			}
			c := pageBrowser(t, eng.WebPort())
			for _, path := range []string{"/", "/approve/abc"} {
				resp, err := c.Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				gotApp := resp.StatusCode == http.StatusOK && string(body) == string(index)
				if gotApp != tc.wantApp {
					t.Errorf("GET %s = %d, the app %v, want %v\n%.300s", path, resp.StatusCode, gotApp, tc.wantApp, body)
				}
				if tc.wantApp && !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
					t.Errorf("GET %s: CSP %q", path, resp.Header.Get("Content-Security-Policy"))
				}
			}
			const why = "built without the web app"
			if got := strings.Count(u.out.String(), why); got != map[bool]int{true: 1}[tc.name == "unbuilt"] {
				t.Errorf("%q said %d times; output:\n%s", why, got, u.out.String())
			}
			if err := u.stop(); err != nil {
				t.Fatalf("up returned %v; output:\n%s", err, u.out.String())
			}
		})
	}
}

// startAppCore starts a core with web_app on, serving the real pwa module,
// and returns its page's base URL and a browser holding the page's cookie.
func startAppCore(t *testing.T) (*upRun, string, *http.Client) {
	t.Helper()
	if !pwa.Built() {
		t.Fatal("the pwa module has no build")
	}
	on := true
	engines := make(chan *engine.Engine, 1)
	u := newUpRun(t, &config.Config{WebApp: &on, Projects: []config.Project{localProject("web", testutil.Upstream(t, "web"), true)}})
	u.noHosts = true
	u.engineOf(engines)
	u.start()
	u.waitFor("Serving until interrupted")
	eng := <-engines
	base, err := eng.PageURL()
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := proxy.PageKeyURL(base)
	if err != nil {
		t.Fatal(err)
	}
	c := pageBrowser(t, eng.WebPort())
	testutil.WaitUntil(t, 10*time.Second, func() bool {
		resp, err := c.Get(keyed)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "the page to take its key")
	t.Cleanup(func() {
		if err := u.stop(); err != nil {
			t.Errorf("up returned %v; output:\n%s", err, u.out.String())
		}
	})
	return u, base, c
}

// getPage gets base+path with c and returns the answer and its body.
func getPage(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// cspDirectives parses a policy into its directives' sources, by name.
func cspDirectives(policy string) map[string][]string {
	out := map[string][]string{}
	for _, d := range strings.Split(policy, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		name := strings.ToLower(f[0])
		if _, seen := out[name]; !seen {
			out[name] = f[1:]
		}
	}
	return out
}

// effectiveSources is what a policy allows for directive: its own sources,
// or default-src's when it names none; 'none' is no source.
func effectiveSources(p map[string][]string, directive string) []string {
	src, ok := p[directive]
	if !ok {
		src = p["default-src"]
	}
	var out []string
	for _, s := range src {
		if s != "'none'" {
			out = append(out, s)
		}
	}
	return out
}

// metaCSP returns the policy of index's <meta http-equiv=content-security-policy>.
func metaCSP(t *testing.T, index string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(index))
	for {
		switch z.Next() {
		case html.ErrorToken:
			t.Fatal("index.html has no CSP meta tag")
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != "meta" {
				continue
			}
			var equiv, content string
			for _, a := range tok.Attr {
				switch strings.ToLower(a.Key) {
				case "http-equiv":
					equiv = a.Val
				case "content":
					content = a.Val
				}
			}
			if strings.EqualFold(equiv, "content-security-policy") {
				return content
			}
		}
	}
}

// The policy the core sends the app with holds the same script hashes as
// the one SvelteKit put in index.html's meta tag, and allows no less than
// it for what the app needs: a hash the header lacked would block the app's
// boot script, and a narrower directive its frames, worker or API.
func TestAppCSPMatchesSvelteKit(t *testing.T) {
	_, base, c := startAppCore(t)
	resp, index := getPage(t, c, base+"/")
	want, err := fs.ReadFile(pwa.FS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || index != string(want) {
		t.Fatalf("GET / = %d, not the app's index.html", resp.StatusCode)
	}
	header := cspDirectives(resp.Header.Get("Content-Security-Policy"))
	meta := cspDirectives(metaCSP(t, index))

	hashes := func(p map[string][]string) map[string]bool {
		out := map[string]bool{}
		for _, s := range effectiveSources(p, "script-src") {
			if strings.HasPrefix(s, "'sha256-") {
				out[s] = true
			}
		}
		return out
	}
	hh, mh := hashes(header), hashes(meta)
	if len(mh) == 0 {
		t.Fatalf("the meta policy names no script hash: %v", meta["script-src"])
	}
	for h := range mh {
		if !hh[h] {
			t.Errorf("the header's script-src lacks the meta's %s", h)
		}
	}
	for h := range hh {
		if !mh[h] {
			t.Errorf("the header's script-src has %s, which the meta's lacks", h)
		}
	}
	for _, d := range []string{"script-src", "style-src", "img-src", "font-src", "connect-src", "frame-src", "worker-src", "manifest-src"} {
		has := effectiveSources(header, d)
		for _, s := range effectiveSources(meta, d) {
			if !slices.Contains(has, s) {
				t.Errorf("the header's %s %v lacks the meta's %s", d, has, s)
			}
		}
	}
	for d, want := range map[string][]string{
		"connect-src": {"'self'"}, "worker-src": {"'self'"}, "frame-ancestors": {"'none'"},
	} {
		if got := header[d]; !slices.Equal(got, want) {
			t.Errorf("the header's %s = %v, want %v", d, got, want)
		}
	}
}

// The app's worker is served from its build, so it updates with the core
// and runs under the app's policy; the page's old worker is retired, never
// answered with the app's index.html, which a browser would refuse as a
// worker on every update check.
func TestCoreServesTheAppsWorker(t *testing.T) {
	_, base, c := startAppCore(t)
	root, _ := getPage(t, c, base+"/")
	want, err := fs.ReadFile(pwa.FS(), "service-worker.js")
	if err != nil {
		t.Fatalf("the pwa module has no service-worker.js: %v", err)
	}
	resp, body := getPage(t, c, base+"/service-worker.js")
	if resp.StatusCode != http.StatusOK || body != string(want) {
		t.Errorf("GET /service-worker.js = %d, not the app's worker", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("/service-worker.js Content-Type %q, want JavaScript", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/service-worker.js Cache-Control %q, want no-cache", got)
	}
	if got, want := resp.Header.Get("Content-Security-Policy"), root.Header.Get("Content-Security-Policy"); got != want {
		t.Errorf("/service-worker.js CSP %q, want the app's %q", got, want)
	}

	resp, body = getPage(t, c, base+"/sw.js")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("GET /sw.js = %d %q, want the retired worker", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "skipWaiting") || strings.Contains(body, "'fetch'") || strings.Contains(body, "<html") {
		t.Errorf("GET /sw.js is not the retired worker:\n%s", body)
	}
}

// A Markdown document asked for with ?theme=paper is styled by the app's
// own documents.css, inlined, and has no link home in the reader's frame.
func TestCoreThemesDocumentsWithTheApp(t *testing.T) {
	_, base, c := startAppCore(t)
	css, err := fs.ReadFile(pwa.FS(), "documents.css")
	if err != nil {
		t.Fatalf("the pwa module has no documents.css: %v", err)
	}
	dir, err := documents.LocalFS{}.RealPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.FromSlash(dir), "plan.md"), []byte("# Plan\n\nText.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tbl := documents.NewTable(time.Now)
	tr := tbl.Add(documents.LocalServer, dir, "claude-code", "plan.md")
	open := func(context.Context, string) (documents.FS, error) { return documents.LocalFS{}, nil }
	t.Cleanup(func() { proxy.SetDocuments(nil, nil) })
	proxy.SetDocuments(documents.NewHandler(tbl, open), nil)

	resp, body := getPage(t, c, base+"/d/"+tr.ID+"/plan.md?theme=paper")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET the themed document = %d\n%.300s", resp.StatusCode, body)
	}
	if !strings.Contains(body, string(css)) || !strings.Contains(body, `data-nh-theme="paper"`) {
		t.Errorf("the themed document does not inline the app's documents.css:\n%.500s", body)
	}
	if strings.Contains(body, `class="home"`) {
		t.Errorf("the themed document links home")
	}
	_, plain := getPage(t, c, base+"/d/"+tr.ID+"/plan.md")
	if strings.Contains(plain, string(css)) || !strings.Contains(plain, `class="home"`) {
		t.Errorf("the unthemed document changed:\n%.500s", plain)
	}
}
