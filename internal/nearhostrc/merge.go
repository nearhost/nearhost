package nearhostrc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
)

// Merge returns p with the file's routes applied. The file's routes come
// first, in file order, so its first web route becomes the primary one. Hosts
// the user added by hand to a port the file still lists are kept after the
// file's own; a host counts as added by hand when p had it on that port but
// the last approved snapshot did not. Routes the file doesn't list are kept
// from discovered (or from p.Routes when discovered is nil) so they stay
// visible, but any that were web are demoted to ignore, since the file is now
// the only thing that says what is served. A demoted route loses its hosts,
// because only web routes may have them. A manual host the file also lists,
// on any route, is dropped from the manual ones so it is served once. Merge
// fails when a route other than the primary lists the project's .test name,
// which is always served on the first web route.
func Merge(p config.Project, f *File, discovered []config.Route) (config.Project, error) {
	inFile := map[string]bool{}
	primary := true
	for _, fr := range f.Routes {
		isPrimary := false
		if fr.Role == config.RoleWeb && primary {
			primary, isPrimary = false, true
		}
		for _, h := range fr.Hosts {
			inFile[strings.ToLower(h)] = true
			if !isPrimary && strings.EqualFold(h, p.Alias) {
				return p, fmt.Errorf("%s: port %d lists %q, the project's .test name, which is always served on the first web route", FileName, fr.Port, h)
			}
		}
	}

	approved := map[int][]string{}
	if p.Nearhostrc != nil {
		for _, r := range p.Nearhostrc.Routes {
			approved[r.Port] = r.Hosts
		}
	}
	current := map[int][]string{}
	for _, r := range p.Routes {
		current[r.Port] = r.Hosts
	}

	routes := make([]config.Route, 0, len(f.Routes))
	snapshot := make([]config.Route, 0, len(f.Routes))
	listed := map[int]bool{}
	firstWeb := true
	for _, fr := range f.Routes {
		listed[fr.Port] = true
		isPrimary := fr.Role == config.RoleWeb && firstWeb
		if isPrimary {
			firstWeb = false
		}
		r := config.Route{Port: fr.Port, Role: fr.Role, Label: fr.Label}
		if fr.Role == config.RoleWeb {
			seen := map[string]bool{}
			add := func(h string) {
				if k := strings.ToLower(h); !seen[k] {
					seen[k] = true
					r.Hosts = append(r.Hosts, h)
				}
			}
			for _, h := range fr.Hosts {
				add(h)
			}
			for _, h := range current[fr.Port] {
				if !containsFold(approved[fr.Port], h) && !inFile[strings.ToLower(h)] && (isPrimary || !strings.EqualFold(h, p.Alias)) {
					add(h)
				}
			}
		}
		routes = append(routes, r)
		snapshot = append(snapshot, config.Route{Port: fr.Port, Role: fr.Role, Label: fr.Label, Hosts: append([]string(nil), fr.Hosts...)})
	}

	rest := discovered
	if rest == nil {
		rest = p.Routes
	}
	for _, r := range rest {
		if listed[r.Port] {
			continue
		}
		r.Hosts = nil
		if r.Role == config.RoleWeb {
			r.Role = config.RoleIgnore
		}
		routes = append(routes, r)
	}

	p.Routes = routes
	p.Nearhostrc = &config.RcSnapshot{SHA256: f.SHA256, Routes: snapshot}
	return p, nil
}

func containsFold(list []string, h string) bool {
	for _, x := range list {
		if strings.EqualFold(x, h) {
			return true
		}
	}
	return false
}

// Item is one host that needs the user's approval before it is served.
type Item struct {
	Port int
	Host string
	// Why says what makes the host risky: "a real domain", "a wildcard" or
	// "localhost".
	Why string
}

// NeedsApproval lists every host on a web route of next that is risky and
// wasn't on a web route of old. Hosts ending in .test can only resolve on
// this machine, so they are safe; a real domain, a wildcard or localhost
// would take over traffic the user may want to reach elsewhere. Pass the zero
// Project as old for a new project. The result is sorted by port, then host.
func NeedsApproval(old, next config.Project) []Item {
	had := map[string]bool{}
	for _, r := range old.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			had[strings.ToLower(h)] = true
		}
	}
	var items []Item
	for _, r := range next.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			// A pattern is risky even under .test: it catches every name
			// below it, not just one the user chose.
			pattern := hostname.IsPattern(h)
			if (!pattern && strings.HasSuffix(strings.ToLower(h), ".test")) || had[strings.ToLower(h)] {
				continue
			}
			why := "a real domain"
			switch {
			case pattern:
				why = "a wildcard"
			case strings.EqualFold(h, "localhost"):
				why = "localhost"
			}
			items = append(items, Item{Port: r.Port, Host: h, Why: why})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Port != items[j].Port {
			return items[i].Port < items[j].Port
		}
		return items[i].Host < items[j].Host
	})
	return items
}
