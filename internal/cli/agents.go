package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/transport"
	"github.com/nearhost/nearhost/internal/tui"
)

// agentsBackend is the seam between the agent API and the machine. A zero
// one serves no agents.
type agentsBackend struct {
	// home is this machine's home directory, which holds ~/.nearhost.
	home func() (string, error)
	// id is this install's id (config.InstallID).
	id func() (string, error)
	// laptop names this machine to agents, as user@host.
	laptop func() string
	// grants is the file of Always allow grants (config.GrantsPath). When it
	// is nil, as in most tests, agents have no passthroughs.
	grants func() (string, error)
}

// systemAgents is the production backend. Only NewRoot wires it in.
func systemAgents() agentsBackend {
	return agentsBackend{home: os.UserHomeDir, id: config.InstallID, laptop: laptopName, grants: config.GrantsPath}
}

// agentSocket is this machine's own agent socket, ~/.nearhost/<id>.sock in
// home, for the install id.
func agentSocket(home, id string) string {
	return filepath.Join(home, transport.AgentDir, id+".sock")
}

// nearhostRunning reports whether another Nearhost is serving on this machine:
// every run serves coding agents on the same socket, so one answering there
// means a Nearhost is up. When the socket can't be worked out it says yes, the
// cautious answer for its callers. Working it out needs the install id, so
// config.InstallID creates the id file if there is none yet.
func nearhostRunning() bool {
	id, err := config.InstallID()
	if err != nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	return agentapi.Answers(agentSocket(home, id))
}

// laptopName is the user and the short host name, as in dev@laptop.
func laptopName() string {
	name := "you"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return name
	}
	host, _, _ = strings.Cut(host, ".")
	return name + "@" + host
}

// agents is the agent API of one run of nearhost: this machine's socket,
// and one private socket for each server, which the server's session
// forwards to ~/.nearhost there.
type agents struct {
	api    *agentapi.Server
	id     string
	runDir string
	src    *agentSource
	sock   string // this machine's own socket
	// grants is the one store of grants for the run, shared with the
	// interface; nil when there are no passthroughs.
	grants *grants.Store
	// grantsOff says why there are no grants when the file could not be
	// read; empty otherwise.
	grantsOff string
	// docs serves the documents agents show; nil when there are no grants,
	// so no documents.
	docs *shower
}

// defaultConfigPath is the config file whose servers grants follow. Tests
// replace it.
var defaultConfigPath = config.DefaultPath

