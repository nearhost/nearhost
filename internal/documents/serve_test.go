package documents

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// htmlCSP and mdCSP are the policies every tree response must carry.
const (
	htmlCSP = "sandbox allow-scripts allow-popups allow-forms"
	mdCSP   = "sandbox allow-popups; default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data: https:"
)

// fakeFS wraps LocalFS and, when err is set, fails every call with it, as a
// connection that dropped would.
type fakeFS struct {
	LocalFS
	err error
}

// RealPath fails with f.err when it is set.
func (f fakeFS) RealPath(ctx context.Context, p string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.LocalFS.RealPath(ctx, p)
}

// Stat fails with f.err when it is set.
func (f fakeFS) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.LocalFS.Stat(ctx, p)
}

// Open fails with f.err when it is set.
func (f fakeFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.LocalFS.Open(ctx, p)
}

// fixture is a tree on disk served by a handler.
type fixture struct {
	dir, root string
	table     *Table
	tree      Tree
	h         http.Handler
}

// newFixture makes dir/root, adds it to a table as a tree on server "local",
// and serves it with LocalFS.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tbl := NewTable(time.Now)
	tr := tbl.Add("local", root, "claude", "plan.md")
	open := func(ctx context.Context, server string) (FS, error) { return LocalFS{}, nil }
	return &fixture{dir: dir, root: root, table: tbl, tree: tr, h: NewHandler(tbl, open)}
}

// write creates rel below the root, with its folders.
func (f *fixture) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// do sends method to the tree's raw (already escaped) path and returns the
// recorded response.
func (f *fixture) do(t *testing.T, method, rawPath string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/d/"+f.tree.ID+"/"+rawPath, nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// checkHeaders fails unless rec carries the isolation headers and csp.
func checkHeaders(t *testing.T, rec *httptest.ResponseRecorder, csp string) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":      "nosniff",
		"Referrer-Policy":             "no-referrer",
		"Cache-Control":               "no-cache",
		"Access-Control-Allow-Origin": "null",
		"Content-Security-Policy":     csp,
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want none", got)
	}
}

