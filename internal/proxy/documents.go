package proxy

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// documentsPrefix is the path the documents of agents are served below on
// Nearhost's page: /d/<tree id>/<path below the tree's root>.
const documentsPrefix = "/d/"

// HomeDoc is one document listed on Nearhost's page: its path below its
// tree's root, and the URL it is served at.
type HomeDoc struct {
	Path, URL string
}

// HomeTree is one served tree listed on Nearhost's page: the server it is
// on, its root there, and the documents given out in it, in the order they
// are listed.
type HomeTree struct {
	Server, Root string
	Docs         []HomeDoc
}

// homeDocuments is what SetDocuments stored: the handler for paths below
// documentsPrefix, and the list of trees the page shows.
type homeDocuments struct {
	handler http.Handler
	list    func() []HomeTree
}

// documents holds what SetDocuments stored last, nil while nothing is set.
// It is package-level, not a field of Home, because Caddy builds a new Home
// on every config load while the documents outlive them all.
var documents atomic.Pointer[homeDocuments]

// SetDocuments makes Nearhost's page send every request below /d/ to h, and
// list the trees list returns on the page, asked again on each request. A
// nil h answers those requests 404, and a nil list shows none.
// SetDocuments(nil, nil) clears both. It is safe to call at any time,
// including while the page is being served.
func SetDocuments(h http.Handler, list func() []HomeTree) {
	if h == nil && list == nil {
		documents.Store(nil)
		return
	}
	documents.Store(&homeDocuments{handler: h, list: list})
}

// serveDocument sends a request below documentsPrefix to the stored
// handler, unchanged, so it sees the full path and sets every header of its
// response itself. It answers 404 while there is none.
func serveDocument(w http.ResponseWriter, r *http.Request) {
	d := documents.Load()
	if d == nil || d.handler == nil {
		http.NotFound(w, r)
		return
	}
	d.handler.ServeHTTP(w, r)
}

// isDocumentPath reports whether path is below documentsPrefix.
func isDocumentPath(path string) bool {
	return strings.HasPrefix(path, documentsPrefix)
}

// homeTrees returns the trees the page lists now, none while no list is
// stored.
func homeTrees() []HomeTree {
	d := documents.Load()
	if d == nil || d.list == nil {
		return nil
	}
	return d.list()
}
