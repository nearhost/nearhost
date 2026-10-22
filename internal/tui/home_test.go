package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/doctor"
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
		"· 2 problems", " r retry · l log · / filter")
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

// A server that gave up says how often it tried; one that is reconnecting
// counts down to its next attempt.
func TestServerAttemptsAndNextTry(t *testing.T) {
	m, f := problemModel(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	f.health["vps"] = engine.ServerHealth{Health: transport.Health{Failures: 3}}
	wantScreen(t, m, "○ vps  auth failed · tried 3 times", "then press r.")
	f.servers["vps"] = transport.StatusReconnecting
	f.health["vps"] = engine.ServerHealth{Health: transport.Health{Failures: 3, NextAttempt: now.Add(15500 * time.Millisecond)}}
	wantScreen(t, m, "◐ vps  reconnecting · attempt 4 · next in 16s", "Nearhost tries again by itself.")
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

// The fix for a proxy that is down matches why it is down.
func TestProxyFix(t *testing.T) {
	denied := fmt.Errorf("start proxy: listen tcp :443: bind: %w", syscall.EACCES)
	text := errors.New("start proxy: listen tcp :443: bind: permission denied")
	busy := fmt.Errorf("start proxy: listen tcp :443: bind: %w", syscall.EADDRINUSE)
	const ports = "pass --https-port and --http-port."
	cases := []struct {
		name string
		err  error
		goos string
		want string
	}{
		{"denied on linux", denied, "linux", doctor.SetcapFix("/usr/bin/nearhost")},
		{"denied as text on linux", text, "linux", doctor.SetcapFix("/usr/bin/nearhost")},
		{"denied on darwin", denied, "darwin", "Run nearhost as a user that may bind the port, or " + ports},
		{"in use", busy, "linux", "Quit the program using the port, or " + ports},
		{"run directory not a directory", fmt.Errorf("create run directory: %w", &fs.PathError{Op: "mkdir", Path: "/run/nearhost", Err: syscall.ENOTDIR}), "linux", "Fix /run/nearhost: not a directory."},
		{"run directory", fmt.Errorf("create run directory: %w", &fs.PathError{Op: "mkdir", Path: "/run/nearhost", Err: syscall.EACCES}), "linux", "Check that this user can create /run/nearhost."},
	}
	for _, c := range cases {
		got := proxyFix(c.err, c.goos, "/usr/bin/nearhost")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: proxyFix = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}