// startAgents serves the agent API on ~/.nearhost/<id>.sock. It returns nil
// and a note saying why when agents cannot reach nearhost from this run,
// and nil with no note when b serves none. When only the grants file cannot
// be used it serves the agents without passthroughs and returns a note too.
// cfg is the config this run loaded and validated, which the grants are
// pruned against; a nil cfg prunes nothing.
func startAgents(b agentsBackend, src agentapi.Source, cfg *config.Config, runDir string) (*agents, string) {
	if b.home == nil || b.id == nil {
		return nil, ""
	}
	id, err := b.id()
	if err != nil {
		return nil, "Coding agents cannot reach Nearhost: " + err.Error() + "."
	}
	home, err := b.home()
	if err != nil {
		return nil, "Coding agents cannot reach Nearhost: " + err.Error() + "."
	}
	laptop := "this machine"
	if b.laptop != nil {
		laptop = b.laptop()
	}
	sock := agentSocket(home, id)
	opts := agentapi.Options{Laptop: laptop, Source: src}
	var store *grants.Store
	var docs *shower
	var note, grantsOff string
	if as, ok := src.(*agentSource); ok && b.grants != nil {
		if path, err := b.grants(); err != nil {
			// Agents are served without passthroughs, and the user is told why.
			note = "Coding agents cannot ask for passthroughs: " + err.Error() + "."
		} else {
			st := grants.Open(path)
			if err := st.Validate(); err != nil {
				// A file that cannot be read would fail every grant check, so
				// agents are served without passthroughs, and the user is told
				// which file to look at.
				grantsOff = fmt.Sprintf("%s cannot be read (%v), so passthrough is off", path, err)
				note = "Coding agents cannot ask for passthroughs: " + grantsOff + "."
			} else {
				store = st
				opts.ID = id
				opts.Passer = passer{eng: &as.eng}
				opts.Grants = passGrants{store}
				docs = newShower(&as.eng)
				opts.Shower = docs
				opts.DocGrants = docGrants{store}
			}
		}
	}
	if d, ok := src.(agentapi.Doctor); ok {
		opts.Doctor = d
	}
	if r, ok := src.(agentapi.Reloader); ok {
		opts.Reload = r
	}
	a := &agents{api: agentapi.New(opts), id: id, runDir: runDir, sock: sock, grants: store, grantsOff: grantsOff, docs: docs}
	if err := a.api.Listen(config.LocalServer, sock); err != nil {
		if docs != nil {
			docs.pool.Close()
		}
		if errors.Is(err, agentapi.ErrInUse) {
			return nil, "Another Nearhost is already serving coding agents on this machine, so this one does not."
		}
		return nil, "Coding agents cannot reach Nearhost: " + err.Error() + "."
	}
	// Only the run that serves agents serves their documents on its page.
	if docs != nil {
		docs.serve()
	}
	// Only a run that serves agents prunes, and against the config the run
	// loaded and validated, never a second read of the file that may have
	// changed since.
	if as, ok := src.(*agentSource); ok && store != nil && cfg != nil {
		if msg := a.prune(as.cfgPath, cfg); msg != "" {
			if note != "" {
				note += " "
			}
			note += msg
		}
	}
	return a, note
}

// forward is the engine's Agent option: it listens on the server's private
// socket and gives the session the forward to ~/.nearhost/<id>.sock there.
func (a *agents) forward(server string) (transport.AgentForward, bool) {
	// Named by a hash, so any server name makes a short plain file name.
	name := fmt.Sprintf("agent-%x", sha256.Sum256([]byte(server)))[:len("agent-")+12]
	local := filepath.Join(a.runDir, name+".sock")
	if err := a.api.Listen(server, local); err != nil {
		// The session would say nothing: it has no forward to fail.
		if eng := a.src.eng.Load(); eng != nil {
			eng.Events().Publish(engine.Event{Server: server, Message: server + ": agents unavailable: " + err.Error()})
		}
		return transport.AgentForward{}, false
	}
	return transport.AgentForward{Name: a.id + ".sock", Local: local, Own: a.sock}, true
}

// agentEnv is what the agents' doctor needs of the run of nearhost.
type agentEnv struct {
	// ssh opens a connection of the doctor's own to a server.
	ssh    sshClient
	doctor doctorEnv
	// restart is true under `nearhost up`, which never re-applies its
	// config, so a reload restarts a running project to apply a change.
	restart bool
}

// serveAgents starts the agent API for a run of nearhost, and sets opts so
// that the engine forwards it to each server. Call attach with the engine
// once it exists. The note says why agents cannot reach nearhost, when
// they cannot; the agents returned are then nil, and their methods do
// nothing. When only passthroughs are off, the agents come back with a
// note that says why. cfg is the config the run loaded from cfgPath and
// validated; the grants of servers it no longer has are pruned against it.
func serveAgents(b agentsBackend, opts *engine.Options, cfg *config.Config, cfgPath string, httpsPort int, env agentEnv) (*agents, string) {
	src := &agentSource{cfgPath: cfgPath, httpsPort: httpsPort, env: env}
	a, note := startAgents(b, src, cfg, opts.RunDir)
	if a != nil {
		a.src = src
		opts.Agent = a.forward
		opts.AgentDone = a.serverGone
	}
	return a, note
}

