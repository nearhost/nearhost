package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	if strings.Contains(h.log(), "-L ") {
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
	if strings.Contains(h.log(), "-L ") {
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
	if want := spec(port, port); !slices.Contains(h.cancelled("apollo"), want) {
		t.Fatalf("no cancel of %s; cancels %q", want, h.cancelled("apollo"))
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

// holdOpen makes the next open of id wait in the engine after it has claimed
// its port. It returns a channel that closes when the open has got there and
// a function that lets it go on.
func holdOpen(h *harness) (reached <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	h.eng.beforeForward = func(string) {
		close(in)
		<-out
	}
	return in, func() { close(out) }
}

func TestOpenPassthroughDuplicateIDLeavesFirstAlone(t *testing.T) {
	h, _ := passthroughHarness(t)
	port := testutil.FreePort(t)
	first, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if h.eng.PortFree(first.Local) {
		t.Fatal("the first passthrough lost its port")
	}
	h.eng.mu.Lock()
	_, relay := h.eng.relays[first.Local]
	h.eng.mu.Unlock()
	if !relay {
		t.Fatal("the first passthrough lost its relay")
	}
	if got := h.eng.Passthroughs(""); len(got) != 1 || got[0] != first {
		t.Fatalf("Passthroughs = %+v", got)
	}
}

func TestClosePassthroughDuringOpen(t *testing.T) {
	h, _ := passthroughHarness(t)
	reached, release := holdOpen(h)
	port := testutil.FreePort(t)
	done := make(chan error, 1)
	go func() {
		_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
		done <- err
	}()
	<-reached
	if len(h.eng.Passthroughs("")) != 0 {
		t.Fatal("an opening passthrough is listed")
	}
	if err := h.eng.ClosePassthrough(bg, "p1", "done"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "closed while it opened") {
		t.Fatalf("open err = %v", err)
	}
	if !h.eng.PortFree(port) || len(h.eng.Passthroughs("")) != 0 {
		t.Fatal("a port or record is still held")
	}
	if got := h.forwards("apollo"); len(got) != 0 {
		t.Fatalf("forwards = %v", got)
	}
}

func TestOwnerTextDuringOpen(t *testing.T) {
	port := testutil.FreePort(t)
	h, _ := passthroughHarness(t, remoteProject("one", "apollo", port))
	reached, release := holdOpen(h)
	done := make(chan error, 1)
	go func() {
		_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
		done <- err
	}()
	<-reached
	err := h.eng.StartProject(bg, "one")
	release()
	if openErr := <-done; openErr != nil {
		t.Fatal(openErr)
	}
	if err == nil || !strings.Contains(err.Error(), "already forwarded for a passthrough for claude-code on apollo (session 1a2b3c4d)") {
		t.Fatalf("err = %v", err)
	}
}

// downWrapper returns the fake ssh set, for the rest of the test, to fail
// every master with a refused connection while the returned flag file
// exists, which the session keeps retrying. Otherwise it behaves as usual.
func downWrapper(t *testing.T) (path, flag string) {
	t.Helper()
	flag = filepath.Join(t.TempDir(), "down")
	t.Setenv("FAKE_SSH_MASTER_DOWN", flag)
	return fakeSSH(t), flag
}

// endHarness is an engine with a connected server "apollo" run by sshPath,
// or the fake ssh when it is empty, with a fixed clock that the returned
// function moves to an offset from the start.
func endHarness(t *testing.T, sshPath string, sshEnv ...string) (h *harness, at func(time.Duration)) {
	t.Helper()
	cfg := &config.Config{Servers: []config.Server{{Name: "apollo", SSHHost: "apollo"}}}
	h = buildHarness(t, cfg, func(o *Options) {
		if sshPath != "" {
			o.SSHPath = sshPath
		}
	}, sshEnv...)
	// The tests run the checks themselves on the fixed clock; one the loop
	// ran as well, on real time, would land at whatever time was set.
	h.eng.passEvery = 1000 * time.Hour
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := start
	h.eng.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	h.eng.probePass = func(context.Context, int) bool { return true }
	sess, err := h.eng.ensureSession(cfg.Servers[0])
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return sess.Status() == transport.StatusConnected }, "apollo connected")
	return h, func(d time.Duration) {
		mu.Lock()
		now = start.Add(d)
		mu.Unlock()
	}
}

// probeAnswers makes the port check answer, or not, as the returned
// function says.
func probeAnswers(h *harness) func(bool) {
	var answers atomic.Bool
	answers.Store(true)
	h.eng.probePass = func(context.Context, int) bool { return answers.Load() }
	return answers.Store
}

// checksEvery runs a check at each passCheckEvery from from to to, both
// included, with the clock moved to it.
func checksEvery(h *harness, at func(time.Duration), from, to time.Duration) {
	for d := from; d <= to; d += passCheckEvery {
		at(d)
		h.eng.checkPassthroughs(bg)
	}
}

// waitClosedEvent waits for the event of the passthrough on remote closing
// with reason.
func waitClosedEvent(t *testing.T, events <-chan Event, remote int, reason string) {
	t.Helper()
	want := fmt.Sprintf("closed port %d for claude-code: %s", remote, reason)
	var seen []string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Server == "apollo" && ev.Message == want {
				return
			}
			seen = append(seen, ev.Message)
		case <-deadline:
			t.Fatalf("no event %q; got %q", want, seen)
		}
	}
}

