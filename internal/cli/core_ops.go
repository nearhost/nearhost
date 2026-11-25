package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"unicode/utf8"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/proxy"
	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/tui"
)

// coreOps is what the interface asks of the engine and the agents' API,
// with no terminal: what the core does for its clients.
type coreOps struct {
	eng *engine.Engine
	// agents answers the agents' proposals; nil when none can reach
	// nearhost.
	agents *agents
	// approvals is the queue of what the agents ask, which the answers take
	// requests off; nil when nothing is queued.
	approvals *approvals
	// path is the config file.
	path      string
	httpsPort int
	// reload lets one Apply run at a time, the watcher's included.
	reload sync.Mutex
	// acceptSaving, when set, runs inside AcceptChange's save. Tests use it
	// to re-read meanwhile; it is nil otherwise.
	acceptSaving func()
	// acceptLocking, when set, runs in AcceptChange after it has checked
	// the change and before it takes the config's lock. Tests use it to
	// save meanwhile; it is nil otherwise.
	acceptLocking func()
	// applied, when set, runs in Apply after the engine has applied and
	// before the grants are pruned. Tests use it to change the config file
	// meanwhile; it is nil otherwise.
	applied func()
}

var _ control.Actions = (*coreOps)(nil)

// Apply applies cfg to the engine, one Apply at a time. Once it has applied,
// the grants of servers cfg no longer has are removed, so a server added
// later under the same name does not inherit them. A failed Apply removes
// none, and a failure to remove them is shown in the log and does not fail
// the Apply. Neither does an Apply that began while the config file was
// missing: a missing file loads as no servers, and the file may be back by
// the time the apply ends.
func (o *coreOps) Apply(ctx context.Context, cfg *config.Config) error {
	o.reload.Lock()
	defer o.reload.Unlock()
	return o.applyLocked(ctx, cfg)
}

// applyLocked is Apply, with o.reload held.
func (o *coreOps) applyLocked(ctx context.Context, cfg *config.Config) error {
	_, statErr := os.Stat(o.path)
	if err := o.eng.Apply(ctx, cfg); err != nil {
		return err
	}
	if o.applied != nil {
		o.applied()
	}
	if statErr != nil {
		return nil
	}
	if msg := o.agents.prune(o.path, cfg); msg != "" {
		o.eng.Events().Publish(reportEvent(report.Notef(report.AgentsGrantsPruned, "%s", msg)))
	}
	return nil
}

// Reload loads the config file and applies it now. A file that does not
// load is not applied, and the error names it. It loads in Apply's turn, so
// two reloads apply the file in the order they read it: an older read is
// never applied over a newer one.
func (o *coreOps) Reload(ctx context.Context) error {
	o.reload.Lock()
	defer o.reload.Unlock()
	cfg, err := config.Load(o.path)
	if err != nil {
		return err
	}
	return o.applyLocked(ctx, cfg)
}

// edit reads the config file afresh, runs fn on it, checks the result and
// saves it, as the interface's EditConfig does.
func (o *coreOps) edit(fn func(cfg *config.Config) error) error {
	return editConfig(o.path, fn)
}

// editConfig is config.Update with the config validated first, and the file
// named in what the validation finds.
func editConfig(path string, fn func(cfg *config.Config) error) error {
	return config.Update(path, func(cfg *config.Config) error {
		if err := validateConfig(path, cfg); err != nil {
			return err
		}
		return fn(cfg)
	})
}

