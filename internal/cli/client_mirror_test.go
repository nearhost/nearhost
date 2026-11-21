package cli

import (
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/documents"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/grants"
	"github.com/nearhost/nearhost/internal/transport"
)

// The reads answer from the last state, with no call: they go on answering
// once the core has gone.
func TestMirrorAnswersWithoutIO(t *testing.T) {
	h := newMirrorHarness(t)
	opened := time.Now().Add(-time.Minute).Truncate(time.Second)
	h.core.set(func(st *control.State) {
		st.Projects["web"] = engine.ProjectStatus{State: engine.StateRunning, Message: "up"}
		st.Servers["mini"] = control.ServerState{
			Health: engine.ServerHealth{Health: transport.Health{Status: transport.StatusConnected}, Held: true},
			Status: transport.StatusConnected, Message: "fine",
		}
		st.Passthroughs = []engine.Passthrough{
			{PassthroughSpec: engine.PassthroughSpec{ID: "x1", Server: "mini", Remote: 5173}, Local: 5173, Opened: opened},
			{PassthroughSpec: engine.PassthroughSpec{ID: "x2", Server: "vps", Remote: 3000}, Local: 3000, Opened: opened},
		}
		st.Documents = []documents.Tree{{ID: "t2", Server: "mini"}, {ID: "t1", Server: "vps"}, {ID: "t0", Server: "mini"}}
		st.Grants = []grants.Grant{{Server: "mini", Requester: "claude-code", Kind: "passthrough"}, {Server: "vps", Requester: "codex", Kind: "document"}}
	})
	b := h.backend(t, h.state(t), nil, nil)
	h.srv.Close()

	if got := b.Status("web"); got.State != engine.StateRunning || got.Message != "up" {
		t.Errorf("Status = %+v", got)
	}
	if got := b.Status("gone"); got.State != engine.StateStopped {
		t.Errorf("Status of an unknown project = %v, want stopped", got.State)
	}
	if cfg := b.Config(); len(cfg.Projects) != 1 || cfg.Projects[0].Name != "web" {
		t.Errorf("Config = %+v", cfg)
	}
	if got := b.ServerHealth("mini"); !got.Held || got.Status != transport.StatusConnected {
		t.Errorf("ServerHealth = %+v", got)
	}
	if status, msg := b.ServerStatus("mini"); status != transport.StatusConnected || msg != "fine" {
		t.Errorf("ServerStatus = %v, %q", status, msg)
	}
	if status, _ := b.ServerStatus(config.LocalServer); status != transport.StatusConnected {
		t.Errorf("ServerStatus of this machine = %v", status)
	}
	if status, _ := b.ServerStatus("other"); status != transport.StatusDisconnected {
		t.Errorf("ServerStatus of an unknown server = %v", status)
	}
	if got := b.ConfigPath(); got != "/home/me/.config/nearhost/config.yaml" {
		t.Errorf("ConfigPath = %q", got)
	}
	if https, http := b.Ports(); https != 8443 || http != 8080 || b.WebPort() != 6327 {
		t.Errorf("Ports = %d, %d, WebPort %d", https, http, b.WebPort())
	}
	if got := b.Passthroughs("mini"); len(got) != 1 || got[0].ID != "x1" || !got[0].Opened.Equal(opened) {
		t.Errorf("Passthroughs(mini) = %+v", got)
	}
	if got := b.Passthroughs(""); len(got) != 2 {
		t.Errorf("Passthroughs() = %+v", got)
	}
	if got := b.Documents("mini"); len(got) != 2 || got[0].ID != "t2" || got[1].ID != "t0" {
		t.Errorf("Documents(mini) = %+v", got)
	}
	if got, err := b.Grants("mini"); err != nil || len(got) != 1 || got[0].Requester != "claude-code" {
		t.Errorf("Grants(mini) = %+v, %v", got, err)
	}
	if got, err := b.Grants(""); err != nil || len(got) != 0 {
		t.Errorf("Grants() = %+v, %v", got, err)
	}
}

// Grants say why they cannot be read when the core says so.
func TestMirrorGrantsError(t *testing.T) {
	m := newMirror(control.State{GrantsError: "grants are off: the file is broken"})
	if _, err := m.grants("mini"); err == nil || err.Error() != "grants are off: the file is broken" {
		t.Errorf("Grants = %v", err)
	}
}

// The mirror parses the config once per change, and takes no state older
// than the one it has.
func TestMirrorTakesNewerStates(t *testing.T) {
	data, err := config.Marshal(&config.Config{Projects: []config.Project{localProject("web", 8000, true)}})
	if err != nil {
		t.Fatal(err)
	}
	m := newMirror(control.State{Seq: 5, Config: string(data)})
	first := m.config()
	if !m.take(control.State{Seq: 6, Config: string(data)}) || m.config() != first {
		t.Error("the same config was parsed again")
	}
	if m.take(control.State{Seq: 4}) || m.seq() != 6 {
		t.Errorf("an older state was taken: at %d", m.seq())
	}
	other, _ := config.Marshal(&config.Config{})
	if !m.take(control.State{Seq: 7, Config: string(other)}) || len(m.config().Projects) != 0 {
		t.Error("a changed config was not parsed")
	}
	// A config that does not parse leaves the last one that did.
	m.take(control.State{Seq: 8, Config: "projects: ["})
	if m.config() == nil {
		t.Error("no config after one that does not parse")
	}
}
