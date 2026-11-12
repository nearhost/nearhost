//go:build !windows

package dockernet

import (
	"os"
	"path/filepath"
	"strings"
)

// endpoints lists where a daemon's socket may be, most specific first:
// DOCKER_HOST when it names a unix socket, the current docker context's
// socket, then Docker Desktop's, OrbStack's, Rancher Desktop's, every Colima
// profile's (default first), a rootless Docker's, Podman's and the system
// one. A DOCKER_HOST or context that is not a unix socket (tcp, ssh) is
// skipped: that daemon's networks are not on this machine, so its containers
// do not come from them. Each place is listed once.
func endpoints() []string {
	var out []string
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && host != "" {
		out = append(out, host)
	}
	home, homeErr := os.UserHomeDir()
	if sock, ok := strings.CutPrefix(contextHost(home), "unix://"); ok && sock != "" {
		out = append(out, sock)
	}
	if homeErr == nil {
		out = append(out,
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".orbstack", "run", "docker.sock"),
			filepath.Join(home, ".rd", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"))
		others, _ := filepath.Glob(filepath.Join(home, ".colima", "*", "docker.sock"))
		out = append(out, others...) // Glob sorts; duplicates are dropped below
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		out = append(out,
			filepath.Join(dir, "docker.sock"),
			filepath.Join(dir, "podman", "podman.sock"))
	}
	out = append(out, "/var/run/docker.sock")
	return unique(out)
}
