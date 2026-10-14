package discovery

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

// Offer is a preset that is not enabled and could be, with the expression
// that says whether it suits a project.
type Offer struct {
	Name   string
	Title  string
	Detect string
}

// Options say which presets discovery may use.
type Options struct {
	Presets []*preset.Preset // enabled presets
	Offers  []Offer          // presets to suggest when they match
}

// Result is everything discovery learned about a project.
type Result struct {
	Routes    []Candidate
	Env       map[string]string
	Applied   []string // presets that ran, in order
	Suggested []Offer  // offers that match the project
	Warnings  []string // presets, or parts of them, that were skipped

	project *preset.Project
	files   *memo // the reader under project, which Advise reads through too
	active  []*preset.Preset
	facts   map[string]map[string]any
	owners  map[string]string
}

// Discover reads the project's files and proposes routes. On its own it
// knows .env files and Compose published ports; presets supply the rest.
func Discover(r FileReader, opts Options) (*Result, error) {
	files := newMemo(r)
	env, err := readEnv(files)
	if err != nil {
		return nil, err
	}
	composeData, hasCompose, err := readCompose(files)
	if err != nil {
		return nil, err
	}
	var composePorts []ComposePort
	if hasCompose {
		composePorts, err = ParseCompose(composeData, env)
		if err != nil {
			return nil, err
		}
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

	res := &Result{
		Env:     env,
		project: &preset.Project{Env: env, Compose: string(composeData), Files: files},
		files:   files,
		facts:   map[string]map[string]any{},
	}
	byPort := map[int]*Candidate{}
	for _, p := range composePorts {
		if _, dup := byPort[p.Published]; dup {
			continue
		}
		c := &Candidate{Port: p.Published, Target: p.Target, Role: config.RoleIgnore, Label: p.Service, Source: "compose:" + p.Service, Var: p.Var}
		if p.Var != "" {
			c.Label = fmt.Sprintf("%s (%s)", p.Service, p.Var)
		}
		byPort[c.Port] = c
	}

	res.activate(opts.Presets)
	for _, p := range res.active {
		res.apply(p, byPort)
	}
	// Offers are not enabled presets: a read error they hit must not fail the run.
	enabledErr := files.err
	for _, o := range opts.Offers {
		// An offer whose expression fails is simply not suggested.
		if ok, err := preset.DetectExpr(o.Detect, res.project); err == nil && ok {
			res.Suggested = append(res.Suggested, o)
		}
	}
	files.err = enabledErr
	if files.err != nil {
		return nil, files.err
	}

	routes := make([]Candidate, 0, len(byPort))
	for _, c := range byPort {
		routes = append(routes, *c)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Port < routes[j].Port })
	res.Routes = assignWeb(routes)
	return res, nil
}

func (res *Result) warn(format string, a ...any) {
	res.Warnings = append(res.Warnings, fmt.Sprintf(format, a...))
}

// activate works out which presets apply to the project and in what order.
func (res *Result) activate(presets []*preset.Preset) {
	var matched []*preset.Preset
	for _, p := range presets {
		ok, err := p.Detected(res.project)
		if err != nil {
			res.warn("preset %s: detect: %v", p.Name, err)
			continue
		}
		if ok {
			matched = append(matched, p)
		}
	}
	active, err := preset.Order(matched)
	if err != nil {
		res.warn("%v", err)
	}
	res.active = active
	res.owners = preset.Owners(active)
	for _, p := range active {
		res.Applied = append(res.Applied, p.Name)
	}
}

func (res *Result) isActive(name string) bool {
	for _, p := range res.active {
		if p.Name == name {
			return true
		}
	}
	return false
}

// context is what p's expressions can see: its own facts and those of the
// active presets that ran before it.
func (res *Result) context(p *preset.Preset) preset.Context {
	earlier := map[string]map[string]any{}
	for _, a := range res.active {
		if a == p {
			break
		}
		earlier[a.Name] = res.facts[a.Name]
	}
	return preset.Context{Project: res.project, Facts: res.facts[p.Name], Earlier: earlier, Active: res.isActive}
}

// apply lets one preset label, add and hint at routes.
func (res *Result) apply(p *preset.Preset, byPort map[int]*Candidate) {
	keep := func(id string) bool { return res.owners[id] == p.Name }
	for _, e := range p.Ports {
		if !keep(e.Var) {
			continue
		}
		label := fmt.Sprintf("%s (%s)", e.Label, e.Var)
		for _, c := range byPort {
			if c.Var == e.Var && strings.HasPrefix(c.Source, "compose:") {
				c.Label, c.Role, c.web, c.Preset = label, e.Role, e.Web, p.Name
			}
		}
		port, err := strconv.Atoi(strings.TrimSpace(res.Env[e.Var]))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if _, claimed := byPort[port]; !claimed {
			byPort[port] = &Candidate{Port: port, Role: e.Role, Label: label, Source: "env:" + e.Var, Var: e.Var, Preset: p.Name, web: e.Web}
		}
	}

	res.facts[p.Name] = p.EvalFacts(res.project)
	routes, errs := p.EvalRoutes(res.context(p), keep)
	for _, err := range errs {
		res.warn("%v", err)
	}
	for _, r := range routes {
		if c, claimed := byPort[r.Port]; claimed {
			if c.web == preset.WebNone {
				c.web = r.Web
			}
			continue
		}
		byPort[r.Port] = &Candidate{Port: r.Port, Role: r.Role, Label: r.Label, Source: "preset:" + p.Name, Preset: p.Name, web: r.Web}
	}
}

// assignWeb picks one web route when a sensible default exists. routes must
// be sorted by port. Presets only hint; the choice is made here.
func assignWeb(routes []Candidate) []Candidate {
	first := func(match func(Candidate) bool) int {
		for i, c := range routes {
			if match(c) {
				return i
			}
		}
		return -1
	}
	pick := first(func(c Candidate) bool { return c.web == preset.WebPreferred })
	if pick < 0 {
		pick = first(func(c Candidate) bool { return c.Target == 80 || (c.Target == 0 && c.Port == 80) })
	}
	if pick < 0 {
		pick = first(func(c Candidate) bool { return c.web == preset.WebFallback && c.Role != config.RoleLoopback })
	}
	if pick < 0 && len(routes) == 1 && routes[0].Preset == "" {
		// A lone port is the web app only when nothing says what it is.
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
