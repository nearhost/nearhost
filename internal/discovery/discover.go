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
	Applied   []string // keys of the presets that ran, in order
	Suggested []Offer  // offers that match the project
	Warnings  []string // presets, or parts of them, that were skipped

	project    *preset.Project
	hasCompose bool
	files      *memo // the reader under project, which Advise reads through too
	active     []*preset.Preset
	facts      map[string]map[string]any // by preset key
	owners     map[string]string         // id to preset key
}

// Discover reads the project's files and proposes routes. On its own it
// knows the port variables in .env. It reads Compose published ports only
// when an active preset asks for them; presets supply the rest.
func Discover(r FileReader, opts Options) (*Result, error) {
	files := newMemo(r)
	env, err := ReadEnv(files)
	if err != nil {
		return nil, err
	}
	composeData, hasCompose, err := readCompose(files)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Env:        env,
		project:    &preset.Project{Env: env, Compose: string(composeData), Files: files},
		files:      files,
		hasCompose: hasCompose,
		facts:      map[string]map[string]any{},
	}
	res.activate(opts.Presets)
	byPort := map[int]*Candidate{}
	if hasCompose && res.wantsCompose() {
		if byPort, err = composeCandidates(composeData, env); err != nil {
			return nil, err
		}
	}
	for _, p := range res.active {
		res.apply(p, byPort)
	}
	// The core's own .env rule comes last, for ports nothing else claimed.
	for _, c := range envPorts(env) {
		if _, claimed := byPort[c.Port]; !claimed {
			byPort[c.Port] = &c
		}
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

// wantsCompose reports whether an active preset asks for the Compose
// file's published ports.
func (res *Result) wantsCompose() bool {
	for _, p := range res.active {
		if p.Compose == preset.ComposePorts {
			return true
		}
	}
	return false
}

// composeCandidates proposes a candidate for each port the Compose file
// publishes, the first service in a stable order winning a shared port.
func composeCandidates(data []byte, env map[string]string) (map[int]*Candidate, error) {
	composePorts, err := ParseCompose(data, env)
	if err != nil {
		return nil, err
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
	return byPort, nil
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
			res.warn("preset %s: detect: %v", p.Key(), err)
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
		res.Applied = append(res.Applied, p.Key())
	}
}

func (res *Result) isActive(key string) bool {
	for _, p := range res.active {
		if p.Key() == key {
			return true
		}
	}
	return false
}

// context is what p's expressions can see: its own facts and those of the
// active presets that ran before it, under the names p uses for them.
func (res *Result) context(p *preset.Preset) preset.Context {
	earlier := map[string]map[string]any{}
	for _, a := range res.active {
		if a == p {
			break
		}
		if name, ok := p.NameOf(a); ok {
			earlier[name] = res.facts[a.Key()]
		}
	}
	active := func(name string) bool { return res.isActive(p.Resolve(name)) }
	return preset.Context{Project: res.project, Facts: res.facts[p.Key()], Earlier: earlier, Active: active}
}

// apply lets one preset label, add and hint at routes.
func (res *Result) apply(p *preset.Preset, byPort map[int]*Candidate) {
	keep := func(id string) bool { return res.owners[id] == p.Key() }
	for _, e := range p.Ports {
		if !keep(e.Var) {
			continue
		}
		label := fmt.Sprintf("%s (%s)", e.Label, e.Var)
		for _, c := range byPort {
			if c.Var == e.Var && strings.HasPrefix(c.Source, "compose:") {
				c.Label, c.Role, c.web, c.Preset = label, e.Role, e.Web, p.Key()
			}
		}
		port, err := strconv.Atoi(strings.TrimSpace(res.Env[e.Var]))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if _, claimed := byPort[port]; !claimed {
			byPort[port] = &Candidate{Port: port, Role: e.Role, Label: label, Source: "env:" + e.Var, Var: e.Var, Preset: p.Key(), web: e.Web}
		}
	}

	res.facts[p.Key()] = p.EvalFacts(res.project)
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
		byPort[r.Port] = &Candidate{Port: r.Port, Role: r.Role, Label: r.Label, Source: "preset:" + p.Key(), Preset: p.Key(), web: r.Web}
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
		// Only a Compose candidate knows its container port.
		pick = first(func(c Candidate) bool {
			return strings.HasPrefix(c.Source, "compose:") && (c.Target == 80 || (c.Target == 0 && c.Port == 80))
		})
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

// ComposeUnread reports whether the project has a Compose file that no
// active preset asked to read for published ports.
func (res *Result) ComposeUnread() bool {
	return res.hasCompose && !res.wantsCompose()
}
