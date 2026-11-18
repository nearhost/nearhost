package documents

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// htmlCSP and mdCSP are the policies every tree response must carry.
const (
	htmlCSP = "sandbox allow-scripts allow-popups allow-forms"
	mdCSP   = "sandbox allow-popups; default-src 'none'; base-uri 'none'; style-src 'unsafe-inline'; img-src 'self' data: https:"
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
	return newFixtureAt(t, "root")
}

// newFixtureAt is newFixture with the root at dir/rel. The tree is hidden
// when rel has a segment starting with ".": the temporary folder above it
// may be in a hidden folder itself, such as a TMPDIR under ~/.worktrees, and
// it does not count.
func newFixtureAt(t *testing.T, rel string) *fixture {
	t.Helper()
	// The documents package works in slash form, so a Windows folder is
	// given as C:/…, not C:\….
	dir := filepath.ToSlash(t.TempDir())
	root := dir + "/" + rel
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tbl := NewTable(time.Now)
	tr := tbl.Add("local", root, "claude", "plan.md")
	tbl.trees[tr.ID].Hidden = InHiddenFolder(rel)
	tr.Hidden = InHiddenFolder(rel)
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
// Access-Control-Allow-Origin depends on the type; TestOnlyAssetsAreReadable
// covers it.
func checkHeaders(t *testing.T, rec *httptest.ResponseRecorder, csp string) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-cache",
		"Content-Security-Policy": csp,
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
	// Root reads a 0o000 folder anyway, so the EACCES case only holds for
	// other users.
	paths := []string{
		".env", "a/.git/config", "%2e%2e/outside.md", "%2E%2E/outside.md",
		"../outside.md", "a/%2e%2e/%2e%2e/outside.md", "%2e%2e%2Foutside.md",
		"link/x.md", "link/nope.md", "link/x.md/y",
		"link/x.md?raw", "env.txt", "a/.git/config?raw",
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(f.dir, "outside", "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		paths = append(paths, "link/locked/x.md")
	}
	testutil.Symlink(t, "../outside", filepath.Join(f.root, "link"))
	testutil.Symlink(t, ".env", filepath.Join(f.root, "env.txt"))
	for _, p := range paths {
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
	testutil.Symlink(t, real, link)
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

	names := map[string]string{"https:evil.com": "./https:evil.com/", "a:b": "./a:b/"}
	if runtime.GOOS == "windows" {
		names = nil // Windows forbids a colon in a folder name.
	}
	for name, want := range names {
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
	for _, want := range []string{"isn't there any more", "local", path.Join(f.root, "p.md")} {
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
	if runtime.GOOS == "windows" {
		// Windows forbids ? in a file name.
		name = strings.ReplaceAll(name, "?", "")
	}
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

func TestSourceFilesAreTextWhateverTheHostSays(t *testing.T) {
	// macOS's /etc/apache2/mime.types maps .ts and .mts to video/mp2t; a
	// host that says so for every extension must not turn source into video.
	old := typeByExtension
	typeByExtension = func(string) string { return "video/mp2t" }
	t.Cleanup(func() { typeByExtension = old })
	f := newFixture(t)
	for _, p := range []string{"src/app.ts", "src/view.tsx", "src/mod.mts", "src/app.jsx", "lib.rs", "conf.yaml", "notes.weird"} {
		f.write(t, p, "export const x = 1;\n")
		rec := f.do(t, "GET", p)
		if rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", p, ct)
		}
	}
}

// assetFiles are web assets a page may load in its opaque origin, with the
// type each is served as.
var assetFiles = map[string]string{
	"app.js":     "text/javascript; charset=utf-8",
	"mod.mjs":    "text/javascript; charset=utf-8",
	"app.css":    "text/css; charset=utf-8",
	"font.woff2": "font/woff2",
	"logo.png":   "image/png",
	"icon.svg":   "image/svg+xml",
	"clip.mp4":   "video/mp4",
	"sound.mp3":  "audio/mpeg",
	"lib.wasm":   "application/wasm",
}

// dataFiles are requests for files a page's script must not read. "sub/"
// serves sub/index.html.
var dataFiles = []string{
	"data.json", "app.js.map", "page.html", "notes.txt", "plan.md", "plan.md?raw",
	"ci.yml", "rows.csv", "app.sqlite", "terraform.tfstate", "main.go", "blob.unknownext", "sub/",
}

// writeAssetsAndData writes every file of assetFiles and dataFiles below f's
// root.
func writeAssetsAndData(t *testing.T, f *fixture) {
	t.Helper()
	for p := range assetFiles {
		f.write(t, p, "x")
	}
	for _, p := range dataFiles {
		p = strings.TrimSuffix(p, "?raw")
		if strings.HasSuffix(p, "/") {
			p += "index.html"
		}
		f.write(t, p, "x")
	}
}

func TestOnlyAssetsAreReadable(t *testing.T) {
	f := newFixture(t)
	writeAssetsAndData(t, f)
	for p, ct := range assetFiles {
		for _, method := range []string{"GET", "HEAD"} {
			rec := f.do(t, method, p)
			if rec.Code != 200 || rec.Header().Get("Content-Type") != ct {
				t.Errorf("%s %s: %d %q, want 200 %q", method, p, rec.Code, rec.Header().Get("Content-Type"), ct)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "null" {
				t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want \"null\"", method, p, got)
			}
		}
	}
	for _, p := range append(dataFiles, "missing.js", "../x.js", "app.js/") {
		for _, method := range []string{"GET", "HEAD", "POST"} {
			rec := f.do(t, method, p)
			if got, ok := rec.Header()["Access-Control-Allow-Origin"]; ok {
				t.Errorf("%s %s (%d, %q): Access-Control-Allow-Origin = %q, want none", method, p, rec.Code, rec.Header().Get("Content-Type"), got)
			}
		}
	}
}

func TestAssetSymlinkToDataIsNotAnAsset(t *testing.T) {
	// The resolved file decides, so a script name can't make a secret
	// readable through an in-tree symlink.
	f := newFixture(t)
	f.write(t, "config/secrets.json", `{"key":"x"}`)
	f.write(t, "build/app.js", "export {}")
	f.write(t, "notes/plan.md", "# Plan")
	if err := os.MkdirAll(filepath.Join(f.root, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, filepath.Join(f.root, "config", "secrets.json"), filepath.Join(f.root, "public", "config.js"))
	testutil.Symlink(t, filepath.Join(f.root, "config", "secrets.json"), filepath.Join(f.root, "public", "x.css"))
	testutil.Symlink(t, filepath.Join(f.root, "notes", "plan.md"), filepath.Join(f.root, "public", "plan.png"))
	testutil.Symlink(t, filepath.Join(f.root, "build", "app.js"), filepath.Join(f.root, "public", "app.js"))
	for p, want := range map[string]string{
		"public/config.js": "application/json",
		"public/x.css":     "application/json",
		"public/plan.png":  "text/html; charset=utf-8",
	} {
		for _, method := range []string{"GET", "HEAD"} {
			rec := f.do(t, method, p)
			if rec.Code != 200 || rec.Header().Get("Content-Type") != want {
				t.Errorf("%s %s: %d %q, want 200 %q", method, p, rec.Code, rec.Header().Get("Content-Type"), want)
			}
			if got, ok := rec.Header()["Access-Control-Allow-Origin"]; ok {
				t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want none", method, p, got)
			}
		}
	}
	rec := f.do(t, "GET", "public/app.js")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Errorf("public/app.js: %d %q, Access-Control-Allow-Origin %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestAssetsKeepTheirTypeWhateverTheHostSays(t *testing.T) {
	// A host whose tables are wrong (a Windows registry can map .js to
	// text/plain) must not break a page's assets, and one that calls
	// everything a script must not make data readable.
	old := typeByExtension
	t.Cleanup(func() { typeByExtension = old })
	f := newFixture(t)
	writeAssetsAndData(t, f)
	for _, host := range []string{"", "text/plain", "application/octet-stream", "text/javascript"} {
		typeByExtension = func(string) string { return host }
		for p, ct := range assetFiles {
			rec := f.do(t, "GET", p)
			if rec.Header().Get("Content-Type") != ct || rec.Header().Get("Access-Control-Allow-Origin") != "null" {
				t.Errorf("host %q, %s: %q, Access-Control-Allow-Origin %q", host, p, rec.Header().Get("Content-Type"), rec.Header().Get("Access-Control-Allow-Origin"))
			}
		}
		for _, p := range dataFiles {
			rec := f.do(t, "GET", p)
			if got, ok := rec.Header()["Access-Control-Allow-Origin"]; ok {
				t.Errorf("host %q, %s (%q): Access-Control-Allow-Origin = %q, want none", host, p, rec.Header().Get("Content-Type"), got)
			}
		}
	}
}

// countingFS is LocalFS whose opened files count the bytes read from them.
type countingFS struct {
	LocalFS
	read *int64
}

// Open opens p and counts what is read from it.
func (f countingFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	r, err := f.LocalFS.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return countingReader{r, f.read}, nil
}

// countingReader adds the bytes read through it to n.
type countingReader struct {
	io.ReadCloser
	n *int64
}

// Read reads into b and counts it.
func (c countingReader) Read(b []byte) (int, error) {
	k, err := c.ReadCloser.Read(b)
	*c.n += int64(k)
	return k, err
}

func TestHeadReadsNoBody(t *testing.T) {
	f := newFixture(t)
	f.write(t, "big.css", strings.Repeat("a{}\n", 1<<16))
	var read int64
	f.h = NewHandler(f.table, func(context.Context, string) (FS, error) { return countingFS{read: &read}, nil })
	rec := f.do(t, "HEAD", "big.css")
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("HEAD: %d, %d bytes, %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"))
	}
	if read != 0 {
		t.Errorf("HEAD read %d bytes of the file, want none", read)
	}

	// Markdown gets the type GET would, rendered or raw, without being read.
	f.write(t, "plan.md", "# Plan\n")
	f.write(t, "huge.md", strings.Repeat("x", markdownLimit+1))
	for p, want := range map[string]string{
		"plan.md": "text/html; charset=utf-8", "plan.md?raw": textPlain, "huge.md": textPlain,
	} {
		read = 0
		rec := f.do(t, "HEAD", p)
		if read != 0 {
			t.Errorf("HEAD %s read %d bytes of the file, want none", p, read)
		}
		get := f.do(t, "GET", p)
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != want || get.Header().Get("Content-Type") != want {
			t.Errorf("HEAD %s: %d, %d bytes, %q (GET %q), want %q", p, rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"), get.Header().Get("Content-Type"), want)
		}
		if got, wantCSP := rec.Header().Get("Content-Security-Policy"), get.Header().Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("HEAD %s: CSP %q, GET has %q", p, got, wantCSP)
		}
	}
}

// hiddenServed and hiddenRefused are the files a tree in a hidden folder
// serves and the ones it answers as missing.
var (
	hiddenServed  = []string{"notes.md", "page.html", "img.png", "app.js", "style.css"}
	hiddenRefused = []string{"id_ed25519", "credentials", "settings.json", "secrets.yml", "data.csv", "config", "x.md"}
)

// writeHiddenFiles writes every file of hiddenServed and hiddenRefused below
// f's root, with x.md a symlink to id_ed25519.
func writeHiddenFiles(t *testing.T, f *fixture) {
	t.Helper()
	for _, p := range append(hiddenServed, hiddenRefused...) {
		if p != "x.md" {
			f.write(t, p, "x")
		}
	}
	testutil.Symlink(t, "id_ed25519", filepath.Join(f.root, "x.md"))
}

func TestHiddenTreeServesOnlyDocumentsAndAssets(t *testing.T) {
	for _, rel := range []string{".ssh", ".claude/plans", "repo/.superpowers/b", "code/app"} {
		t.Run(rel, func(t *testing.T) {
			f := newFixtureAt(t, rel)
			hidden := InHiddenFolder(rel)
			if f.tree.Hidden != hidden {
				t.Fatalf("Hidden = %v, want %v", f.tree.Hidden, hidden)
			}
			writeHiddenFiles(t, f)
			for _, p := range hiddenServed {
				if rec := f.do(t, "GET", p); rec.Code != 200 {
					t.Errorf("%s: %d, want 200", p, rec.Code)
				}
			}
			got := map[string]*httptest.ResponseRecorder{}
			for _, p := range hiddenRefused {
				rec := f.do(t, "GET", p)
				got[p] = rec
				if want := map[bool]int{true: 404, false: 200}[hidden]; rec.Code != want {
					t.Errorf("%s: %d, want %d", p, rec.Code, want)
				}
			}
			if !hidden {
				return
			}
			// Each answer is the one the file gets once it is gone.
			for _, p := range hiddenRefused {
				if err := os.Remove(filepath.Join(f.root, p)); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range hiddenRefused {
				gone := f.do(t, "GET", p)
				if gone.Code != got[p].Code || gone.Body.String() != got[p].Body.String() || gone.Header().Get("Content-Type") != got[p].Header().Get("Content-Type") {
					t.Errorf("%s: %d %q, gone %d %q", p, got[p].Code, got[p].Body.String(), gone.Code, gone.Body.String())
				}
			}
		})
	}
}

func TestTableMarksTreesInHiddenFolders(t *testing.T) {
	tb := NewTable(time.Now)
	for root, want := range map[string]bool{
		"/home/ed/.ssh": true, "/home/ed/.claude/plans": true, "/home/ed/repo/.superpowers/b": true,
		"C:/Users/me/.ssh": true, "/C:/Users/me/.aws": true, `C:\Users\me\.claude\plans`: true,
		"/home/ed/code/app": false, "C:/Users/me/code/app": false,
	} {
		if got := tb.Add("local", root, "c", "a.md"); got.Hidden != want {
			t.Errorf("Add(%q).Hidden = %v, want %v", root, got.Hidden, want)
		}
	}
}
