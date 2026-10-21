package dockernet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// networks is what a daemon answers GET /networks with: one network with an
// IPv4 subnet, one with an IPv4 and an IPv6 subnet, and host, which has no
// IPAM config.
const networks = `[
 {"Name":"bridge","IPAM":{"Driver":"default","Config":[{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]}},
 {"Name":"app_default","IPAM":{"Driver":"default","Config":[{"Subnet":"fd00:d0c::/64"},{"Subnet":"192.168.97.0/24"}]}},
 {"Name":"host","IPAM":{"Driver":"default","Config":[]}}
]`

// socketPath is a path for a unix socket in a short temporary directory, as
// macOS limits socket paths to 104 bytes.
func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(testutil.TempDir(t, "dn-"), "docker.sock")
}

// fakeDaemon serves body at /networks on a unix socket and returns its path.
func fakeDaemon(t *testing.T, body string) string {
	t.Helper()
	path := socketPath(t)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/networks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

func TestRangesReadsDaemonNetworks(t *testing.T) {
	got, err := rangesFrom(context.Background(), []string{fakeDaemon(t, networks)})
	if err != nil {
		t.Fatalf("rangesFrom: %v", err)
	}
	want := []string{"172.17.0.0/16", "192.168.97.0/24", "fd00:d0c::/64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %q, want %q (and never %s)", got, want, Fallback)
	}
}

func TestRangesErrsWhenDaemonIsMissingOrSlow(t *testing.T) {
	silent := func(t *testing.T) string {
		path := socketPath(t)
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			var held []net.Conn
			defer func() {
				for _, c := range held {
					c.Close()
				}
			}()
			for {
				c, err := l.Accept()
				if err != nil {
					<-done
					return
				}
				held = append(held, c) // read nothing, answer nothing
			}
		}()
		t.Cleanup(func() { l.Close(); close(done) })
		return path
	}
	for _, c := range []struct {
		name   string
		socket func(t *testing.T) string
	}{
		{"no socket", socketPath},
		{"never answers", silent},
		{"not json", func(t *testing.T) string { return fakeDaemon(t, "not json") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.socket(t)
			begun := time.Now()
			got, err := rangesFrom(context.Background(), []string{path})
			if err == nil {
				t.Errorf("rangesFrom = %q with no error", got)
			}
			if took := time.Since(begun); took > 3*time.Second {
				t.Errorf("rangesFrom took %s", took)
			}
		})
	}
}

func TestRangesTriesSocketsInOrder(t *testing.T) {
	second := fakeDaemon(t, `[{"IPAM":{"Config":[{"Subnet":"10.99.0.0/16"}]}}]`)
	got, err := rangesFrom(context.Background(), []string{socketPath(t), second})
	if err != nil {
		t.Fatalf("rangesFrom: %v", err)
	}
	if want := []string{"10.99.0.0/16"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %q, want %q", got, want)
	}
}

// TestSocketsOrder checks every place a daemon's socket is looked for, and
// the order they are tried in.
func TestSocketsOrder(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "unix:///x/host.sock")
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("orbstack"))
	write(filepath.Join(home, ".docker", "config.json"), `{"currentContext":"orbstack"}`)
	write(filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json"),
		`{"Name":"orbstack","Endpoints":{"docker":{"Host":"unix:///x/orb.sock"}}}`)
	for _, profile := range []string{"zed", "default", "alpha"} {
		write(filepath.Join(home, ".colima", profile, "docker.sock"), "")
	}
	want := []string{
		"/x/host.sock",
		"/x/orb.sock",
		filepath.Join(home, ".docker", "run", "docker.sock"),
		filepath.Join(home, ".orbstack", "run", "docker.sock"),
		filepath.Join(home, ".rd", "docker.sock"),
		filepath.Join(home, ".colima", "default", "docker.sock"),
		filepath.Join(home, ".colima", "alpha", "docker.sock"),
		filepath.Join(home, ".colima", "zed", "docker.sock"),
		filepath.Join(xdg, "docker.sock"),
		filepath.Join(xdg, "podman", "podman.sock"),
		"/var/run/docker.sock",
	}
	if got := sockets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sockets() = %q, want %q", got, want)
	}

	// DOCKER_CONTEXT wins over the config file, and "default" adds nothing.
	t.Setenv("DOCKER_CONTEXT", "default")
	for _, s := range sockets() {
		if s == "/x/orb.sock" {
			t.Fatalf("default context still added %q", s)
		}
	}
}
