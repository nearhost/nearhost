// Package doctor checks a project's setup and reports what is wrong with it
// as findings. It is pure: everything the checks look at comes in through
// Input, so the CLI and the agent API can run the same checks.
package doctor

import (
	"context"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hosts"
)

// Severity says how much a finding matters.
type Severity string

const (
	// SeverityError is something that is broken.
	SeverityError Severity = "error"
	// SeverityWarning is something that probably isn't what the user wants.
	SeverityWarning Severity = "warning"
	// SeverityInfo is worth knowing and needs no action.
	SeverityInfo Severity = "info"
)

// FixKind says who can make a fix, and where.
type FixKind string

const (
	// FixProject is an edit to the project's own files.
	FixProject FixKind = "project"
	// FixNearhost needs Nearhost to read a file again, or the user to approve
	// or change something in Nearhost.
	FixNearhost FixKind = "nearhost"
	// FixManual is something only the user can do, such as a DNS record.
	FixManual FixKind = "manual"
)

// Fix is how to put a finding right.
type Fix struct {
	Kind    FixKind `json:"kind"`
	Text    string  `json:"text"`
	Snippet string  `json:"snippet,omitempty"`
}

// Finding is one thing a check found. Its ID is stable API: agents and
// skills match on it.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Project  string   `json:"project"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Fix      Fix      `json:"fix"`

	// Host is the name a hosts.unresolved finding is about, so that
	// nearhost doctor can leave out one its hosts block check already
	// names. It stays out of the JSON, whose Message names it.
	Host string `json:"-"`
}

// Input is everything the checks of one project look at.
type Input struct {
	Project    config.Project       // as configured, or as it would be added when !Added
	Added      bool                 // Project is in the config
	Files      discovery.FileReader // the project's directory
	Others     []config.Project     // every other configured project
	Discovered *discovery.Result    // nil when discovery failed or did not run
	Advisories []discovery.Advisory
	Notes      []string // discovery's and advice's warnings

	Lookup       hosts.Lookup                             // nil skips the DNS checks
	Block        []string                                 // the names in the hosts block
	ManagesHosts bool                                     // false: hosts not managed, or the block unreadable
	Running      bool                                     // the engine runs the project
	Listening    func(ctx context.Context, port int) bool // nil skips the port check
	CACert       string                                   // absolute path of the root certificate; "" skips docker.ca
	GOOS         string

	// Environ is the environment Compose would see from the shell, nil when
	// unknown. Compose fills in variables from it over .env's, and the CA
	// fix tells developers to set NEARHOST_CA_CERT in their shell profile.
	Environ map[string]string
}

// Check runs every check on one project. It never returns nil, so that the
// findings encode as an empty JSON array rather than null, and it orders them
// worst first so that the output reads the same on every run.
func Check(ctx context.Context, in Input) []Finding {
	out := []Finding{}
	rc, found := rcFindings(in)
	out = append(out, rc...)
	out = append(out, driftFindings(in, found)...)
	out = append(out, adviceFindings(in)...)
	out = append(out, hostFindings(ctx, in)...)
	out = append(out, dockerFindings(in)...)

	if in.Added {
		for i := range out {
			out[i].Project = in.Project.Name
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Message < b.Message
	})
	return out
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	}
	return 2
}

// SetcapFix is the command that lets the binary at exe bind ports below 1024.
// It names the binary itself rather than `command -v nearhost`, which for an
// npm install is the Node launcher: a capability on a script does nothing.
// Only when exe is unknown ("") does it fall back to `command -v nearhost`,
// which may then be that launcher.
func SetcapFix(exe string) string {
	if exe == "" {
		return `sudo setcap cap_net_bind_service=+ep "$(command -v nearhost)"`
	}
	return "sudo setcap cap_net_bind_service=+ep '" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
}
