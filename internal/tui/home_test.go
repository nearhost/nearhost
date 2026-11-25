package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/engine"
	"github.com/nearhost/nearhost/internal/report"
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
	wantScreen(t, m, "problems ───", "✕ vps  auth failed", "why       Permission denied (publickey)",
		"fix       To fix access, run ssh once in a terminal:", "ssh deploy@vps.example", "Then reconnect the server.", "blocks    clientsite",
		"✕ website  error", "why       port 8080 is not answering", "r retry now · y copy command",
		"· 2 problems", " r retry · l log · / filter")
}

// A run directory too long for ssh is fixed by neither r nor ssh in a
// terminal, so Home offers neither and r reconnects nothing.
func TestHomeOffersNoRetryForARunDirectoryTooLong(t *testing.T) {
	m, f := problemModel(t)
	f.srvErrs["vps"] = socketPathReason
	f.health["vps"] = engine.ServerHealth{Health: transport.Health{
		Problem: report.Errorf(report.SSHSocketPathTooLong, "%s", socketPathReason).Then(transport.SocketPathFix)}}
	f.setState("website", engine.ProjectStatus{State: engine.StateRunning})
	wantScreen(t, m, "✕ vps  auth failed", "blocks    clientsite")
	wantMain(t, m, "Set XDG_CONFIG_HOME to a shorter path")
	for _, not := range []string{"r retry", "copy command", "ssh deploy@vps.example"} {
		if strings.Contains(screen(m), not) {
			t.Errorf("Home shows %q:\n%s", not, screen(m))
		}
	}
	run(m, press(m, "r"))
	if len(f.edit.reconnected) != 0 {
		t.Errorf("r reconnected %v", f.edit.reconnected)
	}
	if cmd := press(m, "y"); cmd != nil && clipboardOf(cmd) != "" {
		t.Errorf("y copied %q", clipboardOf(cmd))
	}
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
	wantScreen(t, m, "· 12 problems", "✕ srva  auth failed")
}

// A server that gave up says how often it tried; one that is reconnecting
// counts down to its next attempt.
func TestServerAttemptsAndNextTry(t *testing.T) {
	m, f := problemModel(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	f.health["vps"] = engine.ServerHealth{Health: transport.Health{Failures: 3}}
	wantScreen(t, m, "✕ vps  auth failed · tried 3 times", "Then reconnect the server.")
	f.servers["vps"] = transport.StatusReconnecting
	f.health["vps"] = engine.ServerHealth{Health: transport.Health{Failures: 3, NextAttempt: now.Add(15500 * time.Millisecond)}}
	wantScreen(t, m, "◐ vps  reconnecting · attempt 4 · next in 16s")
	wantMain(t, m, "Nearhost tries again by itself.")
	now = now.Add(20 * time.Second)
	wantScreen(t, m, "reconnecting · attempt 4 · trying now")
	m.sel = rowKey(rowServer, "vps")
	wantScreen(t, m, " reconnecting · attempt 4 · trying now", "why       Permission denied (publickey)") // the dot spins
}

// A server that connected again does not show its old error.
func TestConnectedServerHidesItsOldError(t *testing.T) {
	m, f := problemModel(t)
	f.servers["vps"] = transport.StatusConnected
	m.sel = rowKey(rowServer, "vps")
	if s := screen(m); strings.Contains(s, "Permission denied") || strings.Contains(s, "fix ") {
		t.Errorf("a connected server shows its old error:\n%s", s)
	}
}

// Each problem Home lists shows the fix steps of its message: the code
// that found the problem wrote them, and the interface adds only its keys.
func TestProblemsComeFromMessages(t *testing.T) {
	bind := report.Errorf(report.PortNeedsPermission, "HTTPS port 443 needs permission to bind.").
		Step("Give this copy of nearhost permission to use ports below 1024, such as 443 and 80:", doctor.SetcapFix("/usr/bin/nearhost")).
		Then("Do not run nearhost with sudo.")
	refused := (&transport.AgentError{Problem: transport.AgentRefused, Msg: "the server refused the agent socket"}).Report()
	notReady := (&transport.AgentError{Problem: transport.AgentNotReady, Msg: "~/.nearhost could not be made"}).Report()
	auth := transport.ConnectionProblem(transport.StatusAuthFailed, "Permission denied (publickey)", "vps", "deploy@vps.example")
	socket := report.Errorf(report.SSHSocketPathTooLong, "%s", socketPathReason).Then(transport.SocketPathFix)
	inUse := report.Errorf(report.PortInUse, "loopback port 5173 is not forwarded").
		Then("Quit the program using port 5173 on this machine, then start the project again.")
	cases := []struct {
		name  string
		setup func(m *Model, f *fakeBackend)
		want  []string // on Home, as text
		not   []string
		sel   string   // a row whose panel shows the problem too
		panel []string // that panel, as text
	}{
		{name: "proxy cannot bind", setup: func(m *Model, f *fakeBackend) {
			m.Update(startedMsg{fmt.Errorf("start proxy: %w", bind)})
		}, want: []string{"✕ proxy down", "HTTPS port 443 needs permission to bind.",
			"Give this copy of nearhost permission to use ports below 1024, such as 443 and 80:",
			doctor.SetcapFix("/usr/bin/nearhost"), "Do not run nearhost with sudo.", "r tries again"}},
		{name: "agents refused", setup: func(m *Model, f *fakeBackend) {
			f.health["mini"] = engine.ServerHealth{Health: transport.Health{AgentErr: refused.Title, AgentReport: refused}}
		}, want: []string{"mini agents unavailable", "the server refused the agent socket",
			"Set AllowStreamLocalForwarding yes in the server's sshd_config, then reconnect the server."},
			sel: rowKey(rowServer, "mini"), panel: []string{"Set AllowStreamLocalForwarding yes", "r reconnect"}},
		{name: "agents not ready", setup: func(m *Model, f *fakeBackend) {
			f.health["mini"] = engine.ServerHealth{Health: transport.Health{AgentErr: notReady.Title, AgentReport: notReady}}
		}, want: []string{"Check that the server account can write ~/.nearhost, then reconnect the server."}},
		{name: "server cannot log in", setup: func(m *Model, f *fakeBackend) {
			f.health["vps"] = engine.ServerHealth{Health: transport.Health{Problem: auth}}
		}, want: []string{"✕ vps auth failed", "Permission denied (publickey)", "To fix access, run ssh once in a terminal:",
			"ssh deploy@vps.example", "Then reconnect the server.", "blocks clientsite", "r retry now · y copy command"},
			sel: rowKey(rowServer, "vps"), panel: []string{"To fix access, run ssh once in a terminal:", "ssh deploy@vps.example", "r reconnect"}},
		{name: "run directory too long", setup: func(m *Model, f *fakeBackend) {
			f.srvErrs["vps"] = socketPathReason
			f.health["vps"] = engine.ServerHealth{Health: transport.Health{Problem: socket}}
		}, want: []string{"✕ vps auth failed", transport.SocketPathFix}, not: []string{"r retry", "copy command"},
			sel: rowKey(rowServer, "vps"), panel: []string{transport.SocketPathFix}},
		{name: "project in error", setup: func(m *Model, f *fakeBackend) {
			f.setState("myapp", engine.ProjectStatus{State: engine.StateError, Message: inUse.Title, Problem: inUse})
		}, want: []string{"✕ myapp error", "loopback port 5173 is not forwarded",
			"Quit the program using port 5173 on this machine, then start the project again."},
			sel: rowKey(rowProject, "myapp"), panel: []string{"Quit the program using port 5173", "enter start it again"}},
		{name: "project in error without a message", setup: func(m *Model, f *fakeBackend) {
			f.setState("myapp", engine.ProjectStatus{State: engine.StateError, Message: "port 8080 is not answering"})
		}, want: []string{"✕ myapp error", "port 8080 is not answering"},
			sel: rowKey(rowProject, "myapp"), panel: []string{"port 8080 is not answering", "enter start it again"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := sampleConfig()
			cfg.Servers = append(cfg.Servers, config.Server{Name: "vps", SSHHost: "deploy@vps.example"})
			cfg.Projects = append(cfg.Projects, config.Project{Name: "clientsite", Server: "vps", Path: "/srv/c", Alias: "c.test", Enabled: true,
				Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}})
			f := newFake(t, cfg)
			f.servers["mini"] = transport.StatusConnected
			f.servers["vps"] = transport.StatusConnected
			m := sized(f)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
			if c.name == "server cannot log in" || c.name == "run directory too long" {
				f.servers["vps"] = transport.StatusAuthFailed
				f.srvErrs["vps"] = "Permission denied (publickey)"
				if c.name == "run directory too long" {
					f.srvErrs["vps"] = socketPathReason
				}
			}
			c.setup(m, f)
			wantMain(t, m, c.want...)
			text := mainText(m)
			for _, not := range c.not {
				if strings.Contains(text, not) {
					t.Errorf("Home shows %q:\n%s", not, screen(m))
				}
			}
			if c.sel != "" {
				m.sel = c.sel
				wantMain(t, m, c.panel...)
			}
		})
	}
}

