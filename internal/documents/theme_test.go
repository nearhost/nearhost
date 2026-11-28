package documents

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// themeCSS stands in for the app's documents.css.
const themeCSS = "/* app documents */ html[data-nh-theme] { color: rebeccapurple; }\n"

// doThemed is f.do with th on the request's context, as the page sets it.
func (f *fixture) doThemed(t *testing.T, method, rawPath string, th Theme) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/d/"+f.tree.ID+"/"+rawPath, nil)
	req = req.WithContext(WithTheme(req.Context(), th))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// forbidsScripts fails unless csp sandboxes the document without scripts
// and blocks every script source.
func forbidsScripts(t *testing.T, csp string) {
	t.Helper()
	if strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP %q lets scripts run", csp)
	}
	sandboxed, blocked := false, false
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "sandbox":
			sandboxed = true
		case f[0] == "script-src" && len(f) == 2 && f[1] == "'none'":
			blocked = true
		case f[0] == "default-src" && len(f) == 2 && f[1] == "'none'":
			blocked = true
		}
	}
	if !sandboxed || !blocked {
		t.Errorf("CSP %q: sandboxed %v, scripts blocked %v", csp, sandboxed, blocked)
	}
}

func TestThemedHTMLGetsTheStylesheetOnce(t *testing.T) {
	f := newFixture(t)
	doc := "<!doctype html>\n<html lang=\"en\"><head><title>x</title><style>p{}</style></head><body><p>hi</p><script>alert(1)</script></body></html>\n"
	f.write(t, "mock/index.html", doc)
	rec := f.doThemed(t, "GET", "mock/index.html", Theme{Name: "paper", CSS: []byte(themeCSS)})
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if n := strings.Count(body, themeCSS); n != 1 {
		t.Fatalf("stylesheet appears %d times in %q", n, body)
	}
	want := "<!doctype html>\n<html data-nh-theme=\"paper\" lang=\"en\"><head><style>\n" + themeCSS + "</style><title>x</title>"
	if !strings.HasPrefix(body, want) {
		t.Errorf("body = %q, want it to start %q", body, want)
	}
	if !strings.HasSuffix(body, "<title>x</title><style>p{}</style></head><body><p>hi</p><script>alert(1)</script></body></html>\n") {
		t.Errorf("the rest of the document changed: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	checkHeaders(t, rec, themedHTMLCSP)
	forbidsScripts(t, rec.Header().Get("Content-Security-Policy"))

	// HEAD gets the same policy and reads nothing.
	rec = f.doThemed(t, "HEAD", "mock/index.html", Theme{Name: "paper", CSS: []byte(themeCSS)})
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	checkHeaders(t, rec, themedHTMLCSP)
}

func TestThemedMarkdownUsesTheAppStylesheet(t *testing.T) {
	f := newFixture(t)
	f.write(t, "plan.md", "# Plan\n\n<script>alert(1)</script>\n")
	rec := f.doThemed(t, "GET", "plan.md", Theme{Name: "app", Scheme: "dark", CSS: []byte(themeCSS)})
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if n := strings.Count(body, themeCSS); n != 1 {
		t.Fatalf("stylesheet appears %d times in %q", n, body)
	}
	if strings.Contains(body, markdownCSS) {
		t.Error("the core's own markdown.css is still inlined")
	}
	if !strings.Contains(body, `<html lang="en" data-nh-theme="app" data-nh-scheme="dark">`) {
		t.Errorf("no theme attributes on <html>: %q", body)
	}
	if !strings.Contains(body, "<h1 id=\"plan\">Plan</h1>") {
		t.Errorf("not rendered: %q", body)
	}
	checkHeaders(t, rec, mdCSP)
	forbidsScripts(t, rec.Header().Get("Content-Security-Policy"))

	// Raw stays raw.
	rec = f.doThemed(t, "GET", "plan.md?raw", Theme{Name: "paper", CSS: []byte(themeCSS)})
	if strings.Contains(rec.Body.String(), themeCSS) {
		t.Error("raw markdown got the stylesheet")
	}
}

// The theme lives on the context the page sets, never in the query: the
// documents handler itself serves ?theme= as written.
func TestThemeQueryAloneChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.write(t, "mock/index.html", "<!doctype html><p>x")
	f.write(t, "plan.md", "# Plan\n")
	for _, p := range []string{"mock/index.html", "plan.md"} {
		plain := f.do(t, "GET", p)
		for _, q := range []string{"?theme=paper", "?theme=written", "?theme=app&scheme=dark"} {
			got := f.do(t, "GET", p+q)
			if got.Body.String() != plain.Body.String() {
				t.Errorf("%s%s differs from %s", p, q, p)
			}
			if got.Header().Get("Content-Security-Policy") != plain.Header().Get("Content-Security-Policy") {
				t.Errorf("%s%s: CSP differs", p, q)
			}
		}
	}
}

// A theme with no stylesheet is no theme.
func TestEmptyThemeIsWritten(t *testing.T) {
	f := newFixture(t)
	f.write(t, "mock/index.html", "<!doctype html><p>x")
	rec := f.doThemed(t, "GET", "mock/index.html", Theme{Name: "paper"})
	if rec.Body.String() != "<!doctype html><p>x" {
		t.Errorf("body = %q", rec.Body.String())
	}
	checkHeaders(t, rec, htmlCSP)
}

func TestThemeRenderedMarkdownIsTodaysWithoutATheme(t *testing.T) {
	plain, err := render([]byte("# a\n"), "a.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "<html lang=\"en\">\n") || !strings.Contains(string(plain), markdownCSS) {
		t.Errorf("unthemed render changed: %q", plain)
	}
}

func TestInjectTheme(t *testing.T) {
	th := &Theme{Name: "paper", Scheme: "light", CSS: []byte("X")}
	attrs := `data-nh-theme="paper" data-nh-scheme="light"`
	style := "<style>\nX</style>"
	for name, c := range map[string]struct{ in, want string }{
		"full": {
			"<!DOCTYPE html><HTML><Head><title>t</title></head><body>b</body></HTML>",
			"<!DOCTYPE html><HTML " + attrs + "><Head>" + style + "<title>t</title></head><body>b</body></HTML>",
		},
		"self-closing html tag": {
			"<html/><p>b",
			"<html " + attrs + "/>" + style + "<p>b",
		},
		"no head": {
			"<!doctype html>\n<html>\n<body>b",
			"<!doctype html>\n<html " + attrs + ">" + style + "\n<body>b",
		},
		"no html": {
			"<!doctype html>\n<title>t</title><p>b",
			"<!doctype html>\n<html " + attrs + ">" + style + "<title>t</title><p>b",
		},
		"fragment": {
			"<p>b</p>",
			"<html " + attrs + ">" + style + "<p>b</p>",
		},
		"text first": {
			"hello <b>x</b>",
			"<html " + attrs + ">" + style + "hello <b>x</b>",
		},
		"empty": {
			"",
			"<html " + attrs + ">" + style,
		},
		"bom": {
			"\ufeff<!doctype html><p>b",
			"\ufeff<!doctype html><html " + attrs + ">" + style + "<p>b",
		},
		"comments before html": {
			"<!-- a --><html><head></head>",
			"<!-- a --><html " + attrs + "><head>" + style + "</head>",
		},
		"html in a comment": {
			"<!-- <html><head> --><p>b",
			"<!-- <html><head> --><html " + attrs + ">" + style + "<p>b",
		},
		"cut-off html tag": {
			"<!doctype html><html",
			"<!doctype html><html " + attrs + ">" + style + "<html",
		},
		"head after body content": {
			"<html><p>b</p><head></head>",
			"<html " + attrs + ">" + style + "<p>b</p><head></head>",
		},
	} {
		if got := string(injectTheme([]byte(c.in), th)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}

	// A scheme is only named when given.
	got := string(injectTheme([]byte("<html>"), &Theme{Name: "app", CSS: []byte("X")}))
	if got != `<html data-nh-theme="app">`+style {
		t.Errorf("no scheme: %q", got)
	}
}

func TestThemeFromContext(t *testing.T) {
	if _, ok := themeFrom(context.Background()); ok {
		t.Error("a bare context has a theme")
	}
	ctx := WithTheme(context.Background(), Theme{Name: "paper", CSS: []byte("x")})
	if th, ok := themeFrom(ctx); !ok || th.Name != "paper" {
		t.Errorf("themeFrom = %+v, %v", th, ok)
	}
	if _, ok := themeFrom(WithTheme(context.Background(), Theme{Name: "paper"})); ok {
		t.Error("a theme without a stylesheet counts")
	}
}

// A themed HTML document too big to theme is sent as written, still with
// scripts off: when it is measured too big, and when it grew past the
// limit after it was measured.
func TestThemedHTMLTooBigKeepsScriptsOff(t *testing.T) {
	th := Theme{Name: "paper", CSS: []byte(themeCSS)}
	big := "<!doctype html><script>alert(1)</script>" + strings.Repeat("x", markdownLimit)

	f := newFixture(t)
	f.write(t, "big.html", big)
	rec := f.doThemed(t, "GET", "big.html", th)
	if rec.Code != 200 || rec.Body.String() != big {
		t.Fatalf("measured too big: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	checkHeaders(t, rec, themedHTMLCSP)
	forbidsScripts(t, rec.Header().Get("Content-Security-Policy"))

	// Grown since it was measured: the size given is small, the file read
	// is not.
	req := httptest.NewRequest("GET", "/d/x/grown.html", nil)
	req = req.WithContext(WithTheme(req.Context(), th))
	rec = httptest.NewRecorder()
	if err := sendFile(rec, req, strings.NewReader(big), "grown.html", 10); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || rec.Body.String() != big {
		t.Fatalf("grown: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != themedHTMLCSP {
		t.Errorf("grown: CSP = %q, want %q", csp, themedHTMLCSP)
	}
}

// A themed page is read in the web app's frame, where the link home would
// load the app inside the frame, which the app's policy refuses: it is left
// out there and kept on today's page.
func TestThemedMarkdownHasNoHomeLink(t *testing.T) {
	const home = `<a class="home" href="/">Nearhost</a>`
	themed, err := render([]byte("# a\n"), "a.md", &Theme{Name: "paper", CSS: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(themed), home) {
		t.Errorf("the themed page links home: %s", themed)
	}
	plain, err := render([]byte("# a\n"), "a.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), home) {
		t.Errorf("today's page lost its link home: %s", plain)
	}
}
