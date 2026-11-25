package service

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nearhost/nearhost/internal/report"
)

// resolve follows symbolic links; tests replace it.
var resolve = filepath.EvalSymlinks

// ServiceEnv returns the environment to write into the service, and notes
// for the user: a notice or a warning about the SSH agent the service uses.
// managerEnv is what the manager already gives its services (systemd's
// show-environment; launchd's SSH_AUTH_SOCK); home and runtimeDir are $HOME
// and $XDG_RUNTIME_DIR. A Windows logon task has the user's environment, so
// it needs none. For a service of this system, SSH_AUTH_SOCK is judged by
// where its links lead.
func ServiceEnv(goos string, getenv func(string) string, managerEnv map[string]string, home, runtimeDir string) ([]EnvVar, []*report.Message) {
	if goos == "windows" {
		return nil, nil
	}
	var env []EnvVar
	if p := getenv("PATH"); p != "" { // PATH= would hide the manager's own
		env = append(env, EnvVar{"PATH", p})
	}
	names := []string{"DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}
	for _, n := range names {
		if v := getenv(n); v != "" {
			env = append(env, EnvVar{n, v})
		}
	}
	if goos == "linux" {
		// The session's display and bus can differ from the manager's;
		// where they match, writing them would only pin a stale copy.
		for _, n := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
			if v := getenv(n); v != "" && v != managerEnv[n] {
				env = append(env, EnvVar{n, v})
			}
		}
	}
	var notes []*report.Message
	sock := getenv("SSH_AUTH_SOCK")
	// A socket is judged by where it leads: ~/.ssh/ssh_auth_sock, a link a
	// login points at its forwarded agent, goes when that login ends. Only
	// this system's disk can say, so another's paths are taken as they are.
	real, realHome, realRun := sock, home, runtimeDir
	if goos == runtime.GOOS && sock != "" {
		real, realHome, realRun = resolved(sock), resolved(home), resolved(runtimeDir)
	}
	switch {
	case sock == "":
		notes = append(notes, report.Notef(report.ServiceAgentUnset, "SSH_AUTH_SOCK is not set here, so the service uses the agent your login session provides.").
			Then("If servers do not connect, set IdentityAgent in ~/.ssh/config."))
	case sock == managerEnv["SSH_AUTH_SOCK"],
		goos == "darwin" && strings.HasPrefix(real, "/private/tmp/com.apple.launchd."):
		// The manager already provides it.
	case under(real, realHome) || under(real, realRun):
		env = append(env, EnvVar{"SSH_AUTH_SOCK", sock})
	default:
		where := sock
		if real != sock {
			where += ", a link to " + real
		}
		notes = append(notes, report.Warnf(report.ServiceAgentChanges, "SSH_AUTH_SOCK here (%s) changes at each login, so the service cannot use it.", where).
			Then("Set IdentityAgent in ~/.ssh/config to a lasting agent socket."))
	}
	return env, notes
}

// resolved is p with its links followed, or p when that fails, as for a
// path that does not exist. A link to a path that does not exist, as one to
// a forwarded agent's socket once that login has ended, is where it leads.
func resolved(p string) string {
	return follow(p, maxLinks)
}

// maxLinks is how many links follow goes through before it gives up, as on
// a loop: Linux's own limit.
const maxLinks = 40

// follow is resolved with up to n more links to go through. A path that
// does not exist is followed link by link to the one that is missing, and
// that is returned with its folder resolved.
func follow(p string, n int) string {
	if p == "" {
		return p
	}
	if r, err := resolve(p); err == nil {
		return r
	}
	if t, err := os.Readlink(p); err == nil && n > 0 {
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		return follow(t, n-1)
	}
	if dir := filepath.Dir(p); dir != p {
		return filepath.Join(follow(dir, n), filepath.Base(p))
	}
	return p
}

// under reports whether the POSIX path p is inside dir. ServiceEnv writes
// nothing on Windows, so every path it judges is POSIX, whatever the host.
func under(p, dir string) bool {
	if dir == "" {
		return false
	}
	return strings.HasPrefix(path.Clean(p), strings.TrimSuffix(path.Clean(dir), "/")+"/")
}