// serverGone is the engine's AgentDone: server's session has gone for good,
// so its private socket closes, and its documents client is dropped.
func (a *agents) serverGone(server string) {
	a.api.Unlisten(server)
	if a.docs != nil {
		a.docs.pool.Drop(server)
	}
}

// attach gives the status calls the engine's state, and has the documents
// drop the client of a server that disconnects.
func (a *agents) attach(eng *engine.Engine) {
	if a != nil {
		a.src.eng.Store(eng)
		if a.docs != nil {
			a.docs.watch(eng)
		}
	}
}

// review has the interface review the agents' proposals, and the changes
// their reloads find.
func (a *agents) review(to *tui.Proposals) {
	if a != nil {
		a.api.SetReviewer(reviewer{to})
		a.src.reviewed.Store(true)
	}
}

// changeAnswered tells the reloads waiting on the change with hash sha of
// project that the user approved or declined it.
func (a *agents) changeAnswered(project, sha string, approved bool) {
	if a != nil {
		a.api.ChangeAnswered(project, sha, approved)
	}
}

// accepting holds the reloads waiting on the change with hash sha of
// project while the user's approval of it is saved; see
// agentapi.Server.Accepting. done(true) tells them it was approved.
func (a *agents) accepting(project, sha string) (done func(saved bool)) {
	if a == nil {
		return func(bool) {}
	}
	return a.api.Accepting(project, sha)
}

func (a *agents) accept(id, name string) {
	if a != nil {
		a.api.Accept(id, name)
	}
}

func (a *agents) decline(id, reason string) {
	if a != nil {
		a.api.Decline(id, reason)
	}
}

// reviewer hands the agent API's proposals to the interface.
type reviewer struct{ to *tui.Proposals }

func (r reviewer) Review(p agentapi.Proposal) {
	r.to.Propose(tui.Proposal{ID: p.ID, Origin: p.Origin, Path: p.Path, Name: p.Name, Web: p.Web, Expires: p.Expires})
}

// ReviewPassthrough shows the interface a passthrough request.
func (r reviewer) ReviewPassthrough(p agentapi.PassRequest) {
	r.to.Pass(tui.PassRequest{
		ID: p.ID, Origin: p.Origin, Requester: p.Requester, Session: p.Session, Label: p.Label,
		Remote: p.Remote, LocalTaken: p.LocalTaken, CanAlways: p.CanAlways, Expires: p.Expires,
	})
}

// Opened tells the interface of a passthrough that opened without asking.
func (r reviewer) Opened(p agentapi.Passthrough) { r.to.Opened(p.Origin, p.Requester, p.Remote) }

// ReviewDocument shows the interface a request to show a document.
func (r reviewer) ReviewDocument(d agentapi.DocRequest) {
	r.to.Doc(tui.DocRequest{
		ID: d.ID, Origin: d.Origin, Requester: d.Requester, Session: d.Session, Label: d.Label,
		Root: d.Root, Doc: d.Doc, CanAlways: d.CanAlways, Expires: d.Expires,
	})
}

// Shown tells the interface of a document opened without asking.
func (r reviewer) Shown(origin, requester, doc string) { r.to.Shown(origin, requester, doc) }

// passer opens passthroughs on the engine, once it is attached.
type passer struct {
	eng *atomic.Pointer[engine.Engine]
}

// Open forwards p.Remote on p.Origin through the engine.
func (s passer) Open(ctx context.Context, p agentapi.Passthrough) (int, error) {
	eng := s.eng.Load()
	if eng == nil {
		return 0, agentapi.ErrNotConnected
	}
	got, err := eng.OpenPassthrough(ctx, engine.PassthroughSpec{
		ID: p.ID, Server: p.Origin, Requester: p.Requester, Session: p.Session, Label: p.Label, Remote: p.Remote,
	})
	if errors.Is(err, engine.ErrNotConnected) {
		return 0, fmt.Errorf("%w: %w", agentapi.ErrNotConnected, err)
	}
	if err != nil {
		return 0, err
	}
	return got.Local, nil
}

