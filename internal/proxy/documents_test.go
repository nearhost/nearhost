package proxy

import (
	"net/http"
	"testing"
)

func TestHomeServesDocuments(t *testing.T) {
	t.Cleanup(func() { SetDocuments(nil, nil) })
	SetDocuments(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/d/abc/x.md" {
			t.Errorf("handler got path %q, want the full /d/abc/x.md", r.URL.Path)
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
		w.Write([]byte("doc"))
	}), nil)
	w := serveHome(t, "1.2.3", "GET", "/d/abc/x.md")
	if w.Code != 200 || w.Body.String() != "doc" {
		t.Fatalf("GET /d/abc/x.md: status %d, body %q, want 200 doc", w.Code, w.Body.String())
	}
	// The documents handler sets its own headers; the home page's must not
	// reach its responses or replace them.
	if csp := w.Header().Values("Content-Security-Policy"); len(csp) != 1 || csp[0] != "sandbox allow-scripts" {
		t.Errorf("a document's Content-Security-Policy = %q, want only its own", csp)
	}
	if w := serveHome(t, "1.2.3", "GET", "/other"); w.Code != 404 {
		t.Errorf("GET /other: status %d, want 404", w.Code)
	}

	SetDocuments(nil, nil)
	if w := serveHome(t, "1.2.3", "GET", "/d/abc/x.md"); w.Code != 404 {
		t.Errorf("GET /d/abc/x.md with no handler: status %d, want 404", w.Code)
	}
}
