package documents

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
)

// The Content-Security-Policy values for tree responses. Neither allows
// same-origin, so every page gets an opaque origin. Rendered markdown and
// Nearhost's own pages run no scripts at all, and their raw HTML can't add a
// <base> that sends relative links elsewhere.
const (
	fileCSP     = "sandbox allow-scripts allow-popups allow-forms"
	markdownCSP = "sandbox allow-popups; default-src 'none'; base-uri 'none'; style-src 'unsafe-inline'; img-src 'self' data: https:"
)

// markdownLimit is the largest markdown file that is rendered. Bigger ones
// are served raw.
const markdownLimit = 5 << 20

// textPlain is the type for source files and raw markdown.
const textPlain = "text/plain; charset=utf-8"

// renderMarkdown turns markdown source into an HTML page titled title. This
// stub shows the escaped source in a <pre>; the real renderer replaces it.
var renderMarkdown = func(src []byte, title string) ([]byte, error) {
	return []byte("<!doctype html><meta charset=\"utf-8\"><title>" + html.EscapeString(title) +
		"</title><pre>" + html.EscapeString(string(src)) + "</pre>\n"), nil
}

// handler serves the files of the trees in a table.
type handler struct {
	table *Table
	open  Opener
}

// NewHandler returns a handler serving GET and HEAD on /d/{id}/{path...}
// from the tree with that ID in t, reading through the FS that open returns
// for the tree's server. It parses the full request path itself, so it must
// not be mounted behind a prefix strip.
func NewHandler(t *Table, open Opener) http.Handler {
	return &handler{table: t, open: open}
}

// headWriter drops the body, so HEAD gets the headers GET would.
type headWriter struct{ http.ResponseWriter }

// Write discards b.
func (headWriter) Write(b []byte) (int, error) { return len(b), nil }

// isHead reports whether w answers a HEAD request.
func isHead(w http.ResponseWriter) bool {
	_, ok := w.(headWriter)
	return ok
}

// notFound sends a plain 404 that names nothing.
func notFound(w http.ResponseWriter) {
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// ServeHTTP serves one tree file.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Access-Control-Allow-Origin", "null")
	hd.Set("Content-Security-Policy", markdownCSP)

	switch r.Method {
	case http.MethodGet:
	case http.MethodHead:
		w = headWriter{w}
	default:
		hd.Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, segs, slash, ok := splitPath(r.URL.EscapedPath())
	if !ok {
		notFound(w)
		return
	}
	tr, ok := h.table.Get(id)
	if !ok {
		notFound(w)
		return
	}
	if segs == nil && !slash {
		redirectSlash(w, r, id)
		return
	}
	rel := strings.Join(segs, "/")
	shown := path.Join(tr.Root, rel)
	err := h.serve(w, r, tr, rel, segs, slash)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotConnected):
		notConnectedPage(w, tr.Server)
	case errors.Is(err, fs.ErrNotExist):
		missingPage(w, tr.Server, shown)
	case errors.Is(err, errRefused):
		notFound(w)
	default:
		http.Error(w, "Nearhost couldn't read this file", http.StatusBadGateway)
	}
}

// errRefused marks a path that is a folder without index.html, is not a
// regular file, or resolves outside the root or onto a dot segment. It gets a
// plain 404.
var errRefused = errors.New("refused")

// serve resolves rel below the tree's root and sends it. It writes nothing
// when it returns an error, so the caller can send the right page.
func (h *handler) serve(w http.ResponseWriter, r *http.Request, tr Tree, rel string, segs []string, slash bool) error {
	ctx := r.Context()
	fsys, err := h.open(ctx, tr.Server)
	if err != nil {
		return err
	}
	root, err := fsys.RealPath(ctx, tr.Root)
	if err != nil {
		return err
	}
	real, err := fsys.RealPath(ctx, path.Join(root, rel))
	if err != nil {
		return explain(ctx, fsys, root, rel, err)
	}
	if path.Clean(real) != path.Clean(root) {
		if _, ok := Below(root, real); !ok {
			return errRefused
		}
	}
	info, err := fsys.Stat(ctx, real)
	if err != nil {
		return err
	}
	name := path.Base(rel)
	if info.IsDir() {
		if !slash {
			redirectSlash(w, r, url.PathEscape(segs[len(segs)-1]))
			return nil
		}
		real, err = fsys.RealPath(ctx, path.Join(real, "index.html"))
		if errors.Is(err, ErrNotConnected) {
			return err
		}
		if err != nil {
			return errRefused
		}
		if _, ok := Below(root, real); !ok {
			return errRefused
		}
		if info, err = fsys.Stat(ctx, real); err != nil {
			return err
		}
		name = "index.html"
	} else if slash {
		return errRefused
	}
	if !info.Mode().IsRegular() {
		return errRefused
	}
	f, err := fsys.Open(ctx, real)
	if err != nil {
		return err
	}
	defer f.Close()
	return sendFile(w, r, f, name, info.Size())
}

// explain turns err, from resolving rel below root, into what the request
// may learn. It walks up from rel to the nearest folder that resolves. When
// that folder is outside the root, or on a dot segment, the path went out
// through a symlink, and the result is errRefused whatever err was, so a page
// can't probe what lies outside. Inside the root, a missing file or a path
// through a file (ENOTDIR) is fs.ErrNotExist, and other errors are kept.
func explain(ctx context.Context, fsys FS, root, rel string, err error) error {
	if errors.Is(err, ErrNotConnected) {
		return err
	}
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		real, derr := fsys.RealPath(ctx, path.Join(root, dir))
		if errors.Is(derr, ErrNotConnected) {
			return derr
		}
		if derr != nil {
			continue
		}
		if path.Clean(real) == path.Clean(root) {
			break
		}
		if _, ok := Below(root, real); !ok {
			return errRefused
		}
		break
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return fs.ErrNotExist
	}
	return err
}

