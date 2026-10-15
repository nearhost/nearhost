package tui

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/transport"
)

// problemModel has a server that cannot connect and a project in error.
func problemModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	cfg := sampleConfig()
	cfg.Servers = append(cfg.Servers, config.Server{Name: "vps", SSHHost: "deploy@vps.example"})
	cfg.Projects = append(cfg.Projects, config.Project{Name: "clientsite", Server: "vps", Path: "/srv/c", Alias: "c.test", Enabled: true,
		Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}})
	f := newFake(t, cfg)
	f.servers["mini"] = transport.StatusConnected
	f.servers["vps"] = transport.StatusAuthFailed
	f.srvErrs["vps"] = "Permission denied (publickey)"
	f.setState("website", engine.ProjectStatus{State: engine.StateError, Message: "port 8080 is not answering"})
	return sized(f), f
}

// Home lists what needs the user, and says how to fix each.
func TestHomeShowsProblems(t *testing.T) {
	m, _ := problemModel(t)
	wantScreen(t, m, "problems ───", "○ vps  auth failed", "why       Permission denied (publickey)",
		"fix       Run ssh deploy@vps.example once in a terminal to fix", "blocks    clientsite",
		"● website  error", "why       port 8080 is not answering", "r retry now · y copy command",
		"· 2 problems", " r retry · / filter")
}

// r on Home reconnects every server that cannot connect; y copies the ssh
// command that fixes access.
func TestHomeRetriesAndCopies(t *testing.T) {
	m, f := problemModel(t)
	run(m, press(m, "r"))
	if strings.Join(f.edit.reconnected, ",") != "vps" {
		t.Errorf("reconnected %v", f.edit.reconnected)
	}
	if got := clipboardOf(press(m, "y")); got != "ssh deploy@vps.example" {
		t.Errorf("copied %q", got)
	}
}

// With nothing wrong, Home has no problems and the header no count.
func TestHomeWithoutProblems(t *testing.T) {
	m := sized(newFake(t, sampleConfig()))
	if s := screen(m); strings.Contains(s, "problem") || strings.Contains(s, "retry") {
		t.Errorf("problems shown:\n%s", s)
	}
}

// More problems than the window holds are cut, and the screen keeps its
// size.
func TestHomeWithManyProblems(t *testing.T) {
	cfg := sampleConfig()
	f := newFake(t, cfg)
	for i := 0; i < 12; i++ {
		name := "srv" + string(rune('a'+i))
		cfg.Servers = append(cfg.Servers, config.Server{Name: name, SSHHost: name + ".example"})
		f.servers[name] = transport.StatusAuthFailed
		f.srvErrs[name] = "Permission denied (publickey)"
	}
	m := sized(f)
	if got := strings.Count(screen(m), "\n") + 1; got != 30 {
		t.Errorf("drew %d lines", got)
	}
	wantScreen(t, m, "· 12 problems", "○ srva  auth failed")
}
