package cli

import (
	"errors"
	"sync"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/transport"
)

// mirror holds the last state the core gave, which the interface reads with
// no call to the core. Its config is parsed once each time the core's YAML
// changes. It is safe for concurrent use.
type mirror struct {
	mu    sync.Mutex
	state control.State
	// cfg is the config last parsed, from the YAML in cfgText.
	cfg     *config.Config
	cfgText string
}

// newMirror returns a mirror that holds st.
func newMirror(st control.State) *mirror {
	m := &mirror{cfg: &config.Config{}}
	m.setLocked(st)
	return m
}

// take holds st, unless the mirror holds a state newer than it, and reports
// whether it did. A state as new as the one held is taken: it may carry
// changes that publish no envelope.
func (m *mirror) take(st control.State) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st.Seq < m.state.Seq {
		return false
	}
	m.setLocked(st)
	return true
}

// replace holds st whatever its number, as a reset asks: the numbers the
// mirror had may belong to a core that has gone.
func (m *mirror) replace(st control.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setLocked(st)
}

// setLocked holds st, parsing its config when the YAML has changed. YAML
// that does not parse leaves the config last parsed.
func (m *mirror) setLocked(st control.State) {
	m.state = st
	if st.Config == m.cfgText {
		return
	}
	m.cfgText = st.Config
	if cfg, err := config.Parse([]byte(st.Config)); err == nil {
		m.cfg = cfg
	}
}

// seq is the number of the envelope the state held is at least as new as.
func (m *mirror) seq() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Seq
}

// config is the config the core runs.
func (m *mirror) config() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mirror) configPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.ConfigPath
}

func (m *mirror) ports() (https, http, web int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.HTTPSPort, m.state.HTTPPort, m.state.WebPort
}

// status is a project's state; one the core does not run is stopped, as the
// engine says.
func (m *mirror) status(project string) engine.ProjectStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.state.Projects[project]; ok {
		return st
	}
	return engine.ProjectStatus{State: engine.StateStopped}
}

// server is a server's connection. This machine is always connected, and a
// server the core has not listed is disconnected, as the engine says.
func (m *mirror) server(name string) control.ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.state.Servers[name]; ok {
		return s
	}
	if name == config.LocalServer {
		return control.ServerState{Health: engine.ServerHealth{Health: transport.Health{Status: transport.StatusConnected}}, Status: transport.StatusConnected}
	}
	return control.ServerState{Health: engine.ServerHealth{Health: transport.Health{Status: transport.StatusDisconnected}}, Status: transport.StatusDisconnected}
}

// passthroughs are the open passthroughs of server, or of every server when
// it is empty.
func (m *mirror) passthroughs(server string) []engine.Passthrough {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []engine.Passthrough
	for _, p := range m.state.Passthroughs {
		if server == "" || p.Server == server {
			out = append(out, p)
		}
	}
	return out
}

// documents are the trees served on server, newest first, or on every server
// when it is empty.
func (m *mirror) documents(server string) []documents.Tree {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []documents.Tree
	for _, t := range m.state.Documents {
		if server == "" || t.Server == server {
			out = append(out, t)
		}
	}
	return out
}

// grants are the grants given on server, or why the core cannot read them.
// An empty server matches none.
func (m *mirror) grants(server string) ([]grants.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.GrantsProblem != nil {
		p := *m.state.GrantsProblem
		return nil, &p
	}
	if m.state.GrantsError != "" {
		// A core from before messages in the state.
		return nil, errors.New(m.state.GrantsError)
	}
	var out []grants.Grant
	for _, g := range m.state.Grants {
		if server != "" && g.Server == server {
			out = append(out, g)
		}
	}
	return out, nil
}

// clients is how many clients follow the core's stream, this one included.
func (m *mirror) clients() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Clients
}
