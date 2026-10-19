package doctor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
)

// rcFindings reports what is wrong with the project's .nearhostrc, or how it
// stands against what was approved. It also says whether the file exists, so
// that the drift check can defer to it: a file is authoritative for the
// routes it lists.
func rcFindings(in Input) (findings []Finding, found bool) {
	f, found, err := nearhostrc.Read(in.Files)
	if err != nil {
		var data []byte
		if found {
			// A read error leaves nothing to point at, so the line is
			// worked out only when the bytes can be read again.
			data, _ = in.Files.ReadFile(nearhostrc.FileName)
		}
		return []Finding{rcErrorFinding(data, err)}, found
	}

	c := nearhostrc.Classify(in.Project, f, found, nil)
	switch {
	case !in.Added && (c.Kind != nearhostrc.Problem || !found):
		// Approval happens when the project is added, so nothing is waiting
		// yet; only the file's own errors matter: those reported above, and
		// a file add could not take, below.
	case c.Kind == nearhostrc.Pending:
		names := make([]string, len(c.Items))
		for i, it := range c.Items {
			names[i] = it.Name()
		}
		findings = append(findings, Finding{
			ID:       "nearhostrc.pending",
			Severity: SeverityWarning,
			Message:  ".nearhostrc asks for changes that wait for approval: " + strings.Join(names, ", "),
			File:     nearhostrc.FileName,
			Fix:      Fix{Kind: FixNearhost, Text: "Ask Nearhost to read it again (reload); the user approves it in Nearhost."},
		})
	case c.Kind == nearhostrc.Updated:
		findings = append(findings, Finding{
			ID:       "nearhostrc.not-applied",
			Severity: SeverityWarning,
			Message:  ".nearhostrc has changed since Nearhost last read it",
			File:     nearhostrc.FileName,
			Fix:      Fix{Kind: FixNearhost, Text: "Ask Nearhost to read it again (reload); this change needs no approval."},
		})
	case c.Kind == nearhostrc.Problem && !found:
		findings = append(findings, Finding{
			ID:       "nearhostrc.gone",
			Severity: SeverityWarning,
			Message:  c.Problem,
			File:     nearhostrc.FileName,
			Fix:      Fix{Kind: FixProject, Text: "Restore .nearhostrc; Nearhost serves what was approved until then."},
		})
	case c.Kind == nearhostrc.Problem:
		data, _ := in.Files.ReadFile(nearhostrc.FileName)
		findings = append(findings, rcErrorFinding(data, errors.New(c.Problem)))
	}
	return findings, found
}

// rcErrorFinding turns an error from reading or parsing .nearhostrc into a
// finding. A version newer than this Nearhost can only be fixed by updating
// it; an unset variable and any other mistake are fixed in the project.
func rcErrorFinding(data []byte, err error) Finding {
	fd := Finding{
		Severity: SeverityError,
		Message:  err.Error(),
		File:     nearhostrc.FileName,
		Line:     nearhostrc.Line(data, err),
	}
	var ue *nearhostrc.UnsetError
	var ve *nearhostrc.VersionError
	switch {
	case errors.As(err, &ve):
		fd.ID = "nearhostrc.version"
		fd.Fix = Fix{Kind: FixManual, Text: "Update Nearhost: run `nearhost update`."}
	case errors.As(err, &ue):
		fd.ID = "nearhostrc.unset-variable"
		fd.Fix = Fix{Kind: FixProject, Text: fmt.Sprintf(
			"Set %[1]s in .env.local or .env, or give it a default in .nearhostrc: ${%[1]s:-value}.", ue.Var)}
	default:
		fd.ID = "nearhostrc.invalid"
		fd.Fix = Fix{Kind: FixProject, Text: "Correct .nearhostrc."}
	}
	return fd
}

// driftFindings compares the routes Nearhost serves with the web ports that
// discovery found. It stays quiet when the project has a .nearhostrc, because
// the file is then the authority on its routes, and when discovery didn't run.
func driftFindings(in Input, rcFound bool) []Finding {
	if rcFound || !in.Added || in.Discovered == nil {
		return nil
	}
	var web []discovery.Candidate
	candidate := map[int]bool{}
	for _, c := range in.Discovered.Routes {
		if c.Role == config.RoleWeb {
			web = append(web, c)
			candidate[c.Port] = true
		}
	}
	configured := map[int]bool{}
	for _, r := range in.Project.Routes {
		configured[r.Port] = true
	}

	var out []Finding
	reported := map[int]bool{}
	for _, c := range web {
		if configured[c.Port] || reported[c.Port] {
			continue
		}
		reported[c.Port] = true
		out = append(out, Finding{
			ID:       "routes.unserved-port",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Port %d looks like a web server, but the project has no route for it", c.Port),
			Fix: Fix{Kind: FixNearhost, Text: fmt.Sprintf(
				"Edit the project in Nearhost: select it and press e, then add port %d as a web route.", c.Port)},
		})
	}
	// With no web candidate at all, discovery may simply have found nothing,
	// so a route is not called stale on that evidence.
	if len(web) == 0 {
		return out
	}
	for _, r := range in.Project.Routes {
		if r.Role != config.RoleWeb || candidate[r.Port] {
			continue
		}
		out = append(out, Finding{
			ID:       "routes.stale-port",
			Severity: SeverityInfo,
			Message:  fmt.Sprintf("The web route on port %d matches nothing in the project's files any more", r.Port),
			Fix: Fix{Kind: FixNearhost, Text: fmt.Sprintf(
				"If the port changed, edit the project in Nearhost: select it and press e, then update port %d.", r.Port)},
		})
	}
	return out
}

// adviceFindings turns discovery's advisories and warnings into findings.
func adviceFindings(in Input) []Finding {
	var out []Finding
	for _, a := range in.Advisories {
		out = append(out, Finding{
			ID:       "advice." + a.Code,
			Severity: SeverityWarning,
			Message:  a.Message,
			Fix:      Fix{Kind: FixProject, Text: "Change the project's settings as shown.", Snippet: a.Snippet},
		})
	}
	for _, n := range in.Notes {
		out = append(out, Finding{
			ID:       "presets.note",
			Severity: SeverityInfo,
			Message:  n,
			Fix:      Fix{Kind: FixManual, Text: "Nothing to change in the project; some checks may be incomplete."},
		})
	}
	return out
}
