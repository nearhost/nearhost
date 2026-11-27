package control

import (
	"net/http"
	"path"
	"strings"
)

// networkAllowed holds the control routes that reach the network, as
// Patterns gives them. Every other route is served on the control socket
// alone. The schema marks these operations x-nearhost-network.
var networkAllowed = map[string]bool{
	"GET /v1":                                    true,
	"GET /v1/status":                             true,
	"GET /v1/state":                              true,
	"GET /v1/events":                             true,
	"GET /v1/openapi.yaml":                       true,
	"POST /v1/projects/{name}/start":             true,
	"POST /v1/projects/{name}/stop":              true,
	"POST /v1/projects/{name}/recheck":           true,
	"POST /v1/projects/{name}/change/accept":     true,
	"POST /v1/projects/{name}/change/decline":    true,
	"POST /v1/servers/{name}/connect":            true,
	"POST /v1/servers/{name}/disconnect":         true,
	"POST /v1/servers/{name}/reconnect":          true,
	"POST /v1/proposals/{id}/accept":             true,
	"POST /v1/proposals/{id}/decline":            true,
	"POST /v1/passthroughs/requests/{id}/answer": true,
	"POST /v1/passthroughs/{id}/close":           true,
	"POST /v1/documents/requests/{id}/answer":    true,
	"POST /v1/documents/{id}/remove":             true,
	"GET /v1/devices":                            true,
	"DELETE /v1/devices/{name}":                  true,
}

// networkDenied holds the control routes that never reach the network. A
// route added later is in neither set until someone lists it, and a test
// fails until then.
var networkDenied = map[string]bool{
	"POST /v1/stop":                     true, // would let a device stop the core
	"POST /v1/hosts/changed":            true, // internal signal from the hosts file watcher
	"POST /v1/config/reload":            true, // local administration
	"POST /v1/grants/revoke":            true, // local administration
	"GET /v1/captured/{id}":             true, // holds captured request bodies
	"POST /v1/captured/{id}/replay":     true, // sends captured requests on
	"GET /v1/environment":               true, // reveals the machine's environment
	"POST /v1/pair":                     true, // pairing must start on the machine
	"GET /v1/pair/{code}":               true, // pairing must start on the machine
	"POST /v1/network/firewall/recheck": true, // local network administration
	"GET /v1/page":                      true, // returns the page's URL, carrying the page secret
	"POST /v1/documents/{id}/open":      true, // opens a browser on the machine
}

// NetworkAllowed reports whether the control route pattern (as Patterns
// gives it, such as "GET /v1/state") may be served on the network.
func NetworkAllowed(pattern string) bool { return networkAllowed[pattern] }

// networkPrefix is where the network handler serves the control routes: a
// route at /v1/state is served at /api/v1/state.
const networkPrefix = "/api"

// NetworkHandler serves the control routes NetworkAllowed allows under
// /api, with the same handlers as the socket and the server's hub, so a
// stream opened through it counts in Clients. Every other path, including a
// denied route's, answers as an unknown path does: 404, with the control
// API's problem body.
func (s *Server) NetworkHandler() http.Handler { return s.network }

// networkGate serves routes, the network's routes at their control paths,
// for paths at or under /api/v1 that are already clean, with /api stripped.
// A path that is not clean is refused rather than redirected: the mux's
// redirect would drop the prefix, and could name a route the client was not
// let reach.
func networkGate(routes http.Handler) http.Handler {
	strip := http.StripPrefix(networkPrefix, routes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.EscapedPath()
		under := p == networkPrefix+"/v1" || strings.HasPrefix(p, networkPrefix+"/v1/")
		if !under || !isClean(p) {
			notFound(w, r.URL.Path)
			return
		}
		strip.ServeHTTP(w, r)
	})
}

// isClean reports whether the escaped path p is as the mux cleans it: no
// empty, "." or ".." segment, and a trailing slash kept.
func isClean(p string) bool {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c == p
}
