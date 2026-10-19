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
// the last approved snapshot did not. Hand-added hosts on a port the file no
// longer lists, as when APP_PORT moves, follow that port's approved hosts to
// the file route that now lists any of them, or go to the primary web route
// when none does, so a port move never loses them; they were already served,
// so moving them needs no approval. Routes the file doesn't list are kept
// from discovered (or from p.Routes when discovered is nil) so they stay
// visible, but any that were web are demoted to ignore, since the file is now
// the only thing that says what is served. A demoted route loses its hosts,
// because only web routes may have them. A manual host the file also lists,
// on any route, is dropped from the manual ones so it is served once.
//
// The file's alias becomes p's main name only when it differs from the alias
// the last approved snapshot had, as when the file is first read or someone
// changes it, so a main name the user set by hand since stays until the file
// moves its own. A file without an alias leaves the main name alone.
//
// Merge fails when a route other than the primary lists the project's main
// name, which is always served on the first web route, and when the file asks
// for docker_clients on a project that is not on this machine, since
// containers cannot reach Nearhost on a server. The file's docker_clients
// choice and alias are kept in the snapshot.
func Merge(p config.Project, f *File, discovered []config.Route) (config.Project, error) {
	if f.DockerClients && p.Server != config.LocalServer {
		return p, fmt.Errorf("%s: docker_clients works only for projects on this machine; containers can't reach Nearhost from server %q", FileName, p.Server)
	}
	approvedAlias := ""
	if p.Nearhostrc != nil {
		approvedAlias = p.Nearhostrc.Alias
	}
	if f.Alias != "" && f.Alias != approvedAlias {
		p.Alias = f.Alias
	}
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
				return p, fmt.Errorf("%s: port %d lists %q, the project's main name, which is always served on the first web route", FileName, fr.Port, h)
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

	moved := movedHosts(p, f, approved)

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
			for _, h := range moved[fr.Port] {
				if !inFile[strings.ToLower(h)] && (isPrimary || !strings.EqualFold(h, p.Alias)) {
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
	p.Nearhostrc = &config.RcSnapshot{SHA256: f.SHA256, Routes: snapshot, DockerClients: f.DockerClients, Alias: f.Alias}
	return p, nil
}

// movedHosts finds the hand-added hosts of p's web routes on ports the file
// no longer lists and says which file port each should move to, keyed by
// that port, in p's route order. Only ports the approved snapshot had count:
// a port the file never listed was not moved by it, so its hosts go with the
// route when it is demoted, as before. A port's hosts go to the first file web
// route that lists any host the snapshot approved on the old port, since that
// is where the file moved the port; otherwise they go to the primary web
// route.
func movedHosts(p config.Project, f *File, approved map[int][]string) map[int][]string {
	listed := map[int]bool{}
	primary := 0
	for _, fr := range f.Routes {
		listed[fr.Port] = true
		if fr.Role == config.RoleWeb && primary == 0 {
			primary = fr.Port
		}
	}
	moved := map[int][]string{}
	for _, r := range p.Routes {
		if _, inSnapshot := approved[r.Port]; !inSnapshot || listed[r.Port] || r.Role != config.RoleWeb {
			continue
		}
		dest := primary
	find:
		for _, fr := range f.Routes {
			if fr.Role != config.RoleWeb {
				continue
			}
			for _, h := range approved[r.Port] {
				if containsFold(fr.Hosts, h) {
					dest = fr.Port
					break find
				}
			}
		}
		for _, h := range r.Hosts {
			if !containsFold(approved[r.Port], h) {
				moved[dest] = append(moved[dest], h)
			}
		}
	}
	return moved
}

func containsFold(list []string, h string) bool {
	for _, x := range list {
		if strings.EqualFold(x, h) {
			return true
		}
	}
	return false
}

// OptionDockerClients is the Item.Option for the docker_clients opt-in.
const OptionDockerClients = "docker_clients"

// Item is one host, or one option, that needs the user's approval before it
// takes effect. An option item has an empty Host, Port 0 and its name in
// Option.
type Item struct {
	Port int
	Host string
	// Why says what makes the host risky: "a real domain", "a wildcard" or
	// "localhost".
	Why string
	// Option names the option being approved, or is empty for a host.
	Option string
}

// Name is what the item is about: its host, or its option when it has no host.
func (it Item) Name() string {
	if it.Host == "" {
		return it.Option
	}
	return it.Host
}

// NeedsApproval lists every host on a web route of next that is risky and
// wasn't on a web route of old. Hosts ending in .test can only resolve on
// this machine, so they are safe; a real domain, a wildcard or localhost
// would take over traffic the user may want to reach elsewhere. Turning on
// docker_clients is listed too, after the hosts, since it opens the project to
// containers. Pass the zero Project as old for a new project. The hosts are
// sorted by port, then host.
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
	// A main name that is a real domain takes over traffic meant for the
	// internet as a route host does, so it is asked about the same way, on
	// the port of the primary web route it is served at.
	if alias := next.Alias; alias != "" && !strings.HasSuffix(strings.ToLower(alias), ".test") && !strings.EqualFold(alias, old.Alias) {
		if web, ok := next.WebRoute(); ok {
			items = append(items, Item{Port: web.Port, Host: alias, Why: "a real domain"})
		}
	}
	if next.DockerClients() && !old.DockerClients() {
		items = append(items, Item{Option: OptionDockerClients, Why: "lets local Docker containers reach this project"})
	}
	return items
}