// Close closes the passthrough id, because the agent asked.
func (s passer) Close(ctx context.Context, id string) error {
	eng := s.eng.Load()
	if eng == nil {
		return nil
	}
	return eng.ClosePassthrough(ctx, id, "the agent closed it")
}

// List is origin's open passthroughs.
func (s passer) List(origin string) []agentapi.Passthrough {
	eng := s.eng.Load()
	if eng == nil {
		return nil
	}
	var out []agentapi.Passthrough
	for _, p := range eng.Passthroughs(origin) {
		out = append(out, agentapi.Passthrough{
			ID: p.ID, Origin: p.Server, Requester: p.Requester, Session: p.Session, Label: p.Label,
			Remote: p.Remote, Local: p.Local, Opened: p.Opened,
		})
	}
	return out
}

// Free reports whether port is free on this machine.
func (s passer) Free(port int) bool {
	eng := s.eng.Load()
	return eng != nil && eng.PortFree(port)
}

// passGrants keeps the agent API's grants in the run's store.
type passGrants struct{ s *grants.Store }

// Allow gives requester a passthrough grant on server.
func (g passGrants) Allow(server, requester string) (string, error) {
	return g.s.Allow(server, requester, grants.KindPassthrough)
}

// Check reports whether token is requester's passthrough token on server.
func (g passGrants) Check(server, requester, token string) (bool, error) {
	return g.s.Check(server, requester, grants.KindPassthrough, token)
}

// docGrants keeps the agent API's documents grants in the run's store,
// apart from the passthrough grants.
type docGrants struct{ s *grants.Store }

// Allow gives requester a documents grant on server.
func (g docGrants) Allow(server, requester string) (string, error) {
	return g.s.Allow(server, requester, grants.KindDocuments)
}

// Check reports whether token is requester's documents token on server.
func (g docGrants) Check(server, requester, token string) (bool, error) {
	return g.s.Check(server, requester, grants.KindDocuments, token)
}

// agentAnswer is the interface's answer as the agent API takes it.
func agentAnswer(answer tui.Answer) agentapi.Answer {
	switch answer {
	case tui.AnswerYes:
		return agentapi.AnswerYes
	case tui.AnswerAlways:
		return agentapi.AnswerAlways
	}
	return agentapi.AnswerNo
}

// answerPassthrough tells the agent API the user's answer to request id.
func (a *agents) answerPassthrough(ctx context.Context, id string, answer tui.Answer) error {
	if a == nil {
		return errors.New("no coding agent can reach Nearhost")
	}
	return answerError(a.api.AnswerPassthrough(ctx, id, agentAnswer(answer)))
}

// answerDocument tells the agent API the user's answer to the document
// request id.
func (a *agents) answerDocument(ctx context.Context, id string, answer tui.Answer) error {
	if a == nil {
		return errors.New("no coding agent can reach Nearhost")
	}
	return answerError(a.api.AnswerDocument(ctx, id, agentAnswer(answer)))
}

// answerError is the agent API's error for an answer, marked for the
// interface: as the end of its request, and as an Always allow that was not
// saved. Both marks apply when the request ended too.
func answerError(err error) error {
	if errors.Is(err, agentapi.ErrAllowNotSaved) {
		err = notSaved{err}
	}
	if errors.Is(err, agentapi.ErrPassEnded) {
		err = tui.RequestEnded(err)
	}
	return err
}

