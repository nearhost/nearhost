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
	// Steps are what to do, for a code whose fix never varies; nil for
	// one whose fix depends on the case. The errors reference page shows
	// them.
	Steps []Step
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
	CLIUsage                   Code = "cli.usage"
	PortNeedsPermission        Code = "port.needs-permission"
	PortInUse                  Code = "port.in-use"
	CoreAlreadyRunning         Code = "core.already-running"
	CoreNotRunning             Code = "core.not-running"
	CoreStoppedWhileStarting   Code = "core.stopped-while-starting"
	CoreStillStarting          Code = "core.still-starting"
	CoreLost                   Code = "core.lost"
	CoreAPIMismatch            Code = "core.api-mismatch"
	CoreOtherConfig            Code = "core.other-config"
	HostsNeedsUpdate           Code = "hosts.needs-update"
	HostsReadOnly              Code = "hosts.read-only"
	SSHUnreachable             Code = "ssh.unreachable"
	SSHAuthFailed              Code = "ssh.auth-failed"
	SSHSocketPathTooLong       Code = "ssh.socket-path-too-long"
	SSHConfigConflict          Code = "ssh.config-conflict"
	SSHStaleMaster             Code = "ssh.stale-master"
	ForwardFailed              Code = "forward.failed"
	ForwardReconnecting        Code = "forward.reconnecting"
	DockerUnreachable          Code = "docker.unreachable"
	DockerNetworksFailed       Code = "docker.networks-failed"
	PassthroughClosed          Code = "passthrough.closed"
	AgentsNotReady             Code = "agents.not-ready"
	AgentsRefused              Code = "agents.refused"
	AgentsPathTooLong          Code = "agents.path-too-long"
	ConfigInvalid              Code = "config.invalid"
	ConfigUnknownKeys          Code = "config.unknown-keys"
	CAMoveFailed               Code = "ca.move-failed"
	ServiceUnsupported         Code = "service.unsupported"
	ServiceNoUserManager       Code = "service.no-user-manager"
	ServiceCopy                Code = "service.copy"
	ServiceAsRoot              Code = "service.as-root"
	ServiceCannotBind          Code = "service.cannot-bind"
	UpdateCheckFailed          Code = "update.check-failed"
	UpdateAvailable            Code = "update.available"
	InterfaceNoTerminal        Code = "interface.no-terminal"
	CoreNothingToServe         Code = "core.nothing-to-serve"
	CoreStopTimeout            Code = "core.stop-timeout"
	CoreOutdated               Code = "core.outdated"
	CoreNoReload               Code = "core.no-reload"
	CoreLogUnavailable         Code = "core.log-unavailable"
	CoreNoProjects             Code = "core.no-projects"
	CoreUnknownProject         Code = "core.unknown-project"
	CoreNoEnabledProjects      Code = "core.no-enabled-projects"
	CANotPrivate               Code = "ca.not-private"
	TrustNoSudo                Code = "trust.no-sudo"
	TrustNotRemoved            Code = "trust.not-removed"
	TrustRemains               Code = "trust.remains"
	ServiceStartFailed         Code = "service.start-failed"
	ServiceOwnSettings         Code = "service.own-settings"
	HostsHelperRefused         Code = "hosts.helper-refused"
	UpdateFailed               Code = "update.failed"
	DiscoveryIncomplete        Code = "discovery.incomplete"
	DoctorChecksFailed         Code = "doctor.checks-failed"
	InterfaceFlagsIgnored      Code = "interface.flags-ignored"
	InterfacePasswordCancelled Code = "interface.password-cancelled"
	InterfacePromptDeclined    Code = "interface.prompt-declined"
	InterfaceLogMissed         Code = "interface.log-missed"
	InterfaceOpenFailed        Code = "interface.open-failed"
	InterfaceCopyFailed        Code = "interface.copy-failed"
	InterfaceNothingToCopy     Code = "interface.nothing-to-copy"
	InterfaceAddressTooLong    Code = "interface.address-too-long"
	InterfaceAnswerNotSaved    Code = "interface.answer-not-saved"
	InterfaceChangeIgnored     Code = "interface.change-ignored"
	ServiceUnreadable          Code = "service.unreadable"
	ServiceManagerFailed       Code = "service.manager-failed"
	ServiceNodeVersion         Code = "service.node-version"
	ServiceAgentUnset          Code = "service.ssh-agent-unset"
	ServiceAgentChanges        Code = "service.ssh-agent-changes"
	AddLonePort                Code = "add.lone-port"
	AddNoWebPort               Code = "add.no-web-port"
	AddBadName                 Code = "add.bad-name"
	AddAliasTaken              Code = "add.alias-taken"
	AddNameTaken               Code = "add.name-taken"
	AddNoDirectory             Code = "add.no-directory"
	AddServerChanged           Code = "add.server-changed"
	AddUnknownServer           Code = "add.unknown-server"
	AddNeedsApproval           Code = "add.needs-approval"
	AddHostTaken               Code = "add.host-taken"
	AddBadConfig               Code = "add.bad-config"
	AddRelativePath            Code = "add.relative-path"
	AddBadAlias                Code = "add.bad-alias"
	PresetRegistryUnreadable   Code = "preset.registry-unreadable"
	PresetNotUpdated           Code = "preset.not-updated"
	PresetNotEnabled           Code = "preset.not-enabled"
	PresetUnknown              Code = "preset.unknown"
	PresetLocalFile            Code = "preset.local-file"
	PresetRegistryUnknown      Code = "preset.registry-unknown"
	PresetRegistryNotTrusted   Code = "preset.registry-not-trusted"
	PresetUpdatesFailed        Code = "preset.updates-failed"
	PresetRegistryInUse        Code = "preset.registry-in-use"
	ExportExists               Code = "export.exists"
	ExportChanged              Code = "export.changed"
	ExportNoProject            Code = "export.no-project"
	ExportReplacedPending      Code = "export.replaced-pending"
	HostsCoreNotTold           Code = "hosts.core-not-told"
	DocumentsOpenFailed        Code = "documents.open-failed"
	AgentsUnavailable          Code = "agents.unavailable"
	AgentsApprovalWaiting      Code = "agents.approval-waiting"
	AgentsGrantsPruned         Code = "agents.grants-pruned"
	AgentsGrantsOff            Code = "agents.grants-off"
	AgentsAllowNotSaved        Code = "agents.allow-not-saved"
	ControlNoRoute             Code = "control.no-route"
	ControlMethodNotAllowed    Code = "control.method-not-allowed"
	ControlBadRequest          Code = "control.bad-request"
	ControlNotFound            Code = "control.not-found"
	ControlUnimplemented       Code = "control.unimplemented"
	ControlRequestEnded        Code = "control.request-ended"
	CoreNoDesktop              Code = "core.no-desktop"
	CoreSessionUnchecked       Code = "core.session-unchecked"
	CoreSSHAgentUnset          Code = "core.ssh-agent-unset"
	CoreSSHAgentUnreachable    Code = "core.ssh-agent-unreachable"
	CoreDockerMissing          Code = "core.docker-missing"
	CoreDockerUnreachable      Code = "core.docker-unreachable"
	CoreRunDirFailed           Code = "core.run-dir-failed"
	PageRenderFailed           Code = "page.render-failed"
	PageRequestEnded           Code = "page.request-ended"
	PageProposalDeclineOnly    Code = "page.proposal-decline-only"
	PageAnswerNotOffered       Code = "page.answer-not-offered"
	PageChangeAnswerInvalid    Code = "page.change-answer-invalid"
	PageNotOpened              Code = "page.not-opened"
	PageAnswerFailed           Code = "page.answer-failed"
	PageChangeChanged          Code = "page.change-changed"
	ProxyAppNotAnswering       Code = "proxy.app-not-answering"
	ProxyForwardReconnecting   Code = "proxy.forward-reconnecting"
	ProxyLocked                Code = "proxy.locked"
	ProxyNotFound              Code = "proxy.not-found"
	ProxyMethodNotAllowed      Code = "proxy.method-not-allowed"
	ProxyForbidden             Code = "proxy.forbidden"
	PairCodeInvalid            Code = "pair.code-invalid"
	PairLinkUsed               Code = "pair.link-used"
	PairBadTarget              Code = "pair.bad-target"
	PairTooManyTries           Code = "pair.too-many-tries"
	PairNeeded                 Code = "pair.needed"
	PairUnavailable            Code = "pair.unavailable"
	NetworkApplyFailed         Code = "network.apply-failed"
	NetworkDevicesUnavailable  Code = "network.devices-unavailable"
	NetworkOff                 Code = "network.off"
	NetworkNoToken             Code = "network.no-token"
	NetworkDNS                 Code = "network.dns"
	NetworkFirewall            Code = "network.firewall"
	NetworkFirewallRefused     Code = "network.firewall-refused"
	NetworkCert                Code = "network.cert"
	NetworkNoPage              Code = "network.no-page"
	NetworkNotListening        Code = "network.not-listening"
	DeviceUnknown              Code = "devices.unknown"
	DocumentsMissing           Code = "documents.missing"
	DocumentsChanging          Code = "documents.changing"
	DocumentsNotConnected      Code = "documents.not-connected"
	DocumentsNotFound          Code = "documents.not-found"
	DocumentsMethodNotAllowed  Code = "documents.method-not-allowed"
	DocumentsUnreadable        Code = "documents.unreadable"
	AgentNoSuchCall            Code = "agent.no-such-call"
	AgentNoToken               Code = "agent.no-token"
	AgentRefused               Code = "agent.refused"
	AgentNoProject             Code = "agent.no-project"
	AgentBadPath               Code = "agent.bad-path"
	AgentBadRequest            Code = "agent.bad-request"
	AgentDoctorBusy            Code = "agent.doctor-busy"
	AgentNotADirectory         Code = "agent.not-a-directory"
	AgentDoctorFailed          Code = "agent.doctor-failed"
	AgentNoReviewer            Code = "agent.no-reviewer"
	AgentTooManyWaiting        Code = "agent.too-many-waiting"
	AgentReloadFailed          Code = "agent.reload-failed"
	AgentNearhostrcUnread      Code = "agent.nearhostrc-unread"
	AgentRequestGone           Code = "agent.request-gone"
	AgentLocalPassthrough      Code = "agent.local-passthrough"
	AgentGrantsFailed          Code = "agent.grants-failed"
	AgentTokenRevoked          Code = "agent.token-revoked"
	AgentTooManyPassthroughs   Code = "agent.too-many-passthroughs"
	AgentNotConnected          Code = "agent.not-connected"
	AgentPassthroughFailed     Code = "agent.passthrough-failed"
	AgentClosedWhileOpening    Code = "agent.closed-while-opening"
	AgentPortHeld              Code = "agent.port-held"
	AgentDocumentNotThere      Code = "agent.document-not-there"
	AgentNotADocument          Code = "agent.not-a-document"
	AgentDocumentFailed        Code = "agent.document-failed"
	AgentNoPage                Code = "agent.no-page"
)

