package report

import (
	"slices"
	"strings"
)

// Code names a kind of problem. It is a contract with the web interface,
// scripts, agents and the errors reference page: once released, a code is
// never renamed or used for something else.
//
// A code is <area>.<what>, lowercase, with hyphens: "port.in-use". The
// general codes Unexpected, Crash and Interrupted are single words.
// Nearhost doctor's finding IDs are codes, unchanged.
//
// Every code is a constant in this file with an entry in the catalog. Code
// elsewhere uses the constants, or AdviceCode for a preset's advice; a test
// fails if it makes a Code from a string.
type Code string

// Entry describes a code in the catalog.
type Entry struct {
	Code Code
	// Level is the level the problem usually has. A message carries its own.
	Level Level
	// Status is the HTTP status of an error with this code in an API
	// response; 0 for a code that is never one.
	Status int
	// Summary says in one line what the code means.
	Summary string
}

// DocsBase is the errors reference page; a code's entry is its anchor.
const DocsBase = "https://nearhost.dev/docs/reference/errors/"

// The general codes.
const (
	Unexpected  Code = "unexpected"
	Crash       Code = "crash"
	Interrupted Code = "interrupted"
)

// Codes of commands, the core and the platform.
const (
	CLIUsage                 Code = "cli.usage"
	PortNeedsPermission      Code = "port.needs-permission"
	PortInUse                Code = "port.in-use"
	CoreAlreadyRunning       Code = "core.already-running"
	CoreNotRunning           Code = "core.not-running"
	CoreStoppedWhileStarting Code = "core.stopped-while-starting"
	CoreStillStarting        Code = "core.still-starting"
	CoreLost                 Code = "core.lost"
	CoreAPIMismatch          Code = "core.api-mismatch"
	CoreOtherConfig          Code = "core.other-config"
	HostsNeedsUpdate         Code = "hosts.needs-update"
	HostsReadOnly            Code = "hosts.read-only"
	SSHUnreachable           Code = "ssh.unreachable"
	SSHAuthFailed            Code = "ssh.auth-failed"
	SSHSocketPathTooLong     Code = "ssh.socket-path-too-long"
	AgentsNotReady           Code = "agents.not-ready"
	AgentsRefused            Code = "agents.refused"
	AgentsPathTooLong        Code = "agents.path-too-long"
	ConfigInvalid            Code = "config.invalid"
	ConfigUnknownKeys        Code = "config.unknown-keys"
	ServiceUnsupported       Code = "service.unsupported"
	ServiceNoUserManager     Code = "service.no-user-manager"
	ServiceCopy              Code = "service.copy"
	ServiceAsRoot            Code = "service.as-root"
	ServiceCannotBind        Code = "service.cannot-bind"
	UpdateCheckFailed        Code = "update.check-failed"
	UpdateAvailable          Code = "update.available"
	InterfaceNoTerminal      Code = "interface.no-terminal"
)

// Nearhost doctor's finding IDs, as released.
const (
	NearhostrcPending       Code = "nearhostrc.pending"
	NearhostrcNotApplied    Code = "nearhostrc.not-applied"
	NearhostrcGone          Code = "nearhostrc.gone"
	NearhostrcVersion       Code = "nearhostrc.version"
	NearhostrcUnsetVariable Code = "nearhostrc.unset-variable"
	NearhostrcInvalid       Code = "nearhostrc.invalid"
	RoutesUnservedPort      Code = "routes.unserved-port"
	RoutesStalePort         Code = "routes.stale-port"
	RoutesUnservedHost      Code = "routes.unserved-host"
	RoutesNotListening      Code = "routes.not-listening"
	PresetsNote             Code = "presets.note"
	HostsDNSUnchecked       Code = "hosts.dns-unchecked"
	HostsUnresolved         Code = "hosts.unresolved"
	HostsPublic             Code = "hosts.public"
	HostsUnresolvedPattern  Code = "hosts.unresolved-pattern"
	HostsOverlap            Code = "hosts.overlap"
	DockerComposeDepth      Code = "docker.compose-depth"
	DockerHostGateway       Code = "docker.host-gateway"
	DockerComposeParse      Code = "docker.compose-parse"
	DockerComposeFile       Code = "docker.compose-file"
	DockerClients           Code = "docker.clients"
	DockerCA                Code = "docker.ca"
	ProjectsUnchecked       Code = "projects.unchecked"
)

// adviceFamily is the prefix of the one dynamic family: a preset's advice,
// whose IDs come from the presets.
const adviceFamily = "advice."