// sendFile sends the open file f named name, size bytes long: markdown
// rendered unless ?raw or too big, anything else streamed with its type. For
// HEAD, markdown's type is chosen by size, so the file isn't read.
func sendFile(w http.ResponseWriter, r *http.Request, f io.Reader, name string, size int64) error {
	ext := strings.ToLower(path.Ext(name))
	if ext == ".md" || ext == ".markdown" {
		if r.URL.Query().Has("raw") || (isHead(w) && size > markdownLimit) {
			return stream(w, f, textPlain, nil)
		}
		if isHead(w) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return nil
		}
		head, err := io.ReadAll(io.LimitReader(f, markdownLimit+1))
		if err != nil {
			return err
		}
		if len(head) > markdownLimit {
			return stream(w, f, textPlain, head)
		}
		out, err := renderMarkdown(head, name)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return nil
	}
	ctype := webType(ext)
	var head []byte
	if ctype == "" {
		buf := make([]byte, 512)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		head = buf[:n]
		ctype = "application/octet-stream"
		if strings.HasPrefix(http.DetectContentType(head), "text/") {
			ctype = textPlain
		}
	}
	return stream(w, f, ctype, head)
}

// stream sends head and then the rest of f as ctype, with the files' CSP. A
// HEAD request reads no more of f; an unknown type has had its first 512
// bytes read to sniff it. A failure once the body has begun can't be
// reported, so it is dropped.
func stream(w http.ResponseWriter, f io.Reader, ctype string, head []byte) error {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", fileCSP)
	w.WriteHeader(http.StatusOK)
	if isHead(w) {
		return nil
	}
	_, _ = io.Copy(w, io.MultiReader(bytes.NewReader(head), f))
	return nil
}

// webTypes are the media types served under their own name. Others are
// sniffed, so a browser never downloads or mishandles a source file.
var webTypes = map[string]bool{
	"text/html": true, "text/css": true, "text/javascript": true, "text/plain": true,
	"text/csv": true, "text/xml": true, "application/javascript": true,
	"application/json": true, "application/xml": true, "application/wasm": true,
	"application/pdf": true, "image/svg+xml": true,
}

// sourceExts are extensions of source and config files, served as plain text
// whatever the host's MIME tables say: macOS's maps .ts and .mts to
// video/mp2t, which would open a TypeScript file in a video player.
var sourceExts = map[string]bool{
	".ts": true, ".mts": true, ".cts": true, ".tsx": true, ".jsx": true,
	".go": true, ".rs": true, ".py": true, ".rb": true, ".php": true,
	".java": true, ".kt": true, ".swift": true, ".c": true, ".h": true,
	".cpp": true, ".cs": true, ".sh": true, ".sql": true, ".vue": true,
	".svelte": true, ".toml": true, ".yaml": true, ".yml": true, ".ini": true,
	".txt": true, ".log": true,
}

// typeByExtension looks up an extension's media type in the host's tables.
// Tests replace it to stand in for a host's odd mappings.
var typeByExtension = mime.TypeByExtension

// webType returns the type for ext when it is one browsers handle (a web text
// or document type, or an image, font, audio or video), with charset=utf-8
// added to text types that lack one. Source files are text/plain. Otherwise,
// including for video/mp2t, which some hosts give .ts, it returns "".
func webType(ext string) string {
	if sourceExts[strings.ToLower(ext)] {
		return textPlain
	}
	t := typeByExtension(ext)
	mt, params, err := mime.ParseMediaType(t)
	if t == "" || err != nil || mt == "video/mp2t" {
		return ""
	}
	switch {
	case webTypes[mt]:
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "font/"),
		strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"):
	default:
		return ""
	}
	if strings.HasPrefix(mt, "text/") && params["charset"] == "" {
		params["charset"] = "utf-8"
	}
	return mime.FormatMediaType(mt, params)
}

// redirectSlash sends a folder request to the same folder with a trailing
// slash, so relative links in its index.html resolve inside it. last is the
// escaped final segment of the request path. The "./" prefix keeps a name
// with a colon, such as "https:evil.com", from reading as a scheme.
func redirectSlash(w http.ResponseWriter, r *http.Request, last string) {
	loc := "./" + last + "/"
	if q := r.URL.RawQuery; q != "" {
		loc += "?" + q
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusMovedPermanently)
}

// splitPath parses an escaped /d/{id}/{path...} into the tree ID and the
// decoded segments below the root. slash reports a trailing "/". ok is false
// when the path isn't a tree path, or a segment doesn't decode, is empty in
// the middle, starts with ".", or holds "/", "\" or NUL once decoded, so
// "%2e%2e" and "%2F" can't step out of the root.
func splitPath(escaped string) (id string, segs []string, slash, ok bool) {
	rest, found := strings.CutPrefix(escaped, "/d/")
	if !found {
		return "", nil, false, false
	}
	id, rest, more := strings.Cut(rest, "/")
	if id == "" {
		return "", nil, false, false
	}
	if !more {
		return id, nil, false, true
	}
	if rest == "" {
		return id, nil, true, true
	}
	slash = strings.HasSuffix(rest, "/")
	for _, s := range strings.Split(strings.TrimSuffix(rest, "/"), "/") {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || strings.HasPrefix(d, ".") || strings.ContainsAny(d, "/\\\x00") {
			return "", nil, false, false
		}
		segs = append(segs, d)
	}
	return id, segs, slash, true
}

// Compile-time check that LocalFS is an FS.
var _ FS = LocalFS{}
