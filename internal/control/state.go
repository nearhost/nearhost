package control

import (
	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
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
)

// Envelope is one item on the stream.
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
	NoCapture    bool                            `json:"noCapture"`
	Projects     map[string]engine.ProjectStatus `json:"projects"`
	Servers      map[string]ServerState          `json:"servers"`
	Passthroughs []engine.Passthrough            `json:"passthroughs"`
	Documents    []documents.Tree                `json:"documents"` // newest first
	Grants       []grants.Grant                  `json:"grants"`
	GrantsError  string                          `json:"grantsError,omitempty"`
	Approvals    []Approval                      `json:"approvals"` // pending, oldest first
	Clients      int                             `json:"clients"`   // attached to the stream, the asker's own included
}
