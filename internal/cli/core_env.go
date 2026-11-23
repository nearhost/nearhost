package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
	"github.com/nearhost/nearhost/internal/transport"
)

// dockerProbeTimeout is how long the core waits for docker to answer.
var dockerProbeTimeout = 5 * time.Second

// envProbe is what coreEnvironment asks of the machine the core runs on, so
// tests can give it any machine.
type envProbe struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	// dialUnix connects to the socket at path and closes it.
	dialUnix func(path string) error
	// run runs the command and returns its output, stdout and stderr
	// together.
	run func(ctx context.Context, name string, args ...string) (string, error)
	// session says whether the core runs in a sign-in session that has a
	// desktop. Only Windows asks.
	session func() (interactive bool, err error)
	findSSH func() (string, error)
}

// systemEnvProbe is the probe of the machine the core runs on.
func systemEnvProbe() envProbe {
	return envProbe{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		dialUnix: func(path string) error {
			conn, err := net.DialTimeout("unix", path, 2*time.Second)
			if err != nil {
				return err
			}
			return conn.Close()
		},
		run: func(ctx context.Context, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			// A command run by a core that has no console must not open one.
			service.HideWindow(cmd)
			// A child that keeps the output pipe open must not keep
			// CombinedOutput waiting past the context.
			cmd.WaitDelay = time.Second
			out, err := cmd.CombinedOutput()
			return string(out), err
		},
		session: interactiveSession,
		findSSH: transport.FindSSH,
	}
}

// coreEnvironment looks at what the core can reach from where it runs: a
// browser to open links in, the SSH agent, Docker and ssh. A core started
// from a service or over SSH can lack what a terminal has.
func coreEnvironment(ctx context.Context, p envProbe) []control.EnvCheck {
	checks := []control.EnvCheck{envBrowser(ctx, p)}
	if p.goos != "windows" {
		// Windows has doctor's own ssh agent check.
		checks = append(checks, envSSHAgent(p))
	}
	checks = append(checks, envDocker(ctx, p), envSSH(p))
	for i := range checks {
		checks[i].Detail = cleanDetail(checks[i].Detail)
	}
	return checks
}

// cleanDetail is s with its control characters removed and its surrounding
// space trimmed, and keeps its first line: it may be what a command printed.
func cleanDetail(s string) string {
	s = strings.TrimSpace(printableLines(s))
	first, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(first)
}

func envBrowser(ctx context.Context, p envProbe) control.EnvCheck {
	c := control.EnvCheck{Name: "browser", Level: "ok", Detail: "can open links"}
	switch p.goos {
	case "darwin":
		out, err := p.run(ctx, "launchctl", "managername")
		if err != nil || strings.TrimSpace(out) != "Aqua" {
			c.Level = "warn"
			c.Detail = "the core runs outside your desktop session, so it cannot open links"
			c.Fix = "run nearhost service install from Terminal on the Mac's desktop, then nearhost stop and nearhost -d"
		}
	case "windows":
		interactive, err := p.session()
		switch {
		case err != nil:
			c.Level, c.Detail = "info", err.Error()
		case !interactive:
			c.Level = "warn"
			c.Detail = "the core runs outside your sign-in session, so it cannot open links"
		}
	default:
		_, err := p.lookPath("xdg-open")
		if (p.getenv("DISPLAY") == "" && p.getenv("WAYLAND_DISPLAY") == "") || err != nil {
			c.Level = "warn"
			c.Detail = "no desktop session (DISPLAY and WAYLAND_DISPLAY are unset) or no xdg-open, so the core cannot open links"
			c.Fix = "run nearhost service install from a terminal in your desktop session"
		}
	}
	return c
}

func envSSHAgent(p envProbe) control.EnvCheck {
	sock := p.getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return control.EnvCheck{Name: "ssh agent", Level: "info",
			Detail: "SSH_AUTH_SOCK is not set for the core; ssh uses IdentityAgent from ~/.ssh/config, if set"}
	}
	if err := p.dialUnix(sock); err != nil {
		return control.EnvCheck{Name: "ssh agent", Level: "warn",
			Detail: fmt.Sprintf("cannot reach %s: %v", sock, err),
			Fix:    "run nearhost service install again from a shell whose agent works, or set IdentityAgent in ~/.ssh/config"}
	}
	return control.EnvCheck{Name: "ssh agent", Level: "ok", Detail: sock}
}

func envDocker(ctx context.Context, p envProbe) control.EnvCheck {
	docker, err := p.lookPath("docker")
	if err != nil {
		return control.EnvCheck{Name: "docker", Level: "info",
			Detail: "docker is not on the core's PATH; only Docker projects need it",
			Fix:    "run nearhost service install from a shell where docker works"}
	}
	ctx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	out, err := p.run(ctx, docker, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		detail := out
		if cleanDetail(detail) == "" {
			detail = err.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				detail = "docker did not answer within 5 seconds"
			}
		}
		return control.EnvCheck{Name: "docker", Level: "warn", Detail: detail,
			Fix: "start Docker, or set DOCKER_HOST before nearhost service install"}
	}
	return control.EnvCheck{Name: "docker", Level: "ok", Detail: "Docker " + strings.TrimSpace(out)}
}

func envSSH(p envProbe) control.EnvCheck {
	path, err := p.findSSH()
	if err != nil {
		return control.EnvCheck{Name: "ssh", Level: "fail",
			Detail: "ssh not found on the core's PATH",
			Fix:    "install OpenSSH, then nearhost service install again"}
	}
	return control.EnvCheck{Name: "ssh", Level: "ok", Detail: path}
}