// assertOpen fails the test unless the passthrough p1 is open.
func assertOpen(t *testing.T, h *harness) {
	t.Helper()
	if got := h.eng.Passthroughs(""); len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("Passthroughs = %+v, want p1 open", got)
	}
}

// assertClosed fails the test unless no passthrough is open and the port
// it held is free.
func assertClosed(t *testing.T, h *harness, local int) {
	t.Helper()
	testutil.WaitUntil(t, 5*time.Second, func() bool { return len(h.eng.Passthroughs("")) == 0 && h.eng.PortFree(local) }, "the passthrough closed")
}

func openP1(t *testing.T, h *harness) Passthrough {
	t.Helper()
	p, err := h.eng.OpenPassthrough(bg, passthroughSpec(testutil.FreePort(t)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// waitStatus waits for apollo's session to reach a status for which ok holds.
func waitStatus(t *testing.T, h *harness, what string, ok func(transport.Status) bool) {
	t.Helper()
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("apollo")
		return ok(st)
	}, what)
}

func connected(st transport.Status) bool { return st == transport.StatusConnected }

func away(st transport.Status) bool {
	return st == transport.StatusReconnecting || st == transport.StatusConnecting
}

func TestDisconnectServerClosesPassthroughs(t *testing.T) {
	h, _ := endHarness(t, "")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	p := openP1(t, h)
	if err := h.eng.DisconnectServer("apollo"); err != nil {
		t.Fatal(err)
	}
	waitClosedEvent(t, events, p.Remote, "apollo was disconnected")
	assertClosed(t, h, p.Local)
}

func TestDropServerClosesPassthroughs(t *testing.T) {
	h, _ := endHarness(t, "")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	p := openP1(t, h)
	if err := h.eng.Apply(bg, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	waitClosedEvent(t, events, p.Remote, "apollo was removed from Nearhost")
	assertClosed(t, h, p.Local)
}

func TestReplacedSessionClosesPassthroughs(t *testing.T) {
	h, _ := endHarness(t, "")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	p := openP1(t, h)
	cfg := &config.Config{Servers: []config.Server{{Name: "apollo", SSHHost: "apollo.example"}}}
	if err := h.eng.Apply(bg, cfg); err != nil {
		t.Fatal(err)
	}
	waitClosedEvent(t, events, p.Remote, "apollo was disconnected")
	assertClosed(t, h, p.Local)
}

func TestShortDisconnectKeeps(t *testing.T) {
	ssh, flag := downWrapper(t)
	h, at := endHarness(t, ssh)
	p := openP1(t, h)
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "apollo")
	waitStatus(t, h, "apollo away", away)
	checksEvery(h, at, passCheckEvery, 9*time.Minute)
	assertOpen(t, h)
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h, "apollo back", connected)
	at(9*time.Minute + passCheckEvery)
	h.eng.checkPassthroughs(bg)
	assertOpen(t, h)
	if f, lost := h.forwards("apollo"), h.unapplied("apollo"); len(f) != 1 || f[0][0] != p.Local || len(lost) != 0 {
		t.Fatalf("forwards %v, not applied %v", f, lost)
	}
}

func TestLongDisconnectCloses(t *testing.T) {
	ssh, flag := downWrapper(t)
	h, at := endHarness(t, ssh)
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	p := openP1(t, h)
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "apollo")
	waitStatus(t, h, "apollo away", away)
	// The first check to find apollo away is at 30s, so 10 minutes away
	// ends at 10m30s.
	checksEvery(h, at, passCheckEvery, passGoneFor)
	assertOpen(t, h)
	at(passGoneFor + passCheckEvery)
	h.eng.checkPassthroughs(bg)
	waitClosedEvent(t, events, p.Remote, "apollo was disconnected for 10 minutes")
	assertClosed(t, h, p.Local)
}