// prune removes the grants and served trees of servers cfg no longer has,
// and returns the message about the grants to show the user when it removed
// some or could not. It is "" when there is nothing to say. It must only be
// given a config that loaded or applied, because a config with no servers
// removes every grant and tree.
//
// Only the default config, and only while its file exists, decides which
// servers keep their grants: grants.json is one file for the machine, so a
// run with another --config, or a config file missing for a moment, must not
// remove grants of servers that are still configured elsewhere. Trees are
// this run's own, so they follow cfg whichever file it came from.
func (a *agents) prune(cfgPath string, cfg *config.Config) string {
	if a == nil || cfg == nil {
		return ""
	}
	keep := []string{config.LocalServer}
	for _, s := range cfg.Servers {
		keep = append(keep, s.Name)
	}
	a.pruneTrees(keep)
	if a.grants == nil || cfgPath == "" {
		return ""
	}
	if def, err := defaultConfigPath(); err != nil || def != cfgPath {
		return ""
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return ""
	}
	n, err := a.grants.Prune(keep)
	switch {
	case err != nil:
		return "Could not remove Always allow grants for servers no longer in Nearhost: " + err.Error() + "."
	case n == 1:
		return "Removed 1 Always allow grant for a server no longer in Nearhost."
	case n > 1:
		return fmt.Sprintf("Removed %d Always allow grants for servers no longer in Nearhost.", n)
	}
	return ""
}

// pruneTrees stops serving the trees of servers not in keep, and drops their
// documents clients.
func (a *agents) pruneTrees(keep []string) {
	if a.docs == nil {
		return
	}
	var gone []string
	for _, tr := range a.docs.table.List("") {
		if !slices.Contains(keep, tr.Server) && !slices.Contains(gone, tr.Server) {
			gone = append(gone, tr.Server)
		}
	}
	a.docs.table.Prune(keep)
	for _, s := range gone {
		a.docs.pool.Drop(s)
	}
}

// grantsErr says why grants cannot be listed or changed in this run, or is
// nil when they can.
func (a *agents) grantsErr() error {
	if a != nil && a.grantsOff != "" {
		return errors.New("grants are off: " + a.grantsOff)
	}
	if a == nil || a.grants == nil {
		return errors.New("Nearhost isn't serving coding agents in this run, so grants can't be changed here")
	}
	return nil
}

// grantsOf are the grants of every kind on server.
func (a *agents) grantsOf(server string) ([]grants.Grant, error) {
	if err := a.grantsErr(); err != nil {
		return nil, err
	}
	return a.grants.List(server)
}

// revokeGrant takes back requester's grant of kind on server.
func (a *agents) revokeGrant(server, requester, kind string) error {
	if err := a.grantsErr(); err != nil {
		return err
	}
	return a.grants.Revoke(server, requester, kind)
}

// errNoDocuments says documents can't be opened or removed in this run.
var errNoDocuments = errors.New("Nearhost isn't serving documents in this run")

// documents are the trees served on server, newest first, or on every
// server when it is empty; none when this run serves no documents.
func (a *agents) documents(server string) []documents.Tree {
	if a == nil || a.docs == nil {
		return nil
	}
	return a.docs.table.List(server)
}

// openDocument opens the newest document of the tree id in the browser.
func (a *agents) openDocument(id string) error {
	if a == nil || a.docs == nil {
		return errNoDocuments
	}
	return a.docs.openNewest(id)
}

// removeDocuments stops serving the tree id.
func (a *agents) removeDocuments(id string) error {
	if a == nil || a.docs == nil {
		return errNoDocuments
	}
	a.docs.table.Remove(id)
	return nil
}

// close stops serving agents and their documents, and removes the sockets
// on this machine.
func (a *agents) close() {
	if a != nil {
		a.api.Close()
		if a.docs != nil {
			a.docs.close()
		}
	}
}

// agentSource answers the agents' status calls from the config file, read
// afresh each time, and the engine's state. eng is set once the engine
// exists; until then every project reads as stopped.
type agentSource struct {
	cfgPath   string
	httpsPort int
	env       agentEnv
	eng       atomic.Pointer[engine.Engine]
	// reviewed is set once the interface reviews what agents ask for, so a
	// reload can point the user at a change that waits.
	reviewed atomic.Bool

	// openFiles opens a server's files for the doctor; nil opens them with
	// env.ssh. Tests set it, and projects, to fakes.
	openFiles func(ctx context.Context, srv config.Server) (serverFiles, error)
	// projects stands in for the engine when not nil.
	projects projectEngine
}