// Codes of nearhost doctor's checks that predict no error of their own.
const (
	DoctorNotChecked        Code = "doctor.not-checked"
	CoreOtherVersion        Code = "core.other-version"
	ServiceNotInstalled     Code = "service.not-installed"
	ServiceOtherCopy        Code = "service.other-copy"
	ServiceLogFolderMissing Code = "service.log-folder-missing"
	ServiceNotRunning       Code = "service.not-running"
	ServiceTakesOver        Code = "service.takes-over"
	ConfigNoHome            Code = "config.no-home"
	ConfigMissing           Code = "config.missing"
	ConfigBadAlias          Code = "config.bad-alias"
	ConfigPatternConflict   Code = "config.pattern-conflict"
	ConfigLoopbackOwnPort   Code = "config.loopback-own-port"
	CANotCreated            Code = "ca.not-created"
	CAUnreadable            Code = "ca.unreadable"
	CANotTrusted            Code = "ca.not-trusted"
	CAOldCopy               Code = "ca.old-copy"
	CABrowsersNotTrusted    Code = "ca.browsers-not-trusted"
	CAFirefoxRootsOff       Code = "ca.firefox-roots-off"
	HostsUnreadable         Code = "hosts.unreadable"
	SSHAgentMissing         Code = "ssh.agent-missing"
	SSHAgentStopped         Code = "ssh.agent-stopped"
	SSHNotFound             Code = "ssh.not-found"
	SSHTooOld               Code = "ssh.too-old"
	SSHVersionUnknown       Code = "ssh.version-unknown"
	SSHOtherVendor          Code = "ssh.other-vendor"
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

// catalogRow is an entry as this file lists it; the steps are in
// fixedSteps.
type catalogRow struct {
	Code    Code
	Level   Level
	Status  int
	Summary string
}

var catalogRows = []catalogRow{
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
	{SSHConfigConflict, Error, 409, "The user's SSH configuration for a server sets options Nearhost cannot keep out of its own connections."},
	{SSHStaleMaster, Warning, 0, "An SSH connection left by an earlier run could not be ended."},
	{ForwardFailed, Error, 500, "A port could not be forwarded to or from a server."},
	{ForwardReconnecting, Warning, 0, "A forwarded port is being connected again, and serves nothing until it is."},
	{DockerUnreachable, Warning, 0, "Docker did not answer, so requests from containers are not accepted yet."},
	{DockerNetworksFailed, Warning, 0, "Nearhost could not start accepting requests from new Docker networks."},
	{PassthroughClosed, Warning, 0, "Nearhost closed a port it had opened for a coding agent, as the server or the port went away."},
	{AgentsNotReady, Error, 503, "Coding agents on a server cannot reach Nearhost yet."},
	{AgentsRefused, Error, 403, "The server's SSH daemon refused to forward the socket coding agents use."},
	{AgentsPathTooLong, Error, 400, "The socket coding agents use would have a path longer than the server allows."},
	{ConfigInvalid, Error, 400, "The config file has a value Nearhost cannot use."},
	{ConfigUnknownKeys, Error, 400, "The config file has keys Nearhost does not know, so saving it would lose them."},
	{CAMoveFailed, Warning, 0, "Nearhost could not move its local CA to the data directory, or could not delete the old copy."},
	{ServiceUnsupported, Error, 501, "The Nearhost service is not supported on this system."},
	{ServiceNoUserManager, Error, 503, "There is no user service manager to run the Nearhost service."},
	{ServiceCopy, Error, 409, "This copy of nearhost cannot be run by a service later, such as a one-run copy."},
	{ServiceAsRoot, Error, 403, "The service must be installed as the user who runs it, not as root."},
	{ServiceCannotBind, Error, 409, "The service cannot run this copy of nearhost, as it lacks permission to use ports 443 and 80."},
	{UpdateCheckFailed, Error, 503, "Nearhost could not find out whether a newer release exists."},
	{UpdateAvailable, Info, 0, "A newer release of Nearhost is available."},
	{InterfaceNoTerminal, Error, 400, "The interface needs a terminal, and there is none."},
	{CoreNothingToServe, Error, 409, "nearhost up found no project it can serve, and none still starting, so it stopped."},
	{CoreStopTimeout, Error, 504, "Nearhost was asked to stop and had not stopped in time."},
	{CoreOutdated, Info, 0, "The running Nearhost is an older release than this copy, and runs it once restarted."},
	{CoreNoReload, Info, 0, "The running Nearhost serves the projects it was started with and does not reload the config."},
	{CoreLogUnavailable, Warning, 0, "The core could not send its standard error to its log, so a crash may not be logged."},
	{CoreNoProjects, Error, 404, "The config has no projects, so there is nothing to start."},
	{CoreUnknownProject, Error, 404, "No project in the config has the name given."},
	{CoreNoEnabledProjects, Error, 409, "Every project in the config is disabled, and none was named to start."},
	{CANotPrivate, Warning, 0, "Nearhost could not make the folder that holds its local CA private to the user."},
	{TrustNoSudo, Error, 500, "sudo, which trusting or untrusting the local CA needs, is not installed."},
	{TrustNotRemoved, Error, 409, "The local CA was not removed from a trust store, and running untrust again can finish it."},
	{TrustRemains, Error, 409, "Something Nearhost cannot change still trusts the local CA."},
	{ServiceStartFailed, Warning, 0, "Starting Nearhost through its service failed, so it was started directly."},
	{ServiceOwnSettings, Info, 0, "A service is installed, but it runs with its own settings, so Nearhost was started directly with the ones given."},
	{HostsHelperRefused, Error, 400, "The elevated hosts helper refused its input or report file."},
	{UpdateFailed, Error, 500, "The update ran, but the installed nearhost is not the new release."},
	{DiscoveryIncomplete, Warning, 0, "Something discovery reads, such as a preset or a project file, could not be read, so it went on without it."},
	{DoctorChecksFailed, Error, 409, "One or more of nearhost doctor's checks failed."},
	{InterfaceFlagsIgnored, Info, 0, "Serve flags given to the interface do nothing to the Nearhost already running, which keeps its settings."},
	{InterfacePasswordCancelled, Error, 403, "The password was cancelled, so what needed it was not done."},
	{InterfacePromptDeclined, Error, 403, "The Windows prompt was declined, so what needed it was not done."},
	{InterfaceLogMissed, Info, 0, "The interface fell behind the running Nearhost, so some log lines were missed."},
	{CoreRunDirFailed, Error, 500, "Nearhost could not create its run directory, which holds its sockets and its lock."},
	{InterfaceOpenFailed, Warning, 0, "The interface could not open a link in the browser."},
	{InterfaceCopyFailed, Warning, 0, "The system's clipboard tool failed on a copy; the terminal may still have copied it."},
	{InterfaceNothingToCopy, Warning, 0, "What the interface was asked to copy has nothing it can copy."},
	{InterfaceAddressTooLong, Warning, 0, "An address in the log is too long to show, so a click does not open it."},
	{InterfaceAnswerNotSaved, Warning, 0, "The quit box's answer could not be saved, so it asks again next time."},
	{InterfaceChangeIgnored, Warning, 0, "A change to the config file could not be applied and was ignored."},
	{ServiceUnreadable, Warning, 0, "The installed service's definition cannot be read, as one edited by hand, so it is removed as it is."},
	{ServiceManagerFailed, Error, 502, "The system's service manager refused to install, start or remove the Nearhost service."},
	{ServiceNodeVersion, Info, 0, "The service runs nearhost from a Node version's folder, so changing the default Node version needs a new install."},
	{ServiceAgentUnset, Info, 0, "SSH_AUTH_SOCK is not set, so the service uses the agent the login session provides."},
	{ServiceAgentChanges, Warning, 0, "SSH_AUTH_SOCK points at an agent socket that changes at each login, so the service cannot use it."},
	{AddLonePort, Warning, 0, "Discovery found one port only, so nearhost add made it the web route."},
	{AddNoWebPort, Error, 400, "nearhost add could not tell which port serves the project, so nothing was saved."},
	{AddBadName, Error, 400, "The project's name is not one Nearhost can use."},
	{AddAliasTaken, Error, 409, "The name the project asks to be served at is already another project's."},
	{AddNameTaken, Error, 409, "Another project already has the name the project would be added under."},
	{AddNoDirectory, Error, 404, "There is no directory at the path given to nearhost add."},
	{AddServerChanged, Error, 409, "The server was added meanwhile and is reached another way, so nothing was saved."},
	{AddUnknownServer, Error, 404, "The server named is not in the config."},
	{AddNeedsApproval, Error, 409, "The project asks for things that need approval, and there is nobody to ask."},
	{AddHostTaken, Info, 0, "A name the project would be served at is already another project's, so it is left out."},
	{AddBadConfig, Error, 400, "The config file cannot be read, so nothing is added to it."},
	{AddRelativePath, Error, 400, "A project on a server was given a relative path, which Nearhost cannot find there."},
	{AddBadAlias, Error, 400, "The domain the project would be served at is not one Nearhost can serve, so nothing was saved."},
	{PresetRegistryUnreadable, Warning, 0, "A trusted registry of presets cannot be read, so its presets are left out."},
	{PresetNotUpdated, Warning, 0, "A preset could not be updated from its registry."},
	{PresetNotEnabled, Error, 404, "The preset named is not enabled."},
	{PresetUnknown, Error, 404, "No preset or registry has the name given."},
	{PresetLocalFile, Error, 409, "The preset is a local file, so there is no registry to update it from."},
	{PresetRegistryUnknown, Error, 404, "No registry is trusted under the name given."},
	{PresetRegistryNotTrusted, Error, 502, "A registry's index could not be fetched or checked, so it was not trusted and nothing was saved."},
	{PresetUpdatesFailed, Error, 502, "Some of the presets asked for could not be updated from their registries."},
	{PresetRegistryInUse, Error, 409, "Presets from the registry are enabled, so it cannot be removed."},
	{ExportExists, Error, 409, "The project already has a .nearhostrc, and export does not replace one unasked."},
	{ExportChanged, Error, 409, "The project changed while it was exported, so the file written is not approved."},
	{ExportNoProject, Error, 404, "No Nearhost project contains the folder, or none has the name given."},
	{ExportReplacedPending, Info, 0, "The .nearhostrc change that waited for approval was replaced with what is served now."},
	{HostsCoreNotTold, Warning, 0, "The hosts file is updated, but the running Nearhost could not be told."},
	{DocumentsOpenFailed, Warning, 0, "A document could not be opened in the browser; its address still works."},
	{AgentsUnavailable, Warning, 0, "Coding agents on a server cannot reach Nearhost."},
	{AgentsApprovalWaiting, Info, 0, "A coding agent asks for something that waits for approval in the interface."},
	{AgentsGrantsPruned, Info, 0, "Grants of projects that are gone were removed."},
	{AgentsGrantsOff, Error, 503, "Grants cannot be read or changed, as the grants file cannot be read or Nearhost does not serve coding agents in this run."},
	{AgentsAllowNotSaved, Error, 500, "A request was answered, but its Always allow could not be saved, so the agent is asked again next time."},
	{ControlNoRoute, Error, 404, "The control API has no route at the path asked for; the running Nearhost may be an older release."},
	{ControlMethodNotAllowed, Error, 405, "A route of the control API was asked with a method it does not take."},
	{ControlBadRequest, Error, 400, "A request to the control API is not what the route takes."},
	{ControlNotFound, Error, 404, "The running Nearhost does not have what was asked for, such as a request it no longer keeps."},
	{ControlUnimplemented, Error, 501, "The running Nearhost cannot do what was asked yet."},
	{ControlRequestEnded, Error, 409, "The request to answer has ended: the agent gave up on it, it expired, or someone answered it first."},
	{CoreNoDesktop, Warning, 0, "Nearhost in the background runs outside the desktop session, so it cannot open links."},
	{CoreSessionUnchecked, Info, 0, "Nearhost in the background could not tell whether it runs in the sign-in session, so it may not open links."},
	{CoreSSHAgentUnset, Info, 0, "SSH_AUTH_SOCK is not set for Nearhost in the background, so ssh uses the agent in the SSH config, if any."},
	{CoreSSHAgentUnreachable, Warning, 0, "The SSH agent Nearhost in the background was given does not answer."},
	{CoreDockerMissing, Info, 0, "docker is not on the PATH of Nearhost in the background; only Docker projects need it."},
	{CoreDockerUnreachable, Warning, 0, "Docker does not answer Nearhost in the background."},
	{PageRenderFailed, Error, 500, "Nearhost's page could not be drawn."},
	{PageRequestEnded, Warning, 409, "The request on the page has been answered or has ended."},
	{PageProposalDeclineOnly, Error, 400, "A proposal can only be declined on the page; reviewing it needs Nearhost on this machine."},
	{PageAnswerNotOffered, Error, 400, "The answer sent is not one the request offers."},
	{PageChangeAnswerInvalid, Error, 400, "The answer sent for a .nearhostrc change is not approve or decline for the change shown."},
	{PageNotOpened, Error, 502, "The answer ended the request, as what it asked for could not be opened."},
	{PageAnswerFailed, Error, 502, "The core could not take the answer given on the page."},
	{PageChangeChanged, Error, 409, "The .nearhostrc change changed again, so the answer was not for the change now waiting."},
	{ProxyAppNotAnswering, Error, 502, "Nothing answered on the port a project's app listens on."},
	{ProxyForwardReconnecting, Error, 502, "The forward to a project's server is down while Nearhost reconnects it."},
	{ProxyLocked, Error, 403, "Nearhost's page must be opened from this machine first, to get its cookie."},
	{ProxyNotFound, Error, 404, "Nearhost's page at a project's domain has nothing at the path asked for."},
	{ProxyMethodNotAllowed, Error, 405, "A path of Nearhost's page was asked with a method it does not take."},
	{ProxyForbidden, Error, 403, "A request to Nearhost's page was refused."},
	{PairCodeInvalid, Error, 400, "The pairing code typed on a device is wrong, has been used or has expired."},
	{PairLinkUsed, Error, 403, "The link that signs a paired device in to a site has been used or has expired."},
	{PairBadTarget, Error, 400, "A device asked to be signed in to an address outside the network domain."},
	{PairTooManyTries, Error, 429, "A device made too many wrong attempts to pair or sign in, so it must wait."},
	{PairNeeded, Error, 401, "A site on the network domain was asked for by a device that is not paired."},
	{PairUnavailable, Error, 503, "A site on the network domain needs a paired device, and Nearhost's page, where devices pair, is not served."},
	{NetworkApplyFailed, Error, 500, "Nearhost could not change how it serves to other devices on the network after the config changed."},
	{NetworkDevicesUnavailable, Warning, 0, "Nearhost could not open its list of paired devices, so no other device can pair or sign in."},
	{NetworkOff, Error, 409, "Something was asked that needs networked mode, and the config does not turn it on."},
	{NetworkNoToken, Error, 503, "Networked mode gets its certificate with a DNS token, and none is set."},
	{NetworkDNS, Warning, 0, "The name of Nearhost's page on the network does not point at this machine."},
	{NetworkFirewall, Error, 503, "The firewall does not let other devices reach Nearhost."},
	{NetworkFirewallRefused, Error, 400, "The elevated firewall helper refused a program other than the one running, or its input or report file."},
	{NetworkCert, Error, 503, "The certificate for Nearhost's page on the network is missing or not the one the config asks for."},
	{NetworkNoPage, Error, 503, "Networked mode needs Nearhost's page, where devices pair, and the page is not served."},
	{NetworkNotListening, Error, 503, "Networked mode is on, but Nearhost does not serve to the network yet, so no device could reach it."},
	{DeviceUnknown, Error, 404, "No paired device has the name asked for."},
	{DocumentsMissing, Error, 404, "The document asked for is not there any more."},
	{DocumentsChanging, Error, 503, "The document's folder kept changing while Nearhost checked it."},
	{DocumentsNotConnected, Error, 503, "Nearhost is not connected to the server that holds the document."},
	{DocumentsNotFound, Error, 404, "The document server has nothing at the path asked for."},
	{DocumentsMethodNotAllowed, Error, 405, "The document server was asked with a method it does not take."},
	{DocumentsUnreadable, Error, 502, "The document could not be read."},
	{AgentNoSuchCall, Error, 404, "The agent API has no call at the path asked for, or this Nearhost does not have that call."},
	{AgentNoToken, Error, 401, "A request to the agent API's loopback port came without its token, as from an older copy of the skill."},
	{AgentRefused, Error, 429, "The user said No to a request of the same kind from the agent's machine moments ago, so this one is refused for now."},
	{AgentNoProject, Error, 404, "No project in Nearhost holds the path the agent asked about."},
	{AgentBadPath, Error, 400, "A path in an agent's request is not one Nearhost takes, such as a relative or network path."},
	{AgentBadRequest, Error, 400, "An agent's request, or a value in it, is not what the call takes."},
	{AgentDoctorBusy, Error, 429, "A doctor run for the agent's server is already going, or started moments ago."},
	{AgentNotADirectory, Error, 404, "The directory an agent asked the doctor about is not there."},
	{AgentDoctorFailed, Error, 502, "The doctor could not check what an agent asked about."},
	{AgentNoReviewer, Error, 409, "An agent's request needs the user's answer, and Nearhost has no page or window open to ask in."},
	{AgentTooManyWaiting, Error, 429, "The agent's machine has as many requests waiting for the user as it may."},
	{AgentReloadFailed, Error, 502, "Nearhost could not read a project's .nearhostrc again for an agent."},
	{AgentNearhostrcUnread, Error, 422, "A project's .nearhostrc could not be taken into account when an agent asked to reload it."},
	{AgentRequestGone, Error, 404, "The proposal, passthrough or document request an agent asked about is not known; it may have ended long ago."},
	{AgentLocalPassthrough, Error, 400, "A passthrough was asked for on the laptop's own socket, and passthroughs forward a server's port."},
	{AgentGrantsFailed, Error, 500, "Nearhost could not read the grants that let an agent's requests open without asking."},
	{AgentTokenRevoked, Error, 401, "The token an agent sent was revoked or never existed."},
	{AgentTooManyPassthroughs, Error, 429, "The agent's server has as many passthroughs open as it may."},
	{AgentNotConnected, Error, 409, "Nearhost is not connected to the agent's server."},
	{AgentPassthroughFailed, Error, 502, "A passthrough's forward could not be opened or closed."},
	{AgentClosedWhileOpening, Error, 409, "The agent's session closed while its passthrough opened, so the passthrough was closed again."},
	{AgentPortHeld, Error, 409, "Another agent or session has the port passed through, or waiting for approval."},
	{AgentDocumentNotThere, Error, 404, "The document an agent asked to show is not there."},
	{AgentNotADocument, Error, 400, "What an agent asked to show is not a document Nearhost can show."},
	{AgentDocumentFailed, Error, 502, "Nearhost could not read or show the document an agent asked to show."},
	{AgentNoPage, Error, 409, "Nearhost's page is not served, so it cannot show an agent's document."},

	{DoctorNotChecked, Info, 0, "nearhost doctor could not run a check, and says what it did not check."},
	{CoreOtherVersion, Warning, 0, "The running Nearhost is another release than this copy."},
	{ServiceNotInstalled, Info, 0, "The Nearhost service is not installed, so Nearhost does not start at login."},
	{ServiceOtherCopy, Warning, 0, "The installed service runs another copy of nearhost than this one."},
	{ServiceLogFolderMissing, Warning, 0, "The folder of the service's log is missing, so the service cannot start again."},
	{ServiceNotRunning, Warning, 0, "The Nearhost service is installed but not running."},
	{ServiceTakesOver, Info, 0, "Nearhost runs, started another way, and the installed service takes over at the next login."},
	{ConfigNoHome, Error, 500, "Nearhost cannot tell where its files are, as HOME (or XDG_CONFIG_HOME) is not set."},
	{ConfigMissing, Warning, 0, "The config file named with --config does not exist yet."},
	{ConfigBadAlias, Error, 400, "A project's main name is not a valid domain, so nearhost up does not start it."},
	{ConfigPatternConflict, Warning, 0, "A project serves a host under another project's pattern, and gets its requests."},
	{ConfigLoopbackOwnPort, Error, 409, "A project's loopback route is on a port Nearhost itself listens on, so it is not forwarded."},
	{CANotCreated, Error, 409, "The local CA has not been created yet."},
	{CAUnreadable, Error, 500, "The local CA's certificate cannot be read."},
	{CANotTrusted, Error, 409, "The system trust store does not trust the local CA."},
	{CAOldCopy, Warning, 0, "An old folder still holds a copy of the local CA's private key."},
	{CABrowsersNotTrusted, Warning, 0, "The browser trust stores (NSS) do not trust the local CA."},
	{CAFirefoxRootsOff, Warning, 0, "Firefox is set not to read Windows' trusted roots, where the local CA is trusted."},
	{HostsUnreadable, Error, 500, "The hosts file cannot be read."},
	{SSHAgentMissing, Info, 0, "The OpenSSH Authentication Agent is not installed on Windows."},
	{SSHAgentStopped, Info, 0, "The OpenSSH Authentication Agent service on Windows is not running."},
	{SSHNotFound, Error, 503, "ssh cannot be run, from this shell's PATH or that of Nearhost in the background, so Nearhost cannot reach servers."},
	{SSHTooOld, Warning, 0, "The ssh on PATH is older than the one Nearhost needs."},
	{SSHVersionUnknown, Info, 0, "Nearhost could not read the version of the ssh on PATH."},
	{SSHOtherVendor, Warning, 0, "The ssh Nearhost runs on Windows is not Microsoft's OpenSSH."},

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

// fixedSteps are the steps of the codes whose fix never varies, which the
// catalog's entries carry.
var fixedSteps = map[Code][]Step{
	PairCodeInvalid: {
		{Text: "Get a new code on the machine Nearhost runs on:", Command: "nearhost pair"},
	},
	CoreNotRunning: {
		{Text: "Start it in the background:", Command: "nearhost -d"},
		{Text: "Or open its interface:", Command: "nearhost"},
	},
	UpdateAvailable: {
		{Text: "Update it:", Command: "nearhost update"},
	},
	InterfaceNoTerminal: {
		{Text: "Run it in the background:", Command: "nearhost -d"},
		{Text: "Or serve without the interface:", Command: "nearhost up"},
	},
	CANotCreated: {
		{Text: "Create the local CA and trust it:", Command: "nearhost trust"},
	},
	CANotTrusted: {
		{Text: "Trust the local CA:", Command: "nearhost trust"},
	},
	ServiceNotInstalled: {
		{Text: "To start Nearhost at login, install the service:", Command: "nearhost service install"},
	},
	ServiceNotRunning: {
		{Text: "Start it:", Command: "nearhost -d"},
	},
}

// catalog is every entry, in the order of catalogRows, with its steps.
var catalog = func() []Entry {
	out := make([]Entry, len(catalogRows))
	for i, r := range catalogRows {
		out[i] = Entry{Code: r.Code, Level: r.Level, Status: r.Status, Summary: r.Summary, Steps: fixedSteps[r.Code]}
	}
	return out
}()

var byCode = func() map[Code]Entry {
	m := make(map[Code]Entry, len(catalog))
	for _, e := range catalog {
		m[e.Code] = e
	}
	return m
}()

// Catalog returns every code with a fixed name, in the order of this file.
// The advice family (AdviceCode) is not listed.
func Catalog() []Entry {
	out := slices.Clone(catalog)
	for i := range out {
		out[i].Steps = slices.Clone(out[i].Steps)
	}
	return out
}

// Lookup returns a code's catalog entry. A code of the advice family is
// found too, as a warning.
func Lookup(c Code) (Entry, bool) {
	if e, ok := byCode[c]; ok {
		e.Steps = slices.Clone(e.Steps)
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

// FindingCode returns the code of a nearhost doctor finding whose ID is id:
// the ID itself, as a code of the catalog or of the advice family. ok is
// false for an ID that is neither, or for a code of the catalog that is not
// a finding's.
func FindingCode(id string) (Code, bool) {
	c := Code(id)
	if _, ok := findingCodes[c]; ok {
		return c, true
	}
	if rest, ok := strings.CutPrefix(id, adviceFamily); ok && rest != "" {
		return c, true
	}
	return "", false
}

// findingCodes are the codes that are nearhost doctor's finding IDs.
var findingCodes = map[Code]struct{}{
	NearhostrcPending: {}, NearhostrcNotApplied: {}, NearhostrcGone: {}, NearhostrcVersion: {},
	NearhostrcUnsetVariable: {}, NearhostrcInvalid: {},
	RoutesUnservedPort: {}, RoutesStalePort: {}, RoutesUnservedHost: {}, RoutesNotListening: {},
	PresetsNote:       {},
	HostsDNSUnchecked: {}, HostsUnresolved: {}, HostsPublic: {}, HostsUnresolvedPattern: {}, HostsOverlap: {},
	DockerComposeDepth: {}, DockerHostGateway: {}, DockerComposeParse: {}, DockerComposeFile: {},
	DockerClients: {}, DockerCA: {},
	ProjectsUnchecked: {},
}
