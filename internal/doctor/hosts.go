package doctor

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hostname"
)

// answer is what a lookup gave for one name.
type answer struct {
	addrs []netip.Addr
	err   error
}

// loopback reports whether the answer holds addresses and all are loopback,
// which means DNS already sends the name to this machine.
func (a answer) loopback() bool {
	if a.err != nil || len(a.addrs) == 0 {
		return false
	}
	for _, ad := range a.addrs {
		if !ad.IsLoopback() {
			return false
		}
	}
	return true
}

// publicAddr returns the first non-loopback address of the answer.
func (a answer) publicAddr() (netip.Addr, bool) {
	if a.err != nil {
		return netip.Addr{}, false
	}
	for _, ad := range a.addrs {
		if !ad.IsLoopback() {
			return ad, true
		}
	}
	return netip.Addr{}, false
}

// lookupAll asks lookup for every name at once, as hosts.Wanted does, so that
// a slow resolver costs one timeout rather than one per name.
func lookupAll(ctx context.Context, lookup func(context.Context, string) ([]netip.Addr, error), names []string) map[string]answer {
	out := make(map[string]answer, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs, err := lookup(ctx, n)
			mu.Lock()
			out[n] = answer{addrs, err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// exactNames lists the names the project needs resolved: its main name when
// that is a valid one, and every exact web host. localhost is left out, as
// the proxy serves it without DNS. Names are lowercased and listed once.
func exactNames(p config.Project) []string {
	var out []string
	add := func(n string) {
		n = strings.ToLower(n)
		if n != "localhost" && !hostname.IsPattern(n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if config.ValidProjectAlias(p.Alias) == nil {
		add(p.Alias)
	}
	for _, r := range p.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			add(h)
		}
	}
	return out
}

// patternsOf lists the lowercased wildcard patterns of the project's web
// routes, once each.
func patternsOf(p config.Project) []string {
	var out []string
	for _, r := range p.Routes {
		if r.Role != config.RoleWeb {
			continue
		}
		for _, h := range r.Hosts {
			h = strings.ToLower(h)
			if hostname.IsPattern(h) && !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// sampleName turns a pattern into a name it matches, so that DNS can be
// asked whether the pattern resolves.
func sampleName(pattern string) string {
	labels := strings.Split(pattern, ".")
	for i, l := range labels {
		if l == "*" {
			labels[i] = "nearhost-check"
		}
	}
	return strings.Join(labels, ".")
}

// hostFindings checks that the names the project is served at reach this
// machine, that its patterns do not shadow another project's, and that the
// dev server is up.
func hostFindings(ctx context.Context, in Input) []Finding {
	var out []Finding
	out = append(out, dnsFindings(ctx, in)...)
	out = append(out, overlapFindings(in)...)
	out = append(out, listeningFindings(ctx, in)...)
	return out
}

func dnsFindings(ctx context.Context, in Input) []Finding {
	if in.Lookup == nil {
		return nil
	}
	p := in.Project
	needsEntry := func(n string) bool {
		return in.Added && p.Enabled && in.ManagesHosts &&
			!slices.ContainsFunc(in.Block, func(b string) bool { return strings.EqualFold(b, n) })
	}

	// Ask only for what a check will use, so that the names of a project
	// that is not added are asked for no more than hosts.public needs.
	names := exactNames(p)
	var ask []string
	for _, n := range names {
		if needsEntry(n) || !strings.HasSuffix(n, ".test") {
			ask = append(ask, n)
		}
	}
	patterns := patternsOf(p)
	for _, pat := range patterns {
		ask = append(ask, sampleName(pat))
	}
	got := lookupAll(ctx, in.Lookup, ask)

	var out []Finding
	for _, n := range names {
		a, asked := got[n]
		if !asked {
			continue
		}
		if needsEntry(n) && !a.loopback() {
			out = append(out, Finding{
				ID:       "hosts.unresolved",
				Severity: SeverityError,
				Message:  n + " does not resolve to this machine, and is not in the hosts file",
				Fix:      Fix{Kind: FixNearhost, Text: "Start the project in Nearhost; it adds " + n + " to /etc/hosts."},
				Host:     n,
			})
		}
		if ad, ok := a.publicAddr(); ok && !strings.HasSuffix(n, ".test") {
			out = append(out, Finding{
				ID:       "hosts.public",
				Severity: SeverityWarning,
				Message: fmt.Sprintf("%s is a real name that resolves to %s; while the project is enabled, the real site is out of reach on this machine",
					n, ad),
				Fix: Fix{Kind: FixManual, Text: "Nothing to do if that is what you want; otherwise use a name that resolves to 127.0.0.1, or a .test name."},
			})
		}
	}
	for _, pat := range patterns {
		a := got[sampleName(pat)]
		if a.err != nil || a.loopback() {
			continue
		}
		out = append(out, Finding{
			ID:       "hosts.unresolved-pattern",
			Severity: SeverityError,
			Message:  pat + " does not resolve to this machine",
			Fix: Fix{Kind: FixManual, Text: "Add a DNS record for " + pat + " pointing at 127.0.0.1 (the hosts file can't hold wildcards), " +
				"or list the names you need as exact hosts."},
		})
	}
	return out
}

// overlapFindings reports a pattern of this project that overlaps, without
// being equal to, a pattern of another enabled project. Equal patterns are a
// clash the config already refuses.
func overlapFindings(in Input) []Finding {
	if !in.Project.Enabled {
		return nil
	}
	var out []Finding
	for _, mine := range patternsOf(in.Project) {
		for _, o := range in.Others {
			if !o.Enabled || o.Name == in.Project.Name {
				continue
			}
			for _, theirs := range patternsOf(o) {
				if mine == theirs || !hostname.Overlap(mine, theirs) {
					continue
				}
				winner, owner := mine, in.Project.Name
				if hostname.Less(theirs, mine) {
					winner, owner = theirs, o.Name
				}
				out = append(out, Finding{
					ID:       "hosts.overlap",
					Severity: SeverityWarning,
					Message: fmt.Sprintf("%s overlaps %s of %s; a name both match goes to the more specific, %s of %s",
						mine, theirs, o.Name, winner, owner),
					Fix: Fix{Kind: FixProject, Text: "If that is not what you want, make the patterns distinct."},
				})
			}
		}
	}
	return out
}

// listeningFindings reports web ports nothing listens on. It is a warning
// when the engine runs the project, since the site is then broken, and only
// info otherwise, since the dev server may simply not be started yet.
func listeningFindings(ctx context.Context, in Input) []Finding {
	p := in.Project
	if !in.Added || !p.Enabled || p.Server != config.LocalServer || in.Listening == nil {
		return nil
	}
	sev := SeverityInfo
	if in.Running {
		sev = SeverityWarning
	}
	var out []Finding
	for _, w := range p.Web() {
		if in.Listening(ctx, w.Port) {
			continue
		}
		hs := slices.DeleteFunc(slices.Clone(w.Hosts), func(h string) bool { return h == "" })
		msg := fmt.Sprintf("nothing listens on port %d", w.Port)
		switch {
		case len(hs) > 0:
			msg += ", which serves " + strings.Join(hs, ", ")
		case p.Alias != "":
			// A route without hosts of its own is the project's all the same.
			msg += ", which serves " + p.Alias
		}
		out = append(out, Finding{
			ID:       "routes.not-listening",
			Severity: sev,
			Message:  msg,
			Fix:      Fix{Kind: FixManual, Text: fmt.Sprintf("Start the project's dev server on port %d, or correct the port.", w.Port)},
		})
	}
	return out
}