// projectEngine is what the doctor and reload need of the engine.
type projectEngine interface {
	Status(name string) engine.ProjectStatus
	Reread(ctx context.Context, name string) (engine.Review, error)
	StopProject(ctx context.Context, name string) error
	StartProject(ctx context.Context, name string) error
}

// engine is the engine, or nil before it is attached.
func (s *agentSource) engine() projectEngine {
	if s.projects != nil {
		return s.projects
	}
	if eng := s.eng.Load(); eng != nil {
		return eng
	}
	return nil
}

func (s *agentSource) Projects(origin string) []agentapi.Project {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil
	}
	eng := s.eng.Load()
	var out []agentapi.Project
	for _, p := range cfg.Projects {
		if p.Server != origin {
			continue
		}
		st := engine.ProjectStatus{State: engine.StateStopped}
		if eng != nil {
			st = eng.Status(p.Name)
		}
		out = append(out, agentProject(p, st, s.httpsPort))
	}
	return out
}

// doctorAgain is what an agent does once ssh lets Nearhost in.
const doctorAgain = "run /nearhost doctor again"

// Doctor checks a configured project, or the directory dir on origin's
// machine as nearhost add would make it. A configured project's .nearhostrc
// is re-read first, as by Reload, so a change that needs no approval is
// applied, and one that does is shown in the interface, before the findings
// are worked out from the config as it then is. The server's files are
// opened for the call, and closed once it ends, which also ends any read
// the checks are still waiting on when ctx ends first.
func (s *agentSource) Doctor(ctx context.Context, origin, name, dir string) ([]doctor.Finding, error) {
	eng := s.engine()
	running := false
	if name != "" && eng != nil {
		// A server not connected can't be re-read; its findings say what
		// is in its files all the same.
		_, _ = s.reread(ctx, eng, name)
		running = eng.Status(name).State == engine.StateRunning
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, err
	}
	srv, ok := cfg.Server(origin)
	if !ok {
		return nil, fmt.Errorf("no server called %q", origin)
	}
	var p config.Project
	if name != "" {
		found, ok := cfg.Project(name)
		if !ok {
			return nil, fmt.Errorf("no project called %q", name)
		}
		p = *found
	}
	open := s.openFiles
	if open == nil {
		open = func(ctx context.Context, srv config.Server) (serverFiles, error) {
			return openFiles(ctx, s.env.ssh, srv, doctorAgain)
		}
	}
	files, err := open(ctx, srv)
	if err != nil {
		return nil, err
	}
	defer files.close()

	type result struct {
		findings []doctor.Finding
		err      error
	}
	done := make(chan result, 1)
	go func() {
		resolved, err := files.resolve(dir)
		if err != nil {
			done <- result{err: notThere{err}}
			return
		}
		reader := files.reader(resolved)
		if name == "" {
			p = draftProject(cfg, s.cfgPath, srv, resolved, reader)
		}
		done <- result{findings: projectFindings(ctx, s.env.doctor, cfg, s.cfgPath, p, name != "", running, reader)}
	}()
	select {
	case r := <-done:
		return r.findings, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("the checks did not finish in time: %w", ctx.Err())
	}
}

// reread re-reads the .nearhostrc of the project called name. A change
// that needs no approval has been saved by the review; the interface's
// config poll applies it, but `nearhost up` never re-applies its config, so
// there a running project is restarted, which adopts the saved change as it
// starts. The restart outlives the call: an agent that hangs up must not
// leave the project stopped.
func (s *agentSource) reread(ctx context.Context, eng projectEngine, name string) (engine.Review, error) {
	r, err := eng.Reread(ctx, name)
	if err != nil {
		return r, err
	}
	if r.Updated != nil && s.env.restart && eng.Status(name).State == engine.StateRunning {
		ctx := context.WithoutCancel(ctx)
		if err := eng.StopProject(ctx, name); err != nil {
			return r, err
		}
		if err := eng.StartProject(ctx, name); err != nil {
			return r, err
		}
	}
	return r, nil
}

