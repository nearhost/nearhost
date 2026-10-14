package preset

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
)

// Order sorts presets so that each runs after those it names in After, with
// ties broken by key. A name in After is resolved with Resolve, and one that
// is not among ps is ignored. Presets that wait on each other are left out
// and named in the error.
func Order(ps []*Preset) ([]*Preset, error) {
	byKey := make(map[string]*Preset, len(ps))
	for _, p := range ps {
		byKey[p.Key()] = p
	}
	done := map[string]bool{}
	out := make([]*Preset, 0, len(byKey))
	for len(out) < len(byKey) {
		next := ""
		for key, p := range byKey {
			if done[key] || (next != "" && key > next) {
				continue
			}
			ready := true
			for _, a := range p.After {
				dep := p.Resolve(a)
				if _, present := byKey[dep]; present && !done[dep] {
					ready = false
				}
			}
			if ready {
				next = key
			}
		}
		if next == "" {
			break
		}
		done[next] = true
		out = append(out, byKey[next])
	}
	if len(out) == len(byKey) {
		return out, nil
	}
	var stuck []string
	for key := range byKey {
		if !done[key] {
			stuck = append(stuck, key)
		}
	}
	sort.Strings(stuck)
	return out, fmt.Errorf("presets %s wait on each other through `after`, so none of them ran", strings.Join(stuck, ", "))
}

// Owners maps each route and advisory id to the key of the preset that has
// the last word on it: a later preset replaces an id by declaring it again
// and removes one by suppressing it, whichever registry it came from. ps
// must be in run order.
func Owners(ps []*Preset) map[string]string {
	owners := map[string]string{}
	for _, p := range ps {
		for _, id := range p.Suppress {
			delete(owners, id)
		}
		for _, e := range p.Ports {
			owners[e.Var] = p.Key()
		}
		for _, r := range p.Routes {
			owners[r.ID] = p.Key()
		}
		for _, a := range p.Advisories {
			owners[a.ID] = p.Key()
		}
	}
	return owners
}

// Context is what a preset's route and advisory expressions can see.
type Context struct {
	Project *Project
	Facts   map[string]any            // this preset's facts
	Earlier map[string]map[string]any // facts of active presets that ran before it, by the names this preset uses for them
	Active  func(name string) bool    // whether the preset this preset calls name is active
}

func (c Context) vars() map[string]any {
	vars := c.Project.vars()
	facts, earlier := c.Facts, c.Earlier
	if facts == nil {
		facts = map[string]any{}
	}
	if earlier == nil {
		earlier = map[string]map[string]any{}
	}
	vars["facts"], vars["presets"] = facts, earlier
	return vars
}

func (c Context) host() host {
	return host{file: c.Project.hasFile, pkg: c.Project.hasPackage, active: c.Active}
}

// RouteOut is a route a preset proposes.
type RouteOut struct {
	ID    string
	Port  int
	Label string
	Role  config.Role
	Web   WebHint
}

// EvalRoutes evaluates the preset's routes for which keep returns true. A route
// whose expressions fail is left out and reported. A route whose port is
// not a valid port is left out silently.
func (p *Preset) EvalRoutes(c Context, keep func(id string) bool) ([]RouteOut, []error) {
	env, err := newEnv(scopeRoute, c.host())
	if err != nil {
		return nil, []error{err}
	}
	vars := c.vars()
	var out []RouteOut
	var errs []error
	for _, r := range p.Routes {
		if !keep(r.ID) {
			continue
		}
		if r.When != "" {
			ok, err := evalBool(env, r.When, vars)
			if err != nil {
				errs = append(errs, fmt.Errorf("preset %s: route %s: when: %w", p.Key(), r.ID, err))
				continue
			}
			if !ok {
				continue
			}
		}
		port, err := evalInt(env, r.Port, vars)
		if err != nil {
			errs = append(errs, fmt.Errorf("preset %s: route %s: port: %w", p.Key(), r.ID, err))
			continue
		}
		if port < 1 || port > 65535 {
			continue
		}
		out = append(out, RouteOut{ID: r.ID, Port: port, Label: r.Label, Role: r.Role, Web: r.Web})
	}
	return out, errs
}

// AdviceOut is an advisory a preset gives.
type AdviceOut struct {
	ID      string
	Message string
	Snippet string
}

// EvalAdvice evaluates the preset's advisories for which keep returns true. An
// advisory whose expressions fail is left out and reported.
func (p *Preset) EvalAdvice(c Context, alias string, freePort func(from int) int, keep func(id string) bool) ([]AdviceOut, []error) {
	h := c.host()
	h.freePort = freePort
	env, err := newEnv(scopeAdvisory, h)
	if err != nil {
		return nil, []error{err}
	}
	vars := c.vars()
	vars["alias"] = alias
	var out []AdviceOut
	var errs []error
	for _, a := range p.Advisories {
		if !keep(a.ID) {
			continue
		}
		fail := func(part string, err error) {
			errs = append(errs, fmt.Errorf("preset %s: advisory %s: %s: %w", p.Key(), a.ID, part, err))
		}
		ok, err := evalBool(env, a.When, vars)
		if err != nil {
			fail("when", err)
			continue
		}
		if !ok {
			continue
		}
		message, err := render(env, a.Message, vars)
		if err != nil {
			fail("message", err)
			continue
		}
		snippet, err := render(env, a.Snippet, vars)
		if err != nil {
			fail("snippet", err)
			continue
		}
		out = append(out, AdviceOut{ID: a.ID, Message: strings.TrimSpace(message), Snippet: strings.TrimRight(snippet, "\n")})
	}
	return out, errs
}
