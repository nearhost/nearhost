package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
)

// Result is everything discovery learned about a project.
type Result struct {
	Routes     []Candidate
	Env        map[string]string
	HasLaravel bool
	HasVite    bool
	Sail       bool
	Vite       ViteInfo
}

// Discover reads the project's files and proposes routes.
func Discover(r FileReader) (*Result, error) {
	res := &Result{}
	env, err := readEnv(r)
	if err != nil {
		return nil, err
	}
	res.Env = env

	composeData, hasCompose, err := readCompose(r)
	if err != nil {
		return nil, err
	}
	var composePorts []ComposePort
	if hasCompose {
		composePorts, err = ParseCompose(composeData, env)
		if err != nil {
			return nil, err
		}
		res.Sail = IsSail(composeData)
	}

	viteData, viteFound, err := readVite(r)
	if err != nil {
		return nil, err
	}
	if viteFound {
		res.Vite = ScanVite(viteData)
	}
	res.HasVite = viteFound || hasVitePackage(r)

	if _, err := r.ReadFile("artisan"); err == nil {
		res.HasLaravel = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read artisan: %w", err)
	}

	// ParseCompose returns ports in map order; sort so "first wins" is stable.
	sort.Slice(composePorts, func(i, j int) bool {
		a, b := composePorts[i], composePorts[j]
		if a.Published != b.Published {
			return a.Published < b.Published
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Var < b.Var
	})

	byPort := map[int]Candidate{}
	for _, p := range composePorts {
		c := Candidate{Port: p.Published, Target: p.Target, Role: config.RoleIgnore, Label: p.Service, Source: "compose:" + p.Service, Var: p.Var}
		if sv, known := sailVarByName(p.Var); known {
			c.Label, c.Role = sv.Label, sv.Role
		} else if p.Var != "" {
			c.Label = fmt.Sprintf("%s (%s)", p.Service, p.Var)
		}
		if _, dup := byPort[c.Port]; !dup {
			byPort[c.Port] = c
		}
	}
	for _, c := range envCandidates(env) {
		if _, dup := byPort[c.Port]; !dup {
			byPort[c.Port] = c
		}
	}
	if !res.Sail && res.Vite.Port > 0 {
		if _, dup := byPort[res.Vite.Port]; !dup {
			byPort[res.Vite.Port] = Candidate{Port: res.Vite.Port, Role: config.RoleIgnore, Label: "Vite (vite.config)", Source: "vite"}
		}
	}

	routes := make([]Candidate, 0, len(byPort))
	for _, c := range byPort {
		routes = append(routes, c)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Port < routes[j].Port })
	res.Routes = assignWeb(routes, res)
	return res, nil
}

// assignWeb picks exactly one web route when a sensible default exists.
func assignWeb(routes []Candidate, res *Result) []Candidate {
	for i := range routes {
		if routes[i].Role == config.RoleWeb {
			routes[i].Role = config.RoleIgnore
		}
	}
	pick := -1
	for i, c := range routes {
		if c.Var == "APP_PORT" {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i, c := range routes {
			if c.Target == 80 || (c.Target == 0 && c.Port == 80) {
				pick = i
				break
			}
		}
	}
	if pick < 0 && res.Vite.Port > 0 {
		for i, c := range routes {
			if c.Port == res.Vite.Port {
				pick = i
				break
			}
		}
	}
	if pick < 0 && len(routes) == 1 {
		pick = 0
	}
	if pick >= 0 {
		routes[pick].Role = config.RoleWeb
	}
	return routes
}

// ConfigRoutes converts candidates to config routes.
func (res *Result) ConfigRoutes() []config.Route {
	out := make([]config.Route, 0, len(res.Routes))
	for _, c := range res.Routes {
		out = append(out, config.Route{Port: c.Port, Role: c.Role, Label: c.Label})
	}
	return out
}

// Advisory is a suggestion shown to the user and never applied.
type Advisory struct {
	Code    string
	Message string
	Snippet string
}

// Advise reports configuration the user may want to change on the project.
func Advise(res *Result, alias string, others []config.Project) []Advisory {
	var advs []Advisory
	taken := map[int]bool{}
	for _, c := range res.Routes {
		if c.Role == config.RoleLoopback {
			taken[c.Port] = true
		}
	}
	for _, o := range others {
		if !o.Enabled {
			continue
		}
		for _, r := range o.Routes {
			if r.Role == config.RoleLoopback {
				taken[r.Port] = true
			}
		}
	}

	for _, c := range res.Routes {
		if c.Role != config.RoleLoopback {
			continue
		}
		for _, o := range others {
			if !o.Enabled {
				continue
			}
			for _, r := range o.Routes {
				if r.Role == config.RoleLoopback && r.Port == c.Port {
					advs = append(advs, Advisory{
						Code:    "loopback-clash",
						Message: fmt.Sprintf("Port %d is also forwarded to this machine by project %q. Only one can bind it. Give this project its own port.", c.Port, o.Name),
						Snippet: fmt.Sprintf("VITE_PORT=%d", suggestPort(taken)),
					})
				}
			}
		}
	}

	if res.HasVite {
		if res.Sail && strings.TrimSpace(res.Env["VITE_PORT"]) == "" {
			advs = append(advs, Advisory{
				Code:    "vite-default-port",
				Message: "Vite will use the default port 5173, which every other Sail project also uses. Set VITE_PORT in .env so projects can run side by side.",
				Snippet: fmt.Sprintf("VITE_PORT=%d", suggestPort(taken)),
			})
		}
		if !res.Sail && !res.Vite.HTTPS && !res.Vite.AllowAllHosts && !hostAllowed(alias, res.Vite.AllowedHosts) {
			advs = append(advs, Advisory{
				Code:    "allowed-hosts",
				Message: fmt.Sprintf("Vite rejects requests for %s unless it is in server.allowedHosts.", alias),
				Snippet: fmt.Sprintf("// vite.config.ts\nexport default defineConfig({\n  server: {\n    allowedHosts: ['%s'],\n  },\n})", alias),
			})
		}
	}

	if res.HasLaravel {
		want := "https://" + alias
		if strings.TrimSpace(res.Env["APP_URL"]) != want {
			advs = append(advs, Advisory{
				Code:    "app-url",
				Message: fmt.Sprintf("APP_URL is %q. Generated links will use it, so set it to the alias.", res.Env["APP_URL"]),
				Snippet: "APP_URL=" + want,
			})
		}
	}
	return advs
}

func hostAllowed(alias string, allowed []string) bool {
	for _, a := range allowed {
		if a == alias {
			return true
		}
		if strings.HasPrefix(a, ".") && (strings.HasSuffix(alias, a) || alias == strings.TrimPrefix(a, ".")) {
			return true
		}
	}
	return false
}

// suggestPort returns the lowest port from 5173 upward that is not taken.
func suggestPort(taken map[int]bool) int {
	p := 5173
	for taken[p] {
		p++
	}
	return p
}