// Reload re-reads the .nearhostrc of the project called name, and says what
// it found.
func (s *agentSource) Reload(ctx context.Context, name string) (agentapi.Reloaded, error) {
	eng := s.engine()
	if eng == nil {
		return agentapi.Reloaded{}, errors.New("nearhost is still starting; try again")
	}
	r, err := s.reread(ctx, eng, name)
	if err != nil {
		return agentapi.Reloaded{}, err
	}
	st := eng.Status(name)
	out := agentapi.Reloaded{Problem: st.Notice}
	switch {
	case st.Pending != nil:
		out.SHA256 = st.Pending.SHA256
		out.Items = make([]string, len(st.Pending.Items))
		for i, it := range st.Pending.Items {
			out.Items[i] = it.Name()
		}
		if e := s.eng.Load(); e != nil && s.reviewed.Load() {
			e.Events().Publish(engine.Event{Project: name,
				Message: "A coding agent asks you to approve what its .nearhostrc asks for: select " + name + " and press a."})
		}
	case r.Pending != nil:
		// The engine drops a change the user declined, so nothing waits
		// though the file asks for it.
		out.Declined = true
	}
	return out, nil
}

// Waiting is the hash of the change of the project's .nearhostrc that
// waits for approval, or "" when none does or the engine is not attached.
func (s *agentSource) Waiting(name string) string {
	eng := s.engine()
	if eng == nil {
		return ""
	}
	if c := eng.Status(name).Pending; c != nil {
		return c.SHA256
	}
	return ""
}

// notThere is a directory that could not be resolved, which the agent API
// answers as not there. It reads as the reason.
type notThere struct{ err error }

func (e notThere) Error() string   { return e.err.Error() }
func (e notThere) Unwrap() []error { return []error{e.err, fs.ErrNotExist} }

// agentProject is p as an agent sees it, given its engine status. A web
// route is applied only when the engine has a local port for it, since a
// project with several web routes can serve some and not others.
func agentProject(p config.Project, st engine.ProjectStatus, httpsPort int) agentapi.Project {
	running := st.State == engine.StateRunning
	ap := agentapi.Project{
		Name: p.Name, Path: p.Path, URL: siteURL(p.Alias, httpsPort), State: string(st.State),
		URLs: []string{}, Routes: []agentapi.Route{},
	}
	if st.State == engine.StateError || st.State == engine.StateDegraded {
		ap.Error = st.Message
	}
	web := p.Web()
	for _, w := range web {
		for _, h := range w.Hosts {
			ap.URLs = append(ap.URLs, siteURL(h, httpsPort))
		}
	}
	for _, r := range p.Routes {
		applied := running && r.Role != config.RoleIgnore
		route := agentapi.Route{Port: r.Port, Role: string(r.Role), Label: r.Label}
		if r.Role == config.RoleWeb {
			if ap.Web == 0 {
				ap.Web = r.Port
			}
			applied = applied && st.WebLocalPorts[r.Port] != 0
			for _, w := range web {
				if w.Port == r.Port {
					route.Hosts = w.Hosts
					break
				}
			}
		}
		route.Applied = applied
		ap.Routes = append(ap.Routes, route)
	}
	return ap
}

// siteURL is where the browser reaches alias.
func siteURL(alias string, httpsPort int) string {
	if httpsPort == 0 || httpsPort == 443 {
		return "https://" + alias
	}
	return "https://" + alias + ":" + strconv.Itoa(httpsPort)
}

// notSaved is an error marked as tui.ErrAllowNotSaved; it reads as the error
// it wraps.
type notSaved struct{ error }

// Unwrap is the error and tui.ErrAllowNotSaved.
func (e notSaved) Unwrap() []error { return []error{e.error, tui.ErrAllowNotSaved} }