func TestServesHTMLAsWritten(t *testing.T) {
	f := newFixture(t)
	body := "<!doctype html><script>alert(1)</script>"
	f.write(t, "mock/index.html", body)
	rec := f.do(t, "GET", "mock/index.html")
	if rec.Code != 200 || rec.Body.String() != body {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	checkHeaders(t, rec, htmlCSP)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestServesSiblingAssets(t *testing.T) {
	f := newFixture(t)
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	f.write(t, "mock/app.css", "body{}")
	f.write(t, "mock/logo.png", png)
	f.write(t, "notes.txt", "hello")
	f.write(t, "main.go", "package main\n\nfunc main() {}\n")
	f.write(t, "blob.unknownext", "\x00\x01\x02\x03binary")
	for p, want := range map[string]string{
		"mock/app.css":    "text/css; charset=utf-8",
		"mock/logo.png":   "image/png",
		"notes.txt":       "text/plain; charset=utf-8",
		"main.go":         "text/plain; charset=utf-8",
		"blob.unknownext": "application/octet-stream",
	} {
		rec := f.do(t, "GET", p)
		if rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != want {
			t.Errorf("%s: Content-Type = %q, want %q", p, ct, want)
		}
		checkHeaders(t, rec, htmlCSP)
	}
	if rec := f.do(t, "GET", "mock/logo.png"); rec.Body.String() != png {
		t.Errorf("png body = %q", rec.Body.String())
	}
}

func TestMarkdownIsRendered(t *testing.T) {
	f := newFixture(t)
	src := "# Plan <b>\n"
	f.write(t, "plan.md", src)
	defer func(old func([]byte, string) ([]byte, error)) { renderMarkdown = old }(renderMarkdown)
	var gotTitle string
	renderMarkdown = func(b []byte, title string) ([]byte, error) {
		gotTitle = title
		return []byte("RENDERED:" + string(b)), nil
	}

	rec := f.do(t, "GET", "plan.md")
	if rec.Code != 200 || rec.Body.String() != "RENDERED:"+src {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if gotTitle != "plan.md" {
		t.Errorf("title = %q", gotTitle)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	checkHeaders(t, rec, mdCSP)
	if strings.Contains(rec.Header().Get("Content-Security-Policy"), "allow-scripts") {
		t.Error("markdown CSP allows scripts")
	}

	rec = f.do(t, "GET", "plan.md?raw")
	if rec.Code != 200 || rec.Body.String() != src {
		t.Fatalf("raw: got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("raw Content-Type = %q", ct)
	}
	checkHeaders(t, rec, htmlCSP)

	big := strings.Repeat("x", 5<<20+1)
	f.write(t, "big.markdown", big)
	rec = f.do(t, "GET", "big.markdown")
	if rec.Code != 200 || rec.Body.Len() != len(big) || strings.HasPrefix(rec.Body.String(), "RENDERED") {
		t.Fatalf("big: got %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("big Content-Type = %q", ct)
	}
}

func TestDefaultRenderMarkdownEscapes(t *testing.T) {
	out, err := renderMarkdown([]byte("<script>x</script>"), "t")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("<script>")) || !bytes.Contains(out, []byte("<pre>")) {
		t.Errorf("renderMarkdown = %q", out)
	}
}

func TestDotfilesAndEscapes404(t *testing.T) {
	f := newFixture(t)
	secret := "SECRET-BYTES"
	f.write(t, ".env", secret)
	f.write(t, "a/.git/config", secret)
	if err := os.WriteFile(filepath.Join(f.dir, "outside.md"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "outside", "x.md"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(f.dir, "outside", "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if err := os.Symlink("../outside", filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(f.root, "env.txt")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		".env", "a/.git/config", "%2e%2e/outside.md", "%2E%2E/outside.md",
		"../outside.md", "a/%2e%2e/%2e%2e/outside.md", "%2e%2e%2Foutside.md",
		"link/x.md", "link/nope.md", "link/x.md/y", "link/locked/x.md",
		"link/x.md?raw", "env.txt", "a/.git/config?raw",
	} {
		rec := f.do(t, "GET", p)
		if rec.Code != 404 {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		if body := rec.Body.String(); body != "404 page not found\n" {
			t.Errorf("%s: body %q, want the plain 404", p, body)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s: response contains the file", p)
		}
		checkHeaders(t, rec, mdCSP)
	}
}

func TestJunkBelowFileIsMissing(t *testing.T) {
	f := newFixture(t)
	f.write(t, "plan.md", "x")
	rec := f.do(t, "GET", "plan.md/anything")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "isn't there any more") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	rec = f.do(t, "GET", "nowhere/deeper/x.md")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "isn't there any more") {
		t.Errorf("missing folder: got %d %q", rec.Code, rec.Body.String())
	}
}

func TestSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "docs", "p.md"), []byte("# P"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	tbl := NewTable(time.Now)
	tr := tbl.Add("local", link, "claude", "docs/p.md")
	h := NewHandler(tbl, func(context.Context, string) (FS, error) { return LocalFS{}, nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+tr.ID+"/docs/p.md?raw", nil))
	if rec.Code != 200 || rec.Body.String() != "# P" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestUnknownTree404(t *testing.T) {
	f := newFixture(t)
	f.write(t, "plan.md", "x")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/AAAAAAAAAAAAAAAAAAAAAA/plan.md", nil))
	if rec.Code != 404 {
		t.Errorf("unknown tree: status %d", rec.Code)
	}
	checkHeaders(t, rec, mdCSP)
	for _, p := range []string{"/", "/d", "/d/", "/x/" + f.tree.ID + "/plan.md"} {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}

	f.write(t, "empty/readme.txt", "x")
	if rec := f.do(t, "GET", "empty/"); rec.Code != 404 {
		t.Errorf("folder without index: status %d", rec.Code)
	}
	if rec := f.do(t, "GET", ""); rec.Code != 404 {
		t.Errorf("root without index: status %d", rec.Code)
	}

	f.write(t, "site/index.html", "<p>site</p>")
	rec = f.do(t, "GET", "site/")
	if rec.Code != 200 || rec.Body.String() != "<p>site</p>" {
		t.Fatalf("folder with index: got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	checkHeaders(t, rec, htmlCSP)
	rec = f.do(t, "GET", "site")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "./site/" {
		t.Errorf("folder without slash: got %d Location %q", rec.Code, rec.Header().Get("Location"))
	}

	for name, want := range map[string]string{"https:evil.com": "./https:evil.com/", "a:b": "./a:b/"} {
		f.write(t, name+"/index.html", "x")
		rec := f.do(t, "GET", name)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: got %d Location %q, want %q", name, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	f.write(t, "index.html", "<p>root</p>")
	if rec := f.do(t, "GET", ""); rec.Code != 200 || rec.Body.String() != "<p>root</p>" {
		t.Errorf("root with index: got %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+f.tree.ID, nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "./"+f.tree.ID+"/" {
		t.Errorf("root without slash: got %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLiveReads(t *testing.T) {
	f := newFixture(t)
	f.write(t, "p.md", "first")
	if rec := f.do(t, "GET", "p.md?raw"); rec.Body.String() != "first" {
		t.Fatalf("first read = %q", rec.Body.String())
	}
	tmp := filepath.Join(f.root, "p.md.tmp")
	if err := os.WriteFile(tmp, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.root, "p.md")); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(t, "GET", "p.md?raw"); rec.Code != 200 || rec.Body.String() != "second" {
		t.Fatalf("after rename: %d %q", rec.Code, rec.Body.String())
	}
	if err := os.Remove(filepath.Join(f.root, "p.md")); err != nil {
		t.Fatal(err)
	}
	rec := f.do(t, "GET", "p.md")
	if rec.Code != 404 {
		t.Fatalf("after delete: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"isn't there any more", "local", filepath.Join(f.root, "p.md")} {
		if !strings.Contains(body, want) {
			t.Errorf("missing page lacks %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	checkHeaders(t, rec, mdCSP)
}

func TestNotConnectedPage(t *testing.T) {
	tbl := NewTable(time.Now)
	tr := tbl.Add("apollo", "/srv/app", "claude", "plan.md")
	h := NewHandler(tbl, func(_ context.Context, server string) (FS, error) {
		return nil, fmt.Errorf("%s: %w", server, ErrNotConnected)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+tr.ID+"/plan.md", nil))
	if rec.Code != 503 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Nearhost isn't connected to apollo", "press c"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page lacks %q:\n%s", want, rec.Body.String())
		}
	}
	checkHeaders(t, rec, mdCSP)

	// A connection that drops after the FS is handed out gives the same page.
	h = NewHandler(tbl, func(context.Context, string) (FS, error) {
		return fakeFS{err: fmt.Errorf("sftp: %w", ErrNotConnected)}, nil
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/d/"+tr.ID+"/plan.md", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Nearhost isn't connected to apollo") {
		t.Errorf("dropped connection: %d %q", rec.Code, rec.Body.String())
	}
}

func TestOddNames(t *testing.T) {
	f := newFixture(t)
	name := "2026 plan (v2) #1?%é.md"
	f.write(t, "sub dir/"+name, "odd")
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	u := URLFor(srv.URL, f.tree, "sub dir/"+name) + "?raw"
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "odd" {
		t.Fatalf("GET %s: %d %q", u, resp.StatusCode, b)
	}
}

func TestMethods(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.html", "<p>a</p>")
	rec := f.do(t, "POST", "a.html")
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	checkHeaders(t, rec, mdCSP)
	rec = f.do(t, "HEAD", "a.html")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("HEAD Content-Type = %q", ct)
	}
	checkHeaders(t, rec, htmlCSP)
}
