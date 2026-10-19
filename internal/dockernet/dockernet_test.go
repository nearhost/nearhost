package dockernet

import (
	"context"
	"net"
	"net/http"
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
