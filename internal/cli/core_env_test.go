package cli

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/report"
)

// envFake is an envProbe over a made-up machine.
type envFake struct {
	goos      string
	env       map[string]string
	onPath    map[string]bool
	agentKeys int
	dialErr   error
	launchctl string
	dockerOut string // stdout
	dockerLog string // stderr
	dockerErr error
	session   bool
	sessErr   error
	sshPath   string
	sshErr    error
	hang      bool        // docker blocks until its context ends
	calls     *[][]string // each run's name and args, when set
}

func (f envFake) probe() envProbe {
	return envProbe{
		goos:   f.goos,
		getenv: func(k string) string { return f.env[k] },
		lookPath: func(n string) (string, error) {
			if f.onPath[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		agentKeys: func(string) (int, error) { return f.agentKeys, f.dialErr },
		run: func(ctx context.Context, name string, args ...string) (string, error) {
			if f.calls != nil {
				*f.calls = append(*f.calls, append([]string{name}, args...))
			}
			if f.hang && name != "launchctl" {
				<-ctx.Done()
				return "", ctx.Err()
			}
			if name == "launchctl" {
				return f.launchctl, nil
			}
			return f.dockerOut, f.dockerErr
		},
		runSplit: func(ctx context.Context, name string, args ...string) (string, string, error) {
			if f.calls != nil {
				*f.calls = append(*f.calls, append([]string{name}, args...))
			}
			if f.hang {
				<-ctx.Done()
				return "", "", ctx.Err()
			}
			return f.dockerOut, f.dockerLog, f.dockerErr
		},
		session: func() (bool, error) { return f.session, f.sessErr },
		findSSH: func() (string, error) { return f.sshPath, f.sshErr },
	}
}

func TestCoreEnvironment(t *testing.T) {
	ok := func(name, detail string) control.EnvCheck {
		return control.EnvCheck{Name: name, Level: "ok", Detail: detail}
	}
	sshOK := ok("ssh", "/usr/bin/ssh")
	dockerOK := ok("docker", "Docker 29.9.2")
	base := func(goos string) envFake {
		return envFake{
			goos: goos, env: map[string]string{}, onPath: map[string]bool{"docker": true},
			dockerOut: "29.9.2\n", sshPath: "/usr/bin/ssh", session: true,
		}
	}
	dockerMissing := control.EnvCheck{Name: "docker", Level: "info",
		Detail: "docker is not on the core's PATH; only Docker projects need it",
		Fix:    "run nearhost service install from a shell where docker works"}
	agentUnset := control.EnvCheck{Name: "ssh agent", Level: "info",
		Detail: "SSH_AUTH_SOCK is not set for the core; ssh uses IdentityAgent from ~/.ssh/config, if set"}
	linuxWarn := control.EnvCheck{Name: "browser", Level: "warn",
		Detail: "no desktop session (DISPLAY and WAYLAND_DISPLAY are unset) or no xdg-open, so the core cannot open links",
		Fix:    "run nearhost service install from a terminal in your desktop session"}

	tests := []struct {
		name string
		mod  func(*envFake)
		want []control.EnvCheck
	}{
		{"mac aqua", func(f *envFake) { *f = base("darwin"); f.launchctl = "Aqua\n" },
			[]control.EnvCheck{ok("browser", "can open links"), agentUnset, dockerOK, sshOK}},
		{"mac background", func(f *envFake) { *f = base("darwin"); f.launchctl = "Background" },
			[]control.EnvCheck{{Name: "browser", Level: "warn",
				Detail: "the core runs outside your desktop session, so it cannot open links",
				Fix:    "run nearhost service install from Terminal on the Mac's desktop, then nearhost stop and nearhost -d"},
				agentUnset, dockerOK, sshOK}},
		{"linux display", func(f *envFake) {
			*f = base("linux")
			f.env["DISPLAY"] = ":0"
			f.onPath["xdg-open"] = true
		}, []control.EnvCheck{ok("browser", "can open links"), agentUnset, dockerOK, sshOK}},
		{"linux wayland", func(f *envFake) {
			*f = base("linux")
			f.env["WAYLAND_DISPLAY"] = "wayland-0"
			f.onPath["xdg-open"] = true
		}, []control.EnvCheck{ok("browser", "can open links"), agentUnset, dockerOK, sshOK}},
		{"linux no display", func(f *envFake) { *f = base("linux"); f.onPath["xdg-open"] = true },
			[]control.EnvCheck{linuxWarn, agentUnset, dockerOK, sshOK}},
		{"linux no xdg-open", func(f *envFake) { *f = base("linux"); f.env["DISPLAY"] = ":0" },
			[]control.EnvCheck{linuxWarn, agentUnset, dockerOK, sshOK}},
		{"windows session", func(f *envFake) { *f = base("windows") },
			[]control.EnvCheck{ok("browser", "can open links"), dockerOK, sshOK}},
		{"windows service session", func(f *envFake) { *f = base("windows"); f.session = false },
			[]control.EnvCheck{{Name: "browser", Level: "warn",
				Detail: "the core runs outside your sign-in session, so it cannot open links"}, dockerOK, sshOK}},
		{"windows session error", func(f *envFake) { *f = base("windows"); f.sessErr = errors.New("access denied") },
			[]control.EnvCheck{{Name: "browser", Level: "info", Detail: "access denied"}, dockerOK, sshOK}},
		{"agent dials", func(f *envFake) {
			*f = base("linux")
			f.env["SSH_AUTH_SOCK"] = "/run/agent.sock"
			f.env["DISPLAY"] = ":0"
			f.onPath["xdg-open"] = true
			f.agentKeys = 3
		}, []control.EnvCheck{ok("browser", "can open links"), ok("ssh agent", "/run/agent.sock (3 keys)"), dockerOK, sshOK}},
		{"agent holds one key", func(f *envFake) {
			*f = base("linux")
			f.env["SSH_AUTH_SOCK"] = "/run/agent.sock"
			f.env["DISPLAY"] = ":0"
			f.onPath["xdg-open"] = true
			f.agentKeys = 1
		}, []control.EnvCheck{ok("browser", "can open links"), ok("ssh agent", "/run/agent.sock (1 key)"), dockerOK, sshOK}},
		{"not an agent", func(f *envFake) {
			*f = base("linux")
			f.env["SSH_AUTH_SOCK"] = "/run/agent.sock"
			f.env["DISPLAY"] = ":0"
			f.onPath["xdg-open"] = true
			f.dialErr = errors.New("agent: failure")
		}, []control.EnvCheck{ok("browser", "can open links"), {Name: "ssh agent", Level: "warn",
			Detail: "/run/agent.sock does not answer as an SSH agent: agent: failure",
			Fix:    "run nearhost service install again from a shell whose agent works, or set IdentityAgent in ~/.ssh/config"},
			dockerOK, sshOK}},
		{"docker warning on stderr", func(f *envFake) {
			*f = base("windows")
			f.dockerLog = "WARNING: a plugin is out of date\n"
		}, []control.EnvCheck{ok("browser", "can open links"), dockerOK, sshOK}},
		{"docker missing", func(f *envFake) { *f = base("windows"); delete(f.onPath, "docker") },
			[]control.EnvCheck{ok("browser", "can open links"), dockerMissing, sshOK}},
		{"docker failing", func(f *envFake) {
			*f = base("windows")
			f.dockerOut = "WARNING: a plugin is out of date\n"
			f.dockerLog = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock\nIs the daemon running?\n"
			f.dockerErr = errors.New("exit status 1")
		}, []control.EnvCheck{ok("browser", "can open links"), {Name: "docker", Level: "warn",
			Detail: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
			Fix:    "start Docker, or set DOCKER_HOST before nearhost service install"}, sshOK}},
		{"ssh missing", func(f *envFake) { *f = base("windows"); f.sshPath, f.sshErr = "", errors.New("not found") },
			[]control.EnvCheck{ok("browser", "can open links"), dockerOK, {Name: "ssh", Level: "fail",
				Detail: "ssh not found on the core's PATH",
				Fix:    "install OpenSSH, then nearhost service install again"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var f envFake
			tc.mod(&f)
			got := coreEnvironment(context.Background(), f.probe())
			for i := range got {
				// Each check that is not ok says so as a message too.
				if (got[i].Level == "ok") != (got[i].Problem == nil) {
					t.Errorf("%s: level %s with problem %+v", got[i].Name, got[i].Level, got[i].Problem)
				}
				got[i].Problem = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("checks =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestCoreEnvironmentCleansDetails(t *testing.T) {
	f := envFake{goos: "windows", env: map[string]string{}, onPath: map[string]bool{"docker": true},
		dockerOut: "\x1b]0;owned\x07bad \x1b[31mred\x1b[0m\n", dockerErr: errors.New("exit status 1"),
		session: true, sshPath: "/usr/bin/ssh"}
	got := coreEnvironment(context.Background(), f.probe())
	for _, c := range got {
		if c.Name == "docker" {
			for _, r := range c.Detail {
				if r < ' ' {
					t.Errorf("detail %q holds a control character", c.Detail)
				}
			}
		}
	}
}

func TestCoreEnvironmentAsksDocker(t *testing.T) {
	var calls [][]string
	f := envFake{goos: "windows", env: map[string]string{}, onPath: map[string]bool{"docker": true},
		dockerOut: "29.9.2", session: true, sshPath: "/usr/bin/ssh", calls: &calls}
	coreEnvironment(context.Background(), f.probe())
	want := [][]string{{"/usr/bin/docker", "version", "--format", "{{.Server.Version}}"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("commands run = %v, want %v", calls, want)
	}
}

func TestCoreEnvironmentDockerHangs(t *testing.T) {
	old := dockerProbeTimeout
	dockerProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dockerProbeTimeout = old })
	f := envFake{goos: "windows", env: map[string]string{}, onPath: map[string]bool{"docker": true},
		hang: true, session: true, sshPath: "/usr/bin/ssh"}
	var docker control.EnvCheck
	for _, c := range coreEnvironment(context.Background(), f.probe()) {
		if c.Name == "docker" {
			docker = c
		}
	}
	if docker.Level != "warn" || docker.Detail != "docker did not answer within 5 seconds" {
		t.Errorf("docker = %+v, want a warning that it did not answer", docker)
	}
}

func TestSystemAgentKeysRejectsNonAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent check does not run on Windows")
	}
	sock := filepath.Join(t.TempDir(), "a.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err == nil {
			conn.Write([]byte("hello, not an agent\n"))
			conn.Close()
		}
	}()
	if n, err := systemEnvProbe().agentKeys(sock); err == nil {
		t.Errorf("agentKeys = %d, nil; want an error from a listener that is no agent", n)
	}
}

// The core's checks that are not ok carry their codes and fix steps, each
// command on its own.
func TestCoreEnvironmentProblems(t *testing.T) {
	f := envFake{goos: "linux", env: map[string]string{"SSH_AUTH_SOCK": "/run/agent.sock"}, onPath: map[string]bool{},
		dialErr: errors.New("agent: failure"), sshErr: errors.New("not found")}
	want := map[string]report.Code{
		"browser":   report.CoreNoDesktop,
		"ssh agent": report.CoreSSHAgentUnreachable,
		"docker":    report.CoreDockerMissing,
		"ssh":       report.CoreSSHMissing,
	}
	for _, c := range coreEnvironment(context.Background(), f.probe()) {
		if c.Problem == nil || c.Problem.Code != want[c.Name] {
			t.Errorf("%s: problem %+v, want %s", c.Name, c.Problem, want[c.Name])
			continue
		}
		if len(c.Problem.Fix) == 0 {
			t.Errorf("%s: no fix steps", c.Name)
		}
		for _, s := range c.Problem.Fix {
			if strings.Contains(s.Text, "nearhost ") {
				t.Errorf("%s: step %q holds a command in its text", c.Name, s.Text)
			}
		}
	}
}
