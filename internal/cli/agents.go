package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/nearhost/nearhost/internal/agentapi"
	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
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

// laptopName is the user and the short host name, as in ecs@macbook.
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
	a := &agents{api: agentapi.New(agentapi.Options{Laptop: laptop, Source: src}), id: id, runDir: runDir}
	sock := filepath.Join(home, transport.AgentDir, id+".sock")
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
		return transport.AgentForward{}, false
	}
	return transport.AgentForward{Name: a.id + ".sock", Local: local}, true
}

// serveAgents starts the agent API for a run of nearhost and has the
// engine opts are for forward it to each server. Call attach with the
// engine once it exists. The note says why agents cannot reach nearhost,
// when they cannot; the agents returned are then nil, and their methods do
// nothing.
func serveAgents(b agentsBackend, opts *engine.Options, cfgPath string, httpsPort int) (*agents, string) {
	src := &agentSource{cfgPath: cfgPath, httpsPort: httpsPort}
	a, note := startAgents(b, src, opts.RunDir)
	if a != nil {
		a.src = src
		opts.Agent = a.forward
	}
	return a, note
}

// attach gives the status calls the engine's state.
func (a *agents) attach(eng *engine.Engine) {
	if a != nil {
		a.src.eng.Store(eng)
	}
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
	eng       atomic.Pointer[engine.Engine]
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
		running := st.State == engine.StateRunning
		ap := agentapi.Project{
			Name: p.Name, Path: p.Path, URL: siteURL(p.Alias, s.httpsPort), State: string(st.State),
			Routes: []agentapi.Route{},
		}
		if st.State == engine.StateError || st.State == engine.StateDegraded {
			ap.Error = st.Message
		}
		for _, r := range p.Routes {
			applied := running && r.Role != config.RoleIgnore
			if r.Role == config.RoleWeb {
				ap.Web = r.Port
				applied = applied && st.WebLocalPort != 0
			}
			ap.Routes = append(ap.Routes, agentapi.Route{Port: r.Port, Role: string(r.Role), Label: r.Label, Applied: applied})
		}
		out = append(out, ap)
	}
	return out
}

// siteURL is where the browser reaches alias.
func siteURL(alias string, httpsPort int) string {
	if httpsPort == 0 || httpsPort == 443 {
		return "https://" + alias
	}
	return "https://" + alias + ":" + strconv.Itoa(httpsPort)
}
