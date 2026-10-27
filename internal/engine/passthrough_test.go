package engine

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

// passthroughHarness is an engine with a connected server "apollo" and a
// fixed clock.
func passthroughHarness(t *testing.T, projects ...config.Project) (*harness, time.Time) {
	t.Helper()
	cfg := &config.Config{
		Servers:  []config.Server{{Name: "apollo", SSHHost: "apollo"}},
		Projects: projects,
	}
	h := newHarness(t, cfg)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	h.eng.now = func() time.Time { return at }
	sess, err := h.eng.ensureSession(cfg.Servers[0])
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return sess.Status() == transport.StatusConnected }, "apollo connected")
	return h, at
}

func passthroughSpec(remote int) PassthroughSpec {
	return PassthroughSpec{ID: "p1", Server: "apollo", Requester: "claude-code", Session: "1a2b3c4d5e6f", Label: "web", Remote: remote}
}

func TestOpenPassthroughSamePort(t *testing.T) {
	h, at := passthroughHarness(t)
	port := testutil.FreePort(t)
	got, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
	if err != nil {
		t.Fatal(err)
	}
	if got.Local != port {
		t.Fatalf("Local = %d, want %d", got.Local, port)
	}
	if want := fmt.Sprintf("-L 127.0.0.1:%d:localhost:%d", port, port); !strings.Contains(h.log(), want) {
		t.Fatalf("no %q in ssh log:\n%s", want, h.log())
	}
	list := h.eng.Passthroughs("apollo")
	if len(list) != 1 || list[0] != got || !list[0].Opened.Equal(at) || list[0].Requester != "claude-code" || list[0].Remote != port {
		t.Fatalf("Passthroughs = %+v, opened %+v", list, got)
	}
	if len(h.eng.Passthroughs("")) != 1 || len(h.eng.Passthroughs("other")) != 0 {
		t.Fatal("filtering by server is wrong")
	}
}

func TestOpenPassthroughFallsBack(t *testing.T) {
	h, _ := passthroughHarness(t)
	port := testutil.FreePort(t)
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
	if err != nil {
		t.Fatal(err)
	}
	if got.Local == port || got.Local == 0 || got.Remote != port {
		t.Fatalf("got %+v", got)
	}
	if want := fmt.Sprintf("-L 127.0.0.1:%d:localhost:%d", got.Local, port); !strings.Contains(h.log(), want) {
		t.Fatalf("no %q in ssh log:\n%s", want, h.log())
	}
	if h.eng.PortFree(port) {
		t.Fatal("PortFree is true for a port something listens on")
	}
}

func TestOpenPassthroughNotConnected(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{{Name: "apollo", SSHHost: "apollo"}}}
	h := newHarness(t, cfg)
	port := testutil.FreePort(t)
	_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
	if !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "Nearhost isn't connected to apollo") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(h.log(), "-O forward") {
		t.Fatalf("something was forwarded:\n%s", h.log())
	}
	if !h.eng.PortFree(port) || len(h.eng.Passthroughs("")) != 0 {
		t.Fatal("a port is claimed")
	}
}

func TestOpenPassthroughLocalRefused(t *testing.T) {
	h, _ := passthroughHarness(t)
	spec := passthroughSpec(testutil.FreePort(t))
	spec.Server = config.LocalServer
	if _, err := h.eng.OpenPassthrough(bg, spec); err == nil {
		t.Fatal("local server accepted")
	}
	if strings.Contains(h.log(), "-O forward") {
		t.Fatalf("something was forwarded:\n%s", h.log())
	}
}

func TestClosePassthrough(t *testing.T) {
	h, _ := passthroughHarness(t)
	events, cancel := h.eng.bus.Subscribe(16)
	defer cancel()
	port := testutil.FreePort(t)
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err != nil {
		t.Fatal(err)
	}
	if h.eng.PortFree(port) {
		t.Fatal("port free while open")
	}
	if err := h.eng.ClosePassthrough(bg, "p1", "the agent finished"); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("-O cancel -L 127.0.0.1:%d:localhost:%d", port, port); !strings.Contains(h.log(), want) {
		t.Fatalf("no %q in ssh log:\n%s", want, h.log())
	}
	if !h.eng.PortFree(port) || len(h.eng.Passthroughs("")) != 0 {
		t.Fatal("port still held")
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("::1", strconv.Itoa(port)), time.Second); err == nil {
		c.Close()
		t.Fatal("[::1] relay still open")
	}
	want := fmt.Sprintf("closed port %d for claude-code: the agent finished", port)
	found := false
	for len(events) > 0 {
		if ev := <-events; ev.Server == "apollo" && ev.Message == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("no event %q", want)
	}
	if err := h.eng.ClosePassthrough(bg, "nope", "x"); err != nil {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestProjectLoopbackNamesPassthrough(t *testing.T) {
	port := testutil.FreePort(t)
	h, _ := passthroughHarness(t, remoteProject("one", "apollo", port))
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err != nil {
		t.Fatal(err)
	}
	err := h.eng.StartProject(bg, "one")
	if err == nil || !strings.Contains(err.Error(), "already forwarded for a passthrough for claude-code on apollo (session 1a2b3c4d)") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenPublishesEvent(t *testing.T) {
	h, _ := passthroughHarness(t)
	events, cancel := h.eng.bus.Subscribe(16)
	defer cancel()
	port := testutil.FreePort(t)
	got, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("opened port %d for claude-code on laptop port %d", port, got.Local)
	for len(events) > 0 {
		if ev := <-events; ev.Server == "apollo" && ev.Message == want {
			return
		}
	}
	t.Fatalf("no event %q", want)
}