// validateConfig validates cfg as read from the config file at path, naming
// the file in what it finds.
func validateConfig(path string, cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// AcceptChange saves the routes and snapshot of the change with hash sha
// that the project's .nearhostrc waits on. It must be the change pending
// now: the user approved what the review showed, and a newer file may ask
// for more. The config poll then applies the save, which restarts the
// project with it. An agent waiting on the change is told once it is saved;
// until then its reload is held, from before the change is read, so that a
// re-read clearing the pending change meanwhile does not end it superseded.
//
// The check is made again under the config's lock, with the approved snapshot
// the change was reviewed against: a review that saved a newer file while
// this waited for the lock would otherwise be overwritten.
func (o *coreOps) AcceptChange(project, sha string) error {
	done := o.agents.accepting(project, sha)
	stale := fmt.Errorf("the %s changed again; review it again", nearhostrc.FileName)
	change := o.eng.Status(project).Pending
	if change == nil || change.SHA256 != sha {
		done(false)
		return stale
	}
	approved := approvedRc(o.eng.Config(), project)
	if o.acceptLocking != nil {
		o.acceptLocking()
	}
	err := o.edit(func(cfg *config.Config) error {
		if now := o.eng.Status(project).Pending; now == nil || now.SHA256 != sha || approvedRc(cfg, project) != approved {
			return stale
		}
		before := append([]config.Project(nil), cfg.Projects...)
		if err := takeRc(cfg, change.Project, o.path); err != nil {
			return err
		}
		if o.acceptSaving != nil {
			o.acceptSaving()
		}
		p, _ := cfg.Project(project)
		if hc, clash := cfg.HostConflictSince(before, *p); clash {
			return hc
		}
		return nil
	})
	done(err == nil)
	return err
}

// approvedRc is the hash of the .nearhostrc approved for project in cfg, ""
// for none.
func approvedRc(cfg *config.Config, project string) string {
	if p, ok := cfg.Project(project); ok && p.Nearhostrc != nil {
		return p.Nearhostrc.SHA256
	}
	return ""
}

// DeclineChange is the engine's, and tells an agent waiting on the change.
// The agent is told first: once the engine forgets the change nothing
// waits, and a poll in between would find its reload superseded.
func (o *coreOps) DeclineChange(project, sha string) {
	o.agents.changeAnswered(project, sha, false)
	o.eng.DeclineChange(project, sha)
}

// StartProject starts a project, saying when the start was asked for: a
// start the user asks for after a refused hosts update asks for the
// password again, where the starts that were already under way do not.
func (o *coreOps) StartProject(ctx context.Context, name string) error {
	return o.eng.StartProject(askedNow(ctx), name)
}

// StopProject stops a project.
func (o *coreOps) StopProject(ctx context.Context, name string) error {
	return o.eng.StopProject(ctx, name)
}

// Recheck reads a project's .nearhostrc again without restarting it.
func (o *coreOps) Recheck(ctx context.Context, name string) error {
	return o.eng.Recheck(ctx, name)
}

func (o *coreOps) ConnectServer(_ context.Context, name string) error {
	return o.eng.ConnectServer(name)
}

func (o *coreOps) DisconnectServer(_ context.Context, name string) error {
	return o.eng.DisconnectServer(name)
}

func (o *coreOps) ReconnectServer(_ context.Context, name string) error {
	return o.eng.ReconnectServer(name)
}

// AcceptProposal accepts the proposal id and takes it off the queue. The
// agent API refuses no answer to a proposal, so it always ends.
func (o *coreOps) AcceptProposal(id, name string) {
	o.agents.accept(id, name)
	o.approvals.take(id)
}

// DeclineProposal declines the proposal id and takes it off the queue.
func (o *coreOps) DeclineProposal(id, reason string) {
	o.agents.decline(id, reason)
	o.approvals.take(id)
}

// ClosePassthrough closes the passthrough id for the user.
func (o *coreOps) ClosePassthrough(ctx context.Context, id string) error {
	return o.eng.ClosePassthrough(ctx, id, "closed in Nearhost")
}

// answerOf is the interface's answer that the control API's answer names.
func answerOf(s string) (tui.Answer, error) {
	switch s {
	case "yes":
		return tui.AnswerYes, nil
	case "no":
		return tui.AnswerNo, nil
	case "always":
		return tui.AnswerAlways, nil
	case "close":
		return tui.AnswerClose, nil
	}
	return 0, fmt.Errorf("the answer must be yes, no, always or close, not %q", s)
}

// AnswerPassthrough gives the user's answer to a coding agent's request for
// a passthrough to the agent API, then takes the request off the queue
// unless it still waits; see settle. One not queued, such as an Always
// allow open answered with close, still reaches the agent API.
func (o *coreOps) AnswerPassthrough(ctx context.Context, id, answer string) error {
	a, err := answerOf(answer)
	if err != nil {
		return err
	}
	// A No ends the origin's other prompts in the agent API too, which
	// takes them off the queue through Ended.
	return o.settle(id, o.agents.answerPassthrough(ctx, id, a))
}

// AnswerDocument gives the user's answer to a coding agent's request to
// show a document to the agent API, then settles it as AnswerPassthrough
// does.
func (o *coreOps) AnswerDocument(ctx context.Context, id, answer string) error {
	a, err := answerOf(answer)
	if err != nil {
		return err
	}
	return o.settle(id, o.agents.answerDocument(ctx, id, a))
}

// settle takes the request id off the queue once err, the agent API's
// answer to it, says it waits no longer: it was answered, it had ended, or
// it was answered and only its Always allow was not saved. Any other error
// refused the answer and left the request waiting, as an Always allow for
// an agent that did not say which tool it is does, so it stays queued for a
// client to answer. It returns err marked for the control API.
func (o *coreOps) settle(id string, err error) error {
	err = controlAnswerError(err)
	if err == nil || errors.Is(err, control.ErrRequestEnded) || errors.Is(err, tui.ErrAllowNotSaved) {
		o.approvals.take(id)
	}
	return err
}

// controlAnswerError is the error of an answer, marked for the control API
// as well: as the end of its request, and as an Always allow that was not
// saved.
func controlAnswerError(err error) error {
	var marks []error
	if errors.Is(err, tui.ErrRequestEnded) {
		marks = append(marks, control.ErrRequestEnded)
	}
	if errors.Is(err, tui.ErrAllowNotSaved) {
		marks = append(marks, control.ErrAllowNotSaved)
	}
	if len(marks) == 0 {
		return err
	}
	return controlMarked{err, marks}
}

// controlMarked is an error that reads as its own, and is also each of the
// control API's errors in marks.
type controlMarked struct {
	error
	marks []error
}

// Unwrap is the error and its marks.
func (e controlMarked) Unwrap() []error { return append([]error{e.error}, e.marks...) }

// Grants lists the grants of every kind on server, or says why they cannot
// be read in this run.
func (o *coreOps) Grants(server string) ([]grants.Grant, error) {
	return o.agents.grantsOf(server)
}

// RevokeGrant takes back requester's grant of kind on server, or says why
// it cannot be taken back in this run.
func (o *coreOps) RevokeGrant(server, requester, kind string) error {
	return o.agents.revokeGrant(server, requester, kind)
}

// Documents lists the trees of documents served on server, newest first.
func (o *coreOps) Documents(server string) []documents.Tree {
	return o.agents.documents(server)
}

// OpenDocument opens the newest document of the tree id in the browser.
func (o *coreOps) OpenDocument(id string) error { return o.agents.openDocument(id) }

// RemoveDocuments stops serving the tree id.
func (o *coreOps) RemoveDocuments(id string) error { return o.agents.removeDocuments(id) }

// Captured is the exchange the proxy kept under id, while it is kept.
func (o *coreOps) Captured(id string) (proxy.Exchange, bool) { return o.eng.Captured(id) }

// Replay sends the request kept under id again and returns its status.
func (o *coreOps) Replay(ctx context.Context, id string) (int, error) {
	return o.eng.Replay(ctx, id)
}

// PageKeyURL is the address of Nearhost's page at /?k=<secret>, which gives
// the browser the cookie the page's document list needs.
func (o *coreOps) PageKeyURL() (string, error) {
	base, err := o.eng.PageURL()
	if err != nil {
		return "", err
	}
	return proxy.PageKeyURL(base)
}

// Environment looks at what the core can reach from where it runs.
func (o *coreOps) Environment(ctx context.Context) ([]control.EnvCheck, error) {
	return coreEnvironment(ctx, systemEnvProbe()), nil
}

// State is what the core serves: the config the engine runs, and the state
// of each of its projects and servers, the open passthroughs, the documents
// served, the grants, this machine's included, and the requests that wait
// for an answer. The caller adds what the core is and the ports it was asked
// for.
func (o *coreOps) State() control.State {
	cfg := o.eng.Config()
	st := control.State{
		ConfigPath:   o.path,
		HTTPSPort:    o.httpsPort,
		Projects:     map[string]engine.ProjectStatus{},
		Servers:      map[string]control.ServerState{},
		Passthroughs: o.eng.Passthroughs(""),
		Documents:    o.agents.documents(""),
		Grants:       []grants.Grant{},
		Approvals:    o.approvals.Pending(),
	}
	if data, err := config.Marshal(cfg); err == nil {
		st.Config = string(data)
	}
	for _, p := range cfg.Projects {
		st.Projects[p.Name] = o.eng.Status(p.Name)
	}
	for _, name := range serverNames(cfg) {
		// One look at the server, so its state, text and problem agree.
		h := o.eng.ServerHealth(name)
		st.Servers[name] = control.ServerState{Health: h, Status: h.Status, Message: h.LastError, Problem: h.Problem}
	}
	if st.Passthroughs == nil {
		st.Passthroughs = []engine.Passthrough{}
	}
	if st.Documents == nil {
		st.Documents = []documents.Tree{}
	}
	if st.Approvals == nil {
		st.Approvals = []control.Approval{}
	}
	if err := o.agents.grantsErr(); err != nil {
		st.GrantsProblem = report.From(err)
		st.GrantsError = st.GrantsProblem.Title
		return st
	}
	for _, name := range serverNames(cfg) {
		gs, err := o.agents.grantsOf(name)
		if err != nil {
			st.GrantsProblem = report.From(err)
			st.GrantsError = st.GrantsProblem.Title
			break
		}
		st.Grants = append(st.Grants, gs...)
	}
	return st
}

// maxEventMessage is the longest event message the core streams to its
// clients, and the longest of each text of an event's report. A client reads
// an envelope of 1 MiB at most, and one longer would break every client's
// stream; engine messages are far shorter.
const maxEventMessage = 64 << 10

// maxEventReport is the most an event's report may take as JSON. With the
// message, which JSON may make six times as long by escaping, it keeps an
// envelope under what a client reads.
const maxEventReport = 512 << 10

// cutMessage is ev with a message longer than maxEventMessage cut to it,
// ending in an ellipsis, and never inside a character, and with its report
// cut by cutReport.
func cutMessage(ev engine.Event) engine.Event {
	ev.Message = cutText(ev.Message, maxEventMessage)
	ev.Report = cutReport(ev.Report)
	return ev
}

// cutText is s cut to n bytes at most, ending in an ellipsis, and never
// inside a character; s when it fits, and "" when n leaves no room for the
// ellipsis.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	if n < len(ellipsis) {
		return ""
	}
	end := n - len(ellipsis)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + ellipsis
}