func TestReapplyFailureCloses(t *testing.T) {
	failFile := filepath.Join(t.TempDir(), "forward-fails")
	h, at := endHarness(t, "", "FAKE_SSH_FORWARD_FAIL_FILE="+failFile)
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	p := openP1(t, h)
	if err := os.WriteFile(failFile, []byte(strconv.Itoa(p.Local)), 0o600); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "apollo")
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("apollo")
		return st == transport.StatusConnected && len(h.unapplied("apollo")) == 1
	}, "a reconnect whose re-apply failed")
	at(passCheckEvery)
	h.eng.checkPassthroughs(bg)
	waitClosedEvent(t, events, p.Remote, fmt.Sprintf("laptop port %d was taken while apollo was away", p.Local))
	assertClosed(t, h, p.Local)
}

func TestPortCheckCloses(t *testing.T) {
	h, at := endHarness(t, "")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	answers := probeAnswers(h)
	p := openP1(t, h)
	answers(false)
	checksEvery(h, at, 0, passFailFor-passCheckEvery)
	assertOpen(t, h)
	at(passFailFor)
	h.eng.checkPassthroughs(bg)
	waitClosedEvent(t, events, p.Remote, fmt.Sprintf("nothing answered on port %d for 5 minutes", p.Remote))
	assertClosed(t, h, p.Local)
}

func TestPortCheckStreakResets(t *testing.T) {
	h, at := endHarness(t, "")
	answers := probeAnswers(h)
	openP1(t, h)
	answers(false)
	checksEvery(h, at, 0, 4*time.Minute+30*time.Second)
	answers(true)
	checksEvery(h, at, 5*time.Minute, 5*time.Minute)
	answers(false)
	checksEvery(h, at, 5*time.Minute+passCheckEvery, 9*time.Minute)
	assertOpen(t, h)
}

func TestPortCheckPausedWhileDisconnected(t *testing.T) {
	ssh, flag := downWrapper(t)
	h, at := endHarness(t, ssh)
	events, cancel := h.eng.bus.Subscribe(256)
	defer cancel()
	answers := probeAnswers(h)
	p := openP1(t, h)
	answers(false)
	checksEvery(h, at, 0, 4*time.Minute)
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "apollo")
	waitStatus(t, h, "apollo away", away)
	checksEvery(h, at, 4*time.Minute+passCheckEvery, 12*time.Minute)
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h, "apollo back", connected)
	// A minute on from the end of the disconnection, the port has failed
	// for under 5 minutes of connected checks.
	checksEvery(h, at, 12*time.Minute+passCheckEvery, 13*time.Minute)
	assertOpen(t, h)
	// The next one makes 5.
	at(13*time.Minute + passCheckEvery)
	h.eng.checkPassthroughs(bg)
	waitClosedEvent(t, events, p.Remote, fmt.Sprintf("nothing answered on port %d for 5 minutes", p.Remote))
}

func TestShutdownReleasesPassthroughs(t *testing.T) {
	h, _ := endHarness(t, "")
	p := openP1(t, h)
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatal(err)
	}
	if !h.eng.PortFree(p.Local) || len(h.eng.Passthroughs("")) != 0 {
		t.Fatal("a passthrough outlived Shutdown")
	}
}

