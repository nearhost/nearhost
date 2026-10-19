package discovery

import (
	"fmt"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
)

// Advisory is a suggestion shown to the user and never applied.
type Advisory struct {
	Code    string
	Message string
	Snippet string
}

// Advise reports configuration the user may want to change on the project.
// hosts is every name the project will be served at, its alias first.
// The second value lists advisories that could not be worked out.
func Advise(res *Result, alias string, hosts []string, others []config.Project) ([]Advisory, []string) {
	// A project's hosts hold the alias only once one of its routes is web,
	// but advisories are promised it first whatever the routes are.
	if len(hosts) == 0 || !strings.EqualFold(hosts[0], alias) {
		hosts = append([]string{alias}, hosts...)
	}
	taken := takenLoopbacks(res, others)
	advs := clashAdvisories(res, others, taken)
	free := func(from int) int { return freeFrom(taken, from) }
	var warnings []string
	// Advisories may read files discovery did not. A file that could not be
	// read looks absent to them, so say so rather than stay quiet.
	if res.files != nil {
		defer func(before error) { res.files.err = before }(res.files.err)
		res.files.err = nil
	}
	for _, p := range res.active {
		keep := func(id string) bool { return res.owners[id] == p.Key() }
		out, errs := p.EvalAdvice(res.context(p), alias, hosts, free, keep)
		for _, err := range errs {
			warnings = append(warnings, err.Error())
		}
		for _, a := range out {
			advs = append(advs, Advisory{Code: a.ID, Message: a.Message, Snippet: a.Snippet})
		}
	}
	if res.files != nil && res.files.err != nil {
		// The memo's error reads "read <file>: <cause>".
		warnings = append(warnings, fmt.Sprintf("advisories could not %v", res.files.err))
	}
	return advs, warnings
}

// takenLoopbacks is every loopback port this project or an enabled project
// would bind on this machine.
func takenLoopbacks(res *Result, others []config.Project) map[int]bool {
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
	return taken
}

// freeFrom returns the lowest port from n upward that is not taken.
func freeFrom(taken map[int]bool, n int) int {
	for taken[n] {
		n++
	}
	return n
}

// clashAdvisories reports loopback ports another enabled project forwards.
func clashAdvisories(res *Result, others []config.Project, taken map[int]bool) []Advisory {
	var advs []Advisory
	for _, c := range res.Routes {
		if c.Role != config.RoleLoopback {
			continue
		}
		for _, o := range others {
			if !o.Enabled {
				continue
			}
			for _, r := range o.Routes {
				if r.Role != config.RoleLoopback || r.Port != c.Port {
					continue
				}
				adv := Advisory{
					Code:    "loopback-clash",
					Message: fmt.Sprintf("Port %d is also forwarded to this machine by project %q. Only one can bind it. Give this project its own port.", c.Port, o.Name),
				}
				if c.Var != "" {
					adv.Snippet = fmt.Sprintf("%s=%d", c.Var, freeFrom(taken, c.Port+1))
				}
				advs = append(advs, adv)
			}
		}
	}
	return advs
}
