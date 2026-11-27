package proxy

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	docs "github.com/nearhost/nearhost/internal/documents"
)

// docsCSS stands in for the app's documents.css.
const docsCSS = "/* the app's documents */ html[data-nh-theme] { color: teal; }\n"

// serveTree serves a tree holding an HTML document and a Markdown one
// below /d/, as the core does, and returns the path prefix of the tree.
func serveTree(t *testing.T) string {
	t.Helper()
	dir, err := docs.LocalFS{}.RealPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html": "<!doctype html>\n<html lang=\"en\"><head><title>t</title></head><body><p>hi</p><script>alert(1)</script></body></html>\n",
		"plan.md":    "# Plan\n\nText.\n",
	} {
		if err := os.WriteFile(filepath.Join(filepath.FromSlash(dir), name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tbl := docs.NewTable(time.Now)
	tr := tbl.Add(docs.LocalServer, dir, "claude", "plan.md")
	open := func(context.Context, string) (docs.FS, error) { return docs.LocalFS{}, nil }
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(docs.NewHandler(tbl, open), nil)
	return "/d/" + tr.ID + "/"
}

// setAppWithDocsCSS sets the test app with a documents.css for the test.
func setAppWithDocsCSS(t *testing.T) {
	t.Helper()
	fsys := testApp()
	fsys["documents.css"] = &fstest.MapFile{Data: []byte(docsCSS)}
	t.Cleanup(func() { SetApp(nil) })
	SetApp(fsys)
}

// sameAnswer fails unless got and want have the same body and headers.
func sameAnswer(t *testing.T, what string, got, want *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code != want.Code || got.Body.String() != want.Body.String() {
		t.Errorf("%s: %d %q, want %d %q", what, got.Code, got.Body.String(), want.Code, want.Body.String())
	}
	for _, k := range []string{"Content-Type", "Content-Security-Policy", "Cache-Control", "X-Content-Type-Options", "Referrer-Policy"} {
		if got.Header().Get(k) != want.Header().Get(k) {
			t.Errorf("%s: %s = %q, want %q", what, k, got.Header().Get(k), want.Header().Get(k))
		}
	}
}

// scriptsForbidden fails unless csp sandboxes without scripts and blocks
// every script source.
func scriptsForbidden(t *testing.T, what, csp string) {
	t.Helper()
	if strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("%s: CSP %q lets scripts run", what, csp)
	}
	if !strings.HasPrefix(csp, "sandbox") ||
		!(strings.Contains(csp, "script-src 'none'") || strings.Contains(csp, "default-src 'none'")) {
		t.Errorf("%s: CSP %q does not block scripts", what, csp)
	}
}

func TestDocumentThemesInlineTheAppStylesheet(t *testing.T) {
	base := serveTree(t)
	// Today's answers, with no app set.
	today := map[string]*httptest.ResponseRecorder{}
	for _, doc := range []string{"index.html", "plan.md"} {
		today[doc] = serveHome(t, "1.2.3", "GET", base+doc)
		if today[doc].Code != 200 {
			t.Fatalf("%s: status %d", doc, today[doc].Code)
		}
	}

	// With no app, every theme is written.
	for _, doc := range []string{"index.html", "plan.md"} {
		for _, q := range []string{"?theme=paper", "?theme=app&scheme=dark"} {
			sameAnswer(t, "no app: "+doc+q, serveHome(t, "1.2.3", "GET", base+doc+q), today[doc])
		}
	}

	setAppWithDocsCSS(t)
	for _, doc := range []string{"index.html", "plan.md"} {
		for _, q := range []string{"", "?theme=written", "?theme=bogus", "?theme=PAPER", "?theme=", "?scheme=dark"} {
			sameAnswer(t, doc+q, serveHome(t, "1.2.3", "GET", base+doc+q), today[doc])
		}
		for q, attrs := range map[string]string{
			"?theme=paper":                `data-nh-theme="paper"`,
			"?theme=paper&scheme=dark":    `data-nh-theme="paper" data-nh-scheme="dark"`,
			"?theme=app&scheme=light":     `data-nh-theme="app" data-nh-scheme="light"`,
			"?theme=app&scheme=dark":      `data-nh-theme="app" data-nh-scheme="dark"`,
			"?theme=app&scheme=%22%3E%3C": `data-nh-theme="app"`,
			"?theme=app":                  `data-nh-theme="app"`,
		} {
			w := serveHome(t, "1.2.3", "GET", base+doc+q)
			body := w.Body.String()
			if w.Code != 200 {
				t.Errorf("%s%s: status %d", doc, q, w.Code)
				continue
			}
			if n := strings.Count(body, docsCSS); n != 1 {
				t.Errorf("%s%s: the stylesheet appears %d times", doc, q, n)
			}
			if !strings.Contains(body, attrs) {
				t.Errorf("%s%s: no %s in %q", doc, q, attrs, body)
			}
			if !strings.Contains(q, "scheme=") || strings.Contains(q, "%22") {
				if strings.Contains(body, "data-nh-scheme") {
					t.Errorf("%s%s: names a scheme it was not given", doc, q)
				}
			}
			scriptsForbidden(t, doc+q, w.Header().Get("Content-Security-Policy"))
			if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("%s%s: Cache-Control = %q", doc, q, cc)
			}
			if et := w.Header().Get("ETag"); et != "" {
				t.Errorf("%s%s: ETag %q", doc, q, et)
			}
		}
	}
}

func TestDocumentThemeWithoutDocumentsCSSIsWritten(t *testing.T) {
	base := serveTree(t)
	today := serveHome(t, "1.2.3", "GET", base+"plan.md")
	setTestApp(t) // an app with no documents.css
	sameAnswer(t, "paper", serveHome(t, "1.2.3", "GET", base+"plan.md?theme=paper"), today)
}