var catalog = []Entry{
	{Unexpected, Error, 500, "Something failed that Nearhost has no explanation for; the message holds the error as it came."},
	{Crash, Error, 500, "Nearhost stopped unexpectedly; the details are in a crash file."},
	{Interrupted, Error, 500, "The command was interrupted before it finished."},

	{CLIUsage, Error, 400, "A command was given arguments or flags it does not take."},
	{PortNeedsPermission, Error, 403, "Nearhost may not listen on a port below 1024, such as 443 or 80, without permission."},
	{PortInUse, Error, 409, "Another program is already listening on a port Nearhost needs."},
	{CoreAlreadyRunning, Error, 409, "Nearhost is already running for this config."},
	{CoreNotRunning, Error, 503, "Nearhost is not running."},
	{CoreStoppedWhileStarting, Error, 503, "Nearhost stopped while it was starting in the background."},
	{CoreStillStarting, Error, 503, "Nearhost is still starting in the background."},
	{CoreLost, Error, 503, "The connection to the running Nearhost was lost."},
	{CoreAPIMismatch, Error, 409, "The running Nearhost is a different version that this copy cannot talk to."},
	{CoreOtherConfig, Error, 409, "The running Nearhost uses a different config file."},
	{HostsNeedsUpdate, Warning, 409, "The hosts file does not yet list the names Nearhost serves."},
	{HostsReadOnly, Error, 403, "The hosts file is read-only, so Nearhost cannot update it."},
	{SSHUnreachable, Error, 503, "A server cannot be reached over SSH."},
	{SSHAuthFailed, Error, 403, "SSH refused to log in to a server."},
	{SSHSocketPathTooLong, Error, 400, "The path of an SSH socket is longer than the system allows."},
	{AgentsNotReady, Error, 503, "Coding agents on a server cannot reach Nearhost yet."},
	{AgentsRefused, Error, 403, "The server's SSH daemon refused to forward the socket coding agents use."},
	{AgentsPathTooLong, Error, 400, "The socket coding agents use would have a path longer than the server allows."},
	{ConfigInvalid, Error, 400, "The config file has a value Nearhost cannot use."},
	{ConfigUnknownKeys, Error, 400, "The config file has keys Nearhost does not know, so saving it would lose them."},
	{ServiceUnsupported, Error, 501, "The Nearhost service is not supported on this system."},
	{ServiceNoUserManager, Error, 503, "There is no user service manager to run the Nearhost service."},
	{ServiceCopy, Error, 409, "This copy of nearhost cannot be run by a service later, such as a one-run copy."},
	{ServiceAsRoot, Error, 403, "The service must be installed as the user who runs it, not as root."},
	{ServiceCannotBind, Error, 409, "The service cannot run this copy of nearhost, as it lacks permission to use ports 443 and 80."},
	{UpdateCheckFailed, Error, 503, "Nearhost could not find out whether a newer release exists."},
	{UpdateAvailable, Info, 0, "A newer release of Nearhost is available."},
	{InterfaceNoTerminal, Error, 400, "The interface needs a terminal, and there is none."},

	{NearhostrcPending, Warning, 0, "The project's .nearhostrc has changes that wait for approval."},
	{NearhostrcNotApplied, Warning, 0, "The project's .nearhostrc has changed since Nearhost last read it."},
	{NearhostrcGone, Warning, 0, "The project's .nearhostrc is gone or cannot be read, and Nearhost still serves what it last said."},
	{NearhostrcVersion, Error, 409, "The project's .nearhostrc needs a newer Nearhost."},
	{NearhostrcUnsetVariable, Error, 400, "The project's .nearhostrc uses a variable that is not set."},
	{NearhostrcInvalid, Error, 400, "The project's .nearhostrc cannot be read."},
	{RoutesUnservedPort, Warning, 0, "A port in the project's files looks like a web server, but the project has no route for it."},
	{RoutesStalePort, Info, 0, "A web route's port matches nothing in the project's files any more."},
	{RoutesUnservedHost, Warning, 0, "The project's settings name a host that Nearhost does not serve."},
	{RoutesNotListening, Warning, 0, "Nothing listens on a port Nearhost serves for the project."},
	{PresetsNote, Info, 0, "A note from the preset that matched the project."},
	{HostsDNSUnchecked, Info, 0, "Nearhost could not ask DNS directly, so it did not check whether real-domain names resolve elsewhere."},
	{HostsUnresolved, Error, 409, "A name the project is served at does not resolve to this machine and is not in the hosts file."},
	{HostsPublic, Warning, 0, "A name the project is served at is a real name, so the real site is out of reach while the project is enabled."},
	{HostsUnresolvedPattern, Error, 409, "A pattern the project is served at does not resolve to this machine."},
	{HostsOverlap, Warning, 0, "Two host patterns overlap; a name both match goes to the more specific."},
	{DockerComposeDepth, Warning, 0, "The Compose file nests or repeats merges too much to check."},
	{DockerHostGateway, Warning, 0, "A container calls a name that resolves to itself or to the internet, not to Nearhost."},
	{DockerComposeParse, Warning, 0, "The Compose file is not valid YAML, so its services were not checked."},
	{DockerComposeFile, Info, 0, "COMPOSE_FILE is set, and Nearhost checks only the Compose files Compose reads by default."},
	{DockerClients, Warning, 0, "A container calls a name Nearhost serves, and Nearhost does not accept requests from Docker's network."},
	{DockerCA, Warning, 0, "A container calls a name Nearhost serves and does not trust Nearhost's root certificate."},
	{ProjectsUnchecked, Info, 0, "Nearhost could not run the project's checks, for example because its server is not reachable."},
}

var byCode = func() map[Code]Entry {
	m := make(map[Code]Entry, len(catalog))
	for _, e := range catalog {
		m[e.Code] = e
	}
	return m
}()

// Catalog returns every code with a fixed name, in the order of this file.
// The advice family (AdviceCode) is not listed.
func Catalog() []Entry { return slices.Clone(catalog) }

// Lookup returns a code's catalog entry. A code of the advice family is
// found too, as a warning.
func Lookup(c Code) (Entry, bool) {
	if e, ok := byCode[c]; ok {
		return e, true
	}
	if id, ok := strings.CutPrefix(string(c), adviceFamily); ok && id != "" {
		return Entry{Code: c, Level: Warning, Summary: "Advice from the preset that matched the project."}, true
	}
	return Entry{}, false
}

// AdviceCode returns the code of a preset's advice with the given ID, as
// nearhost doctor reports it: "advice.<id>".
func AdviceCode(id string) Code { return Code(adviceFamily + id) }