// serveOnce listens on 127.0.0.1 and hands each connection to serve.
func serveOnce(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestDefaultProbe(t *testing.T) {
	silent := serveOnce(t, func(c net.Conn) {
		time.Sleep(3 * time.Second)
		c.Close()
	})
	talks := serveOnce(t, func(c net.Conn) {
		c.Write([]byte("SSH-2.0-hello\r\n"))
		time.Sleep(time.Second)
		c.Close()
	})
	refuses := serveOnce(t, func(c net.Conn) { c.Close() })
	closed := testutil.FreePort(t)
	for name, tc := range map[string]struct {
		port int
		want bool
	}{
		"silent":  {silent, true},
		"talks":   {talks, true},
		"refuses": {refuses, false},
		"closed":  {closed, false},
	} {
		if got := probePassthroughPort(bg, tc.port); got != tc.want {
			t.Errorf("%s: probe = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPassthroughCheckIntervalDefault(t *testing.T) {
	if got := New(&config.Config{}, Options{}).passEvery; got != 30*time.Second {
		t.Fatalf("passEvery %v", got)
	}
}

func TestOpenedPublishedBeforeClose(t *testing.T) {
	h, _ := endHarness(t, "")
	events, cancel := h.eng.bus.Subscribe(64)
	defer cancel()
	// The open reads the clock once its forward is made, as it marks the
	// passthrough open. Taking the bus there keeps it from publishing its
	// event until the test lets go.
	at := h.eng.now()
	var once sync.Once
	held := make(chan struct{})
	h.eng.now = func() time.Time {
		once.Do(func() {
			h.eng.bus.mu.Lock()
			close(held)
		})
		return at
	}
	port := testutil.FreePort(t)
	opened := make(chan error, 1)
	go func() {
		_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
		opened <- err
	}()
	<-held
	// A close can begin once the passthrough is listed, so it must not be
	// listed before its event is published.
	visible := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		for len(h.eng.Passthroughs("")) == 0 {
			time.Sleep(time.Millisecond)
		}
		close(visible)
		closed <- h.eng.ClosePassthrough(bg, "p1", "done")
	}()
	early := false
	select {
	case <-visible:
		early = true
	case <-time.After(300 * time.Millisecond):
	}
	h.eng.bus.mu.Unlock()
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if early {
		t.Fatal("the passthrough was listed, so could be closed, before its open was published")
	}
	var order []string
	for len(events) > 0 {
		ev := <-events
		switch {
		case strings.HasPrefix(ev.Message, fmt.Sprintf("opened port %d ", port)):
			order = append(order, "opened")
		case strings.HasPrefix(ev.Message, fmt.Sprintf("closed port %d ", port)):
			order = append(order, "closed")
		}
	}
	if strings.Join(order, ",") != "opened,closed" {
		t.Fatalf("events = %v, want opened then closed", order)
	}
}

func TestShutdownWaitsForInFlightOpen(t *testing.T) {
	h, _ := endHarness(t, "")
	reached, release := holdOpen(h)
	port := testutil.FreePort(t)
	opened := make(chan error, 1)
	go func() {
		_, err := h.eng.OpenPassthrough(bg, passthroughSpec(port))
		opened <- err
	}()
	<-reached
	shut := make(chan error, 1)
	go func() { shut <- h.eng.Shutdown(bg) }()
	select {
	case err := <-shut:
		release()
		t.Fatalf("Shutdown returned (%v) while an open was in flight", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-opened; err != nil {
		t.Fatalf("the in-flight open: %v", err)
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	records, ports, relays := len(h.eng.passthroughs), len(h.eng.ports), len(h.eng.relays)
	h.eng.mu.Unlock()
	if records != 0 || ports != 0 || relays != 0 {
		t.Fatalf("after Shutdown: %d passthroughs, %d ports, %d relays", records, ports, relays)
	}
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); !errors.Is(err, errShutDown) {
		t.Fatalf("open after Shutdown: err = %v, want errShutDown", err)
	}
	if err := h.eng.ClosePassthrough(bg, "p1", "done"); !errors.Is(err, errShutDown) {
		t.Fatalf("close after Shutdown: err = %v, want errShutDown", err)
	}
}

func TestReopenAfterCloseKeepsItsPort(t *testing.T) {
	h, _ := passthroughHarness(t)
	port := testutil.FreePort(t)
	if _, err := h.eng.OpenPassthrough(bg, passthroughSpec(port)); err != nil {
		t.Fatal(err)
	}
	var reopened Passthrough
	var reopenErr error
	h.eng.afterClose = func(id string) {
		h.eng.afterClose = nil
		reopened, reopenErr = h.eng.OpenPassthrough(bg, passthroughSpec(port))
	}
	if err := h.eng.ClosePassthrough(bg, "p1", "done"); err != nil {
		t.Fatal(err)
	}
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	if reopened.Local != port {
		t.Fatalf("reopen got laptop port %d, want %d: the close still held it", reopened.Local, port)
	}
	if h.eng.PortFree(port) {
		t.Fatal("the close released the reopen's port")
	}
	h.eng.mu.Lock()
	owner := h.eng.ports[port]
	h.eng.mu.Unlock()
	if owner != passthroughOwner("p1") {
		t.Fatalf("port %d owner = %q, want the reopen", port, owner)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("::1", strconv.Itoa(port)), time.Second); err != nil {
		t.Fatalf("the reopen's [::1] relay is gone: %v", err)
	} else {
		c.Close()
	}
}