// cutReport is m as the stream can carry it: m itself when it is short and
// marshals, and otherwise a copy with each text cut to maxEventMessage, the
// quoted lines to as much in all, and data that does not marshal left out.
// A copy still over maxEventReport keeps only its title and why, cut short,
// and a report that cannot marshal at all is left out, rather than break
// the stream.
func cutReport(m *report.Message) *report.Message {
	if m == nil {
		return nil
	}
	if data, err := json.Marshal(m); err == nil && len(data) <= maxEventMessage {
		return m
	}
	c := *m
	c.Title = cutText(m.Title, maxEventMessage)
	c.Why = cutText(m.Why, maxEventMessage)
	c.Verbatim = nil
	left := maxEventMessage
	for _, line := range m.Verbatim {
		if line = cutText(line, left); line == "" && left < len("…") {
			break
		}
		c.Verbatim = append(c.Verbatim, line)
		left -= len(line)
	}
	c.Fix = nil
	for _, s := range m.Fix {
		c.Fix = append(c.Fix, report.Step{Text: cutText(s.Text, maxEventMessage), Command: cutText(s.Command, maxEventMessage)})
	}
	if _, err := json.Marshal(c.Data); err != nil {
		c.Data = nil
	}
	data, err := json.Marshal(&c)
	if err != nil {
		return nil
	}
	if len(data) > maxEventReport {
		const short = 4 << 10
		c.Title, c.Why = cutText(c.Title, short), cutText(c.Why, short)
		c.Verbatim, c.Fix, c.Data = nil, nil, nil
	}
	return &c
}

// forwardEvents publishes each event of the engine's bus to hub, until stop
// is called; stop returns once the last one has gone. A full subscriber
// drops events, as any subscriber of the bus does.
func forwardEvents(bus *engine.Bus, hub *control.Hub) (stop func()) {
	events, unsubscribe := bus.Subscribe(eventBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			ev = cutMessage(ev)
			hub.Publish(control.Envelope{Kind: control.KindEvent, Event: &ev})
		}
	}()
	return sync.OnceFunc(func() {
		unsubscribe()
		<-done
	})
}
