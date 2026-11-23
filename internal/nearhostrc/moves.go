package nearhostrc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
)

// Moves says, a line each as was → now, what the change from old to next
// takes away from what old serves: a host moved to another port or gone, and
// a route whose port is gone, unless its hosts moved with it. The hosts and
// labels are as the config holds them: the caller makes them safe to show.
func Moves(old, next config.Project) []string {
	var out []string
	for _, r := range old.Routes {
		if r.Role == config.RoleIgnore {
			continue
		}
		moved := false
		for _, h := range r.Hosts {
			if r.Role != config.RoleWeb {
				break
			}
			switch now := webPortOf(next, h); {
			case now == 0:
				out = append(out, fmt.Sprintf("%s port %d → removed", h, r.Port))
			case now != r.Port:
				moved = true
				out = append(out, fmt.Sprintf("%s port %d → %d", h, r.Port, now))
			}
		}
		gone := !slices.ContainsFunc(next.Routes, func(q config.Route) bool { return q.Port == r.Port })
		if gone && !moved {
			what := "port " + strconv.Itoa(r.Port)
			if r.Label != "" {
				what += " (" + r.Label + ")"
			}
			out = append(out, what+" → removed")
		}
	}
	return out
}

// webPortOf is the port of the web route of p that serves host, or 0.
func webPortOf(p config.Project, host string) int {
	for _, r := range p.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			if strings.EqualFold(h, host) {
				return r.Port
			}
		}
	}
	return 0
}