// A message whose title already holds its advice, as an older text did,
// shows the advice once.
func TestProblemAdviceShowsOnce(t *testing.T) {
	f := newFake(t, sampleConfig())
	const advice = "Clear it with: attrib -r \"C:\\hosts\" (in an administrator terminal), or run with --no-hosts."
	problem := report.Errorf(report.HostsReadOnly, "project \"website\"'s hosts are not resolvable: C:\\hosts is read-only. %s", advice).
		Then(advice)
	f.setState("website", engine.ProjectStatus{State: engine.StateStopped, Message: problem.Title, Problem: problem})
	m := sized(f)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.sel = rowKey(rowProject, "website")
	wantMain(t, m, "hosts are not resolvable", "Clear it with: attrib -r")
	if n := strings.Count(mainText(m), "Clear it with"); n != 1 {
		t.Errorf("the advice shows %d times:\n%s", n, screen(m))
	}
}

// With the proxy down and a server that cannot connect, Home names r once.
func TestHomeNamesROnce(t *testing.T) {
	m, _ := problemModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	run(m, func() tea.Msg { return startedMsg{errBoom} })
	wantScreen(t, m, "✕ proxy  down", "r retry now · y copy command")
	if n := strings.Count(screen(m), "r tries again") + strings.Count(screen(m), "r retry now"); n != 1 {
		t.Errorf("the r hint shows %d times:\n%s", n, screen(m))
	}
}

// mainText is the screen right of the sidebar as text: each line's part
// after the sidebar's edge, joined with single spaces, wherever the lines
// wrap it.
func mainText(m *Model) string {
	var parts []string
	for _, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, "│"); i >= 0 {
			l = l[i+len("│"):]
		}
		parts = append(parts, l)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// wantMain fails unless the main panel holds every one of subs as text.
func wantMain(t *testing.T, m *Model, subs ...string) {
	t.Helper()
	text := mainText(m)
	for _, sub := range subs {
		if !strings.Contains(text, sub) {
			t.Errorf("the main panel lacks %q:\n%s", sub, screen(m))
		}
	}
}
