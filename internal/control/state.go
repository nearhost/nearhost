package control

import (
	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/transport"
)

// The kinds of envelope on the stream.
const (
	// KindEvent carries an engine event: a state change or a log line.
	KindEvent = "event"
	// KindApproval carries a request that waits for the user's answer.
	KindApproval = "approval"
	// KindEnded says the approval with the envelope's ID waits no longer.
	KindEnded = "ended"
	// KindOpened tells of a passthrough opened without asking.
	KindOpened = "opened"
	// KindShown tells of a document shown without asking.
	KindShown = "shown"
	// KindReset says the client missed envelopes: it fetches the state
	// again, and goes on with the envelopes numbered after the reset's.
	KindReset = "reset"
	// KindConfig says a new config was applied: the projects, their routes
	// and servers may have changed, so the client fetches the state again.
	// It follows the event that tells of it in the log.
	KindConfig = "config"
)

// Envelope is one item on the stream. An event that carries a warning or an
// error has it in Event.Report, with its code and fix, beside its text.
type Envelope struct {
	Seq      uint64                `json:"seq"`
	Kind     string                `json:"kind"`
	Event    *engine.Event         `json:"event,omitempty"`
	Approval *Approval             `json:"approval,omitempty"` // kind approval
	ID       string                `json:"id,omitempty"`       // kind ended: the approval that is gone
	Opened   *agentapi.Passthrough `json:"opened,omitempty"`
	Shown    *Shown                `json:"shown,omitempty"`
}

// Shown is a document opened in the browser without asking. Doc is relative
// to its tree's root.
type Shown struct {
	Origin    string `json:"origin"`
	Requester string `json:"requester"`
	Doc       string `json:"doc"`
}

// Approval is a request from an agent that waits for the user's answer. One
// of Proposal, Pass and Doc is set.
type Approval struct {
	ID       string                `json:"id"`
	Proposal *agentapi.Proposal    `json:"proposal,omitempty"`
	Pass     *agentapi.PassRequest `json:"pass,omitempty"`
	Doc      *agentapi.DocRequest  `json:"doc,omitempty"`
}

// ServerState is a server's connection, as the interface shows it.
type ServerState struct {
	Health  engine.ServerHealth `json:"health"`
	Status  transport.Status    `json:"status"`
	Message string              `json:"message"`
	// Problem is why the server is not connected, as a message whose
	// title is Message; nil when there is none.
	Problem *report.Message `json:"problem,omitempty"`
}

// State is what GET /v1/state answers: everything a client mirrors.
type State struct {
	Info Info `json:"info"`
	// Seq is an envelope number the state is at least as new as: it
	// includes every envelope up to Seq, and may include some numbered
	// after it. A client applies the envelopes numbered after Seq, which
	// must therefore be harmless to apply twice.
	Seq        uint64 `json:"seq"`
	ConfigPath string `json:"configPath"`
	Config     string `json:"config"` // the YAML the engine runs
	HTTPSPort  int    `json:"httpsPort"`
	HTTPPort   int    `json:"httpPort"`
	WebPort    int    `json:"webPort"`
	// NoHosts is set when the core does not manage the hosts file: it was
	// started with --no-hosts.
	NoHosts bool `json:"noHosts"`
	// NoCapture is set when the core keeps no requests and responses in
	// memory: it was started with --no-capture.
	NoCapture bool                            `json:"noCapture"`
	Projects  map[string]engine.ProjectStatus `json:"projects"`
	Servers   map[string]ServerState          `json:"servers"`
	// Sites is each project's server and the addresses of its web routes.
	// A project's routes change only with the config, so a client learns of
	// a change by fetching the state again on an envelope of kind config (and
	// on a reset).
	Sites        map[string]Site      `json:"sites"`
	Passthroughs []engine.Passthrough `json:"passthroughs"`
	Documents    []documents.Tree     `json:"documents"` // newest first
	Grants       []grants.Grant       `json:"grants"`
	GrantsError  string               `json:"grantsError,omitempty"`
	// GrantsProblem is GrantsError as a message, with its code; nil when
	// the grants could be read.
	GrantsProblem *report.Message `json:"grantsProblem,omitempty"`
	Approvals     []Approval      `json:"approvals"` // pending, oldest first
	Clients       int             `json:"clients"`   // attached to the stream, the asker's own included
	// Update is the newer release the core has found, without "v", or ""
	// when none is known, as in Status. The core said it on the stream when
	// it found it, before an interface that opens later follows that.
	Update string `json:"update,omitempty"`
}

// Site is where a project runs and the addresses its web routes are served
// at, as ProjectInfo gives them for the first.
type Site struct {
	// Server is the server the project runs on; "" for this machine.
	Server string `json:"server,omitempty"`
	// Routes has one entry per web route, the primary first; none when the
	// project has no web route.
	Routes []SiteRoute `json:"routes"`
}

// SiteRoute is one web route of a project.
type SiteRoute struct {
	// Host is the route's main name: the project's alias for the primary
	// route.
	Host string `json:"host"`
	// Port is the route's port, the one the dev server listens on.
	Port int `json:"port"`
	// URL is the address a browser uses.
	URL string `json:"url"`
	// NetworkURL is the address on the network; "" when the core does not
	// serve to the network or the route has no host there.
	NetworkURL string `json:"networkUrl,omitempty"`
}
