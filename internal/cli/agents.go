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
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/engine"
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
}

// systemAgents is the production backend. Only NewRoot wires it in.
func systemAgents() agentsBackend {
	return agentsBackend{home: os.UserHomeDir, id: config.InstallID, laptop: laptopName}
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
}

// startAgents serves the agent API on ~/.nearhost/<id>.sock. It returns nil
// and a note saying why when agents cannot reach nearhost from this run,
// and nil with no note when b serves none.
func startAgents(b agentsBackend, src agentapi.Source, runDir string) (*agents, string) {
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
	if d, ok := src.(agentapi.Doctor); ok {
		opts.Doctor = d
	}
	if r, ok := src.(agentapi.Reloader); ok {
		opts.Reload = r
	}
	a := &agents{api: agentapi.New(opts), id: id, runDir: runDir, sock: sock}
	if err := a.api.Listen(config.LocalServer, sock); err != nil {
		if errors.Is(err, agentapi.ErrInUse) {
			return nil, "Another Nearhost is already serving coding agents on this machine, so this one does not."
		}
		return nil, "Coding agents cannot reach Nearhost: " + err.Error() + "."
	}
	return a, ""
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
// once it exists. The note says why agents cannot reach nearhost,
// when they cannot; the agents returned are then nil, and their methods do
// nothing.
func serveAgents(b agentsBackend, opts *engine.Options, cfgPath string, httpsPort int, env agentEnv) (*agents, string) {
	src := &agentSource{cfgPath: cfgPath, httpsPort: httpsPort, env: env}
	a, note := startAgents(b, src, opts.RunDir)
	if a != nil {
		a.src = src
		opts.Agent = a.forward
		opts.AgentDone = a.api.Unlisten
	}
	return a, note
}

// attach gives the status calls the engine's state.
func (a *agents) attach(eng *engine.Engine) {
	if a != nil {
		a.src.eng.Store(eng)
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

// close stops serving agents and removes the sockets on this machine.
func (a *agents) close() {
	if a != nil {
		a.api.Close()
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
