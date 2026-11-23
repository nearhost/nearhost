package service

import (
	"path/filepath"
	"strings"
)

// ServiceEnv returns the environment to write into the service, and notes.
// managerEnv is what the manager already gives its services (systemd's
// show-environment; launchd's SSH_AUTH_SOCK); home and runtimeDir are $HOME
// and $XDG_RUNTIME_DIR. A Windows logon task has the user's environment, so
// it needs none.
func ServiceEnv(goos string, getenv func(string) string, managerEnv map[string]string, home, runtimeDir string) ([]EnvVar, []string) {
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
	var notes []string
	sock := getenv("SSH_AUTH_SOCK")
	switch {
	case sock == "":
		notes = append(notes, "SSH_AUTH_SOCK is not set here, so the service uses the agent your login session provides. "+
			"If servers do not connect, set IdentityAgent in ~/.ssh/config.")
	case sock == managerEnv["SSH_AUTH_SOCK"],
		goos == "darwin" && strings.HasPrefix(sock, "/private/tmp/com.apple.launchd."):
		// The manager already provides it.
	case under(sock, home) || under(sock, runtimeDir):
		env = append(env, EnvVar{"SSH_AUTH_SOCK", sock})
	default:
		notes = append(notes, "SSH_AUTH_SOCK here ("+sock+") changes at each login, so the service cannot use it. "+
			"Set IdentityAgent in ~/.ssh/config to a lasting agent socket.")
	}
	return env, notes
}

// under reports whether p is inside dir.
func under(p, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}
