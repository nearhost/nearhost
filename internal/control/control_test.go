package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/testutil"
)

// fakeBackend is a Backend that records the calls it gets.
type fakeBackend struct {
	info   Info
	status Status

	mu      sync.Mutex
	stops   int
	changed int
}

func (f *fakeBackend) Info() Info     { return f.info }
func (f *fakeBackend) Status() Status { return f.status }
func (f *fakeBackend) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}
func (f *fakeBackend) HostsChanged(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed++
}
func (f *fakeBackend) calls() (stops, changed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops, f.changed
}

func newFake() *fakeBackend {
	return &fakeBackend{
		info: Info{
			API:       APIVersion,
			Version:   "1.2.3",
			PID:       4242,
			Started:   time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC),
			StartedBy: StartedDetached,
			Reloads:   true,
		},
		status: Status{
			Servers: []ServerInfo{{Name: "leto", State: "connected", Message: ""}},
			Projects: []ProjectInfo{
				{Name: "shop", State: "serving", URL: "https://shop.test"},
				{Name: "blog", State: "failed", Message: "hosts file is read-only"},
			},
		},
	}
}

// serve starts a server for b on a fresh run directory and returns the
// directory. The server is closed when the test ends.
func serve(t *testing.T, b Backend) string {
	t.Helper()
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return dir
}

func dial(t *testing.T, dir string) *Client {
	t.Helper()
	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInfoRoundTrip(t *testing.T) {
	b := newFake()
	c := dial(t, serve(t, b))
	got, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, b.info) {
		t.Fatalf("Info = %+v, want %+v", got, b.info)
	}
}

func TestStatusRoundTrip(t *testing.T) {
	b := newFake()
	b.status.Info = b.info
	c := dial(t, serve(t, b))
	got, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, b.status) {
		t.Fatalf("Status = %+v, want %+v", got, b.status)
	}
}

func TestStopCallsBackend(t *testing.T) {
	b := newFake()
	c := dial(t, serve(t, b))
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stops, _ := b.calls(); stops != 1 {
		t.Fatalf("Stop called %d times, want 1", stops)
	}
}

func TestHostsChangedCallsBackend(t *testing.T) {
	b := newFake()
	c := dial(t, serve(t, b))
	if err := c.HostsChanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, changed := b.calls(); changed != 1 {
		t.Fatalf("HostsChanged called %d times, want 1", changed)
	}
}

func TestDialNotRunning(t *testing.T) {
	_, err := Dial(testutil.TempDir(t, "ctl"))
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial error = %v, want ErrNotRunning", err)
	}
}

// staleSocket leaves a socket file at path whose listener has gone, as a
// core that was killed does.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Skipf("no stale socket file to test with: %v", err)
	}
}

func TestDialStaleSocket(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	staleSocket(t, SocketPath(dir))
	start := time.Now()
	_, err := Dial(dir)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial error = %v, want ErrNotRunning", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Dial took %v, want under 1s", d)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	staleSocket(t, SocketPath(dir))
	b := newFake()
	s, err := Listen(SocketPath(dir), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := dial(t, dir).Info(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestListenRefusesLiveSocket(t *testing.T) {
	dir := serve(t, newFake())
	s, err := Listen(SocketPath(dir), newFake())
	if !errors.Is(err, ErrInUse) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("second Listen error = %v, want ErrInUse", err)
	}
	// The first server still answers: the refused Listen left its socket be.
	if _, err := dial(t, dir).Info(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRemovesSocket(t *testing.T) {
	dir := testutil.TempDir(t, "ctl")
	s, err := Listen(SocketPath(dir), newFake())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(dir); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial after Close = %v, want ErrNotRunning", err)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	dir := serve(t, newFake())
	c := dial(t, dir)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/v1/stop", http.StatusMethodNotAllowed},
		{"POST", "/v1", http.StatusMethodNotAllowed},
		{"GET", "/v2", http.StatusNotFound},
	} {
		code, body, err := c.raw(context.Background(), tc.method, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, code, tc.want)
		}
		if tc.want == http.StatusNotFound && !jsonHasError(body) {
			t.Errorf("%s %s body = %q, want a JSON error", tc.method, tc.path, body)
		}
	}
}

func TestDialMissingRunDir(t *testing.T) {
	_, err := Dial(filepath.Join("no-such-dir", "run")) // short and relative, under the unix socket path limit
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial of a missing run folder = %v, want ErrNotRunning", err)
	}
}

func TestSocketPath(t *testing.T) {
	if got, want := SocketPath(filepath.Join("a", "b")), filepath.Join("a", "b", "control.sock"); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}

func jsonHasError(body []byte) bool {
	var v struct{ Error string }
	return json.Unmarshal(body, &v) == nil && v.Error != ""
}
