package control

import (
	"slices"
	"strings"
	"testing"
)

func registeredPatterns() []string {
	s := &Server{b: newActionFake(), hub: NewHub()}
	s.routes()
	return s.Patterns()
}

// Every registered route is either allowed on the network or denied, never
// both and never neither, and the sets name no route that is not registered.
func TestEveryRouteIsAllowedOrDenied(t *testing.T) {
	reg := registeredPatterns()
	for _, p := range reg {
		a, d := networkAllowed[p], networkDenied[p]
		if a && d {
			t.Errorf("%q is both allowed and denied", p)
		}
		if !a && !d {
			t.Errorf("%q is neither allowed nor denied on the network", p)
		}
	}
	for name, set := range map[string]map[string]bool{"allowed": networkAllowed, "denied": networkDenied} {
		for p := range set {
			if !slices.Contains(reg, p) {
				t.Errorf("%s entry %q is not a registered route", name, p)
			}
		}
	}
}

// The schema marks exactly the allowed operations. Operations marked
// x-nearhost-page are served by the page host, not the control socket, and
// are not control routes.
func TestNetworkMarksMatchTheAllowList(t *testing.T) {
	marked := map[string]bool{}
	for path, item := range loadSchema(t).Paths.Map() {
		for method, op := range item.Operations() {
			if page, _ := op.Extensions["x-nearhost-page"].(bool); page {
				continue
			}
			if net, _ := op.Extensions["x-nearhost-network"].(bool); net {
				marked[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	for p := range marked {
		if !networkAllowed[p] {
			t.Errorf("%q is marked x-nearhost-network but not allowed", p)
		}
	}
	for p := range networkAllowed {
		if !marked[p] {
			t.Errorf("%q is allowed but not marked x-nearhost-network in the schema", p)
		}
	}
}

func TestDeniedRoutesStayDenied(t *testing.T) {
	for _, p := range []string{"POST /v1/stop", "POST /v1/pair", "GET /v1/page", "GET /v1/captured/{id}"} {
		if NetworkAllowed(p) {
			t.Errorf("NetworkAllowed(%q) = true", p)
		}
	}
	if !NetworkAllowed("GET /v1/state") {
		t.Error("NetworkAllowed(GET /v1/state) = false")
	}
}
