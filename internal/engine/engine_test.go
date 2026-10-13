package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/hosts"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/transport"
)

var bg = context.Background()

func TestLocalProjectIsServedAndStopped(t *testing.T) {
	upPort := upstream(t, "ok")
	web := localProject("web", upPort)
	// A local project's loopback route needs nothing: the port is already local.
	web.Routes = append(web.Routes, config.Route{Port: testutil.FreePort(t), Role: config.RoleLoopback})
	h := newHarness(t, &config.Config{Projects: []config.Project{web}})

	if portOpen(h.httpsPort) {
		t.Fatal("proxy listens before any project is started")
	}
	if st := h.eng.Status("web"); st.State != StateStopped || st.Alias != "web.test" {
		t.Fatalf("status before start = %+v", st)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	st := h.eng.Status("web")
	if st.State != StateRunning || st.WebLocalPort != upPort || st.Alias != "web.test" {
		t.Fatalf("status = %+v", st)
	}
	if calls := h.ensureCalls(); len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "web.test" {
		t.Fatalf("EnsureHosts calls = %v", calls)
	}
	if code, body := h.getEventually(t, "web.test", "/"); code != 200 || !strings.HasPrefix(body, "ok:web.test") {
		t.Fatalf("through proxy: %d %q", code, body)
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "web.test" || sites[0].Upstream != "127.0.0.1:"+strconv.Itoa(upPort) {
		t.Fatalf("Sites = %+v", sites)
	}
	if log := h.log(); log != "" {
		t.Fatalf("a local project must not run ssh:\n%s", log)
	}

	if err := h.eng.StopProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	if st := h.eng.Status("web"); st.State != StateStopped || st.WebLocalPort != 0 || len(h.eng.Sites()) != 0 {
		t.Fatalf("expected stopped with no sites, got %+v", st)
	}
	if code, body := h.get(t, "web.test", "/"); code == 200 && strings.HasPrefix(body, "ok:") {
		t.Fatal("project still served after stop")
	}
	if portOpen(h.httpsPort) || portOpen(h.httpPort) {
		t.Fatal("stopping the last project must leave the proxy with no listeners")
	}
}

func TestRemoteProjectForwardsThroughSSH(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb))
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateRunning || st.WebLocalPort == 0 || st.WebLocalPort == 80 {
		t.Fatalf("status = %+v", st)
	}
	log := h.log()
	if !regexp.MustCompile(`-O forward -L 127\.0\.0\.1:` + strconv.Itoa(st.WebLocalPort) + `:localhost:80 mini`).MatchString(log) {
		t.Fatalf("web forward missing:\n%s", log)
	}
	if !strings.Contains(log, fmt.Sprintf("-O forward -L 127.0.0.1:%d:localhost:%d mini", lb, lb)) {
		t.Fatalf("loopback forward missing:\n%s", log)
	}
	if strings.Contains(log, "localhost:3306") {
		t.Fatal("ignored route must not be forwarded")
	}
	if status, _ := h.eng.ServerStatus("mini"); status != transport.StatusConnected {
		t.Fatalf("server status = %s", status)
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Upstream != "127.0.0.1:"+strconv.Itoa(st.WebLocalPort) {
		t.Fatalf("Sites = %+v", sites)
	}

	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(h.log(), "-O cancel") != 2 {
		t.Fatalf("expected two cancels:\n%s", h.log())
	}
	if h.eng.Status("myapp").State != StateStopped || len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 {
		t.Fatalf("expected stopped with nothing left: %+v, forwards %v", h.eng.Status("myapp"), h.forwards("mini"))
	}
	// The master stays up for the next start; only Shutdown ends it.
	if status, _ := h.eng.ServerStatus("mini"); status != transport.StatusConnected {
		t.Fatalf("server status after stop = %s", status)
	}
}

func TestServerStatusOfLocalAndUnusedServers(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	if st, msg := h.eng.ServerStatus(config.LocalServer); st != transport.StatusConnected || msg != "" {
		t.Fatalf("local = %s %q", st, msg)
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusDisconnected {
		t.Fatalf("unused server = %s", st)
	}
	if st := h.eng.Status("nope"); st.State != StateStopped || st.Alias != "" {
		t.Fatalf("unknown project status = %+v", st)
	}
}

func TestStartingTwiceIsANoOp(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb))
	// Concurrent starts: one does the work, the rest return at once.
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.eng.StartProject(bg, "myapp")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("StartProject: %v", err)
		}
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "running")
	first := h.eng.Status("myapp")
	// And a start of a running project.
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("second StartProject: %v", err)
	}
	if got := h.eng.Status("myapp"); got != first {
		t.Fatalf("status changed by a second start: %+v -> %+v", first, got)
	}
	want := [][2]int{{first.WebLocalPort, 80}, {lb, lb}}
	if want[0][0] > want[1][0] {
		want[0], want[1] = want[1], want[0]
	}
	if got := h.forwards("mini"); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("forwards = %v, want exactly %v", got, want)
	}
	if n := strings.Count(h.log(), "-O forward"); n != 2 {
		t.Fatalf("%d forward commands, want 2:\n%s", n, h.log())
	}
	if len(h.eng.Sites()) != 1 {
		t.Fatalf("Sites = %+v", h.eng.Sites())
	}
}

func TestLoopbackBindFailureIsReportedButWebStillServed(t *testing.T) {
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL="+strconv.Itoa(lb))
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) {
		t.Fatalf("expected error naming port %d, got %v", lb, err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, strconv.Itoa(lb)) || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 1 {
		t.Fatal("web route should still be proxied")
	}
	if f := h.forwards("mini"); len(f) != 1 || f[0] != [2]int{st.WebLocalPort, 80} {
		t.Fatalf("forwards = %v, want only the web forward", f)
	}
	// The forward error is a log line of the project.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project == "myapp" && ev.State == "" && strings.Contains(ev.Message, strconv.Itoa(lb)) && strings.Contains(ev.Message, "Address already in use") {
				return
			}
		case <-deadline:
			t.Fatal("no project log line for the failed forward")
		}
	}
}

func TestLoopbackPortAlreadyBoundLocally(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lb := l.Addr().(*net.TCPAddr).Port
	h := newHarness(t, remoteConfig(lb))
	// The real lookup, or a stand-in where this machine has no tool for it.
	holder := fmt.Sprintf("pid %d", os.Getpid())
	if !strings.Contains(portHolder(bg, lb), holder) {
		holder = "vite (pid 4242)"
		h.eng.holderOf = func(context.Context, int) string { return holder }
	} else {
		h.eng.holderOf = portHolder
	}

	err = h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) {
		t.Fatalf("expected error naming port %d, got %v", lb, err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, strconv.Itoa(lb)) || !strings.Contains(st.Message, "in use") {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(st.Message, "in use on this machine by ") || !strings.Contains(st.Message, holder) {
		t.Fatalf("message %q does not name the process holding the port (%s)", st.Message, holder)
	}
	if len(h.eng.Sites()) != 1 || st.WebLocalPort == 0 {
		t.Fatal("web route should still be proxied")
	}
	if strings.Contains(h.log(), fmt.Sprintf("-L 127.0.0.1:%d:", lb)) {
		t.Fatalf("a forward was requested for a port that is in use:\n%s", h.log())
	}

	// Once the port is free, starting again replaces the partial start: the
	// old web forward is cancelled, not left behind.
	l.Close()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject after freeing the port: %v", err)
	}
	st2 := h.eng.Status("myapp")
	if st2.State != StateRunning || st2.Message != "" {
		t.Fatalf("status = %+v", st2)
	}
	if f := h.forwards("mini"); len(f) != 2 {
		t.Fatalf("forwards = %v, want exactly the web and loopback forwards", f)
	}
	if n := strings.Count(h.log(), "-O cancel"); n != 1 {
		t.Fatalf("%d cancels, want 1 (the first web forward):\n%s", n, h.log())
	}
}

func TestLoopbackPortInUseByAnUnknownProcess(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lb := l.Addr().(*net.TCPAddr).Port
	h := newHarness(t, remoteConfig(lb)) // the harness lookup finds nothing
	if err := h.eng.StartProject(bg, "myapp"); err == nil {
		t.Fatal("expected an error")
	}
	msg := h.eng.Status("myapp").Message
	if !strings.Contains(msg, fmt.Sprintf("loopback port %d is already in use on this machine: ", lb)) {
		t.Fatalf("message %q should fall back to naming the port alone", msg)
	}
}

func TestLoopbackPortClashBetweenProjects(t *testing.T) {
	lb := testutil.FreePort(t)
	cfg := &config.Config{
		Servers:  []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{remoteProject("one", "mini", lb), remoteProject("two", "mini", lb)},
	}
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "one"); err != nil {
		t.Fatalf("StartProject one: %v", err)
	}
	err := h.eng.StartProject(bg, "two")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(lb)) || !strings.Contains(err.Error(), `"one"`) {
		t.Fatalf("expected a clash error naming port %d and project one, got %v", lb, err)
	}
	two := h.eng.Status("two")
	if two.State != StateError || !strings.Contains(two.Message, strconv.Itoa(lb)) || two.WebLocalPort == 0 {
		t.Fatalf("status of two = %+v", two)
	}
	if one := h.eng.Status("one"); one.State != StateRunning {
		t.Fatalf("status of one = %+v", one)
	}
	if len(h.eng.Sites()) != 2 {
		t.Fatalf("both web routes should be proxied: %+v", h.eng.Sites())
	}
	if n := strings.Count(h.log(), fmt.Sprintf("-O forward -L 127.0.0.1:%d:", lb)); n != 1 {
		t.Fatalf("loopback port forwarded %d times, want once", n)
	}

	// Stopping the second project must not take the first one's forward.
	if err := h.eng.StopProject(bg, "two"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.log(), fmt.Sprintf("-O cancel -L 127.0.0.1:%d:", lb)) {
		t.Fatalf("stopping two cancelled one's loopback forward:\n%s", h.log())
	}
	if f := h.forwards("mini"); len(f) != 2 {
		t.Fatalf("forwards = %v, want one's two forwards", f)
	}

	// The port is free for the second project once the first lets go of it.
	if err := h.eng.StopProject(bg, "one"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "two"); err != nil {
		t.Fatalf("StartProject two after one stopped: %v", err)
	}
}

func TestAuthFailureMarksProjectError(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_AUTH_FAIL=1")
	ctx, cancel := context.WithTimeout(bg, 20*time.Second)
	defer cancel()
	err := h.eng.StartProject(ctx, "myapp")
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected auth error, got %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, "Permission denied") || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v", st)
	}
	if status, msg := h.eng.ServerStatus("mini"); status != transport.StatusAuthFailed || !strings.Contains(msg, "Permission denied") {
		t.Fatalf("server status = %s %q", status, msg)
	}
	if len(h.eng.Sites()) != 0 {
		t.Fatal("no site should be published")
	}
	time.Sleep(300 * time.Millisecond) // a fourth attempt would show up by now
	if n := strings.Count(h.log(), "-N -M"); n != 3 {
		t.Fatalf("ssh was launched %d times, want 3 attempts and then no more:\n%s", n, h.log())
	}
	if len(h.ensureCalls()) != 0 || strings.Contains(h.log(), "-O forward") {
		t.Fatal("nothing else may be started for a project whose server cannot connect")
	}
}

func TestAuthFailureOnOneServerDoesNotBlockOthers(t *testing.T) {
	wrapper, _, hold := authWrapper(t)
	// The bad server's ssh does not fail before the other projects are up,
	// however slow this machine is.
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	upPort := upstream(t, "ok")
	cfg := &config.Config{
		Servers: []config.Server{{Name: "bad", SSHHost: "badhost"}, {Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{
			remoteProject("broken", "bad", testutil.FreePort(t)),
			remoteProject("myapp", "mini", testutil.FreePort(t)),
			localProject("web", upPort),
		},
	}
	h := buildHarness(t, cfg, func(o *Options) { o.SSHPath = wrapper })
	t.Cleanup(func() { os.Remove(hold) }) // before the harness shuts down: never leave ssh waiting
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	brokenErr := make(chan error, 1)
	go func() { brokenErr <- h.eng.StartProject(bg, "broken") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		return h.eng.Status("broken").State == StateStarting && strings.Contains(h.log(), "badhost")
	}, "broken to be waiting for its server")

	// While the bad server is still connecting, the others start normally.
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject myapp: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject web: %v", err)
	}
	select {
	case err := <-brokenErr:
		t.Fatalf("the failing project finished first (%v); the others did not start alongside it", err)
	default:
	}
	if code, body := h.getEventually(t, "web.test", "/"); code != 200 || !strings.HasPrefix(body, "ok:") {
		t.Fatalf("web through proxy: %d %q", code, body)
	}

	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	if err := <-brokenErr; err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected auth error for broken, got %v", err)
	}
	if st := h.eng.Status("broken"); st.State != StateError {
		t.Fatalf("broken = %+v", st)
	}
	for _, name := range []string{"myapp", "web"} {
		if st := h.eng.Status(name); st.State != StateRunning {
			t.Fatalf("%s = %+v", name, st)
		}
	}
	if st, _ := h.eng.ServerStatus("bad"); st != transport.StatusAuthFailed {
		t.Fatalf("bad server = %s", st)
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusConnected {
		t.Fatalf("mini server = %s", st)
	}
	if len(h.eng.Sites()) != 2 {
		t.Fatalf("Sites = %+v", h.eng.Sites())
	}
}

func TestStartAfterAuthFailureTriesAgain(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil {
		t.Fatal("expected auth error")
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject after the key was fixed: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStartRevivesAServerThatGaveUpUnderARunningProject(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	running := h.eng.Status("myapp")
	// The connection drops and the key is no longer accepted.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.eng.mu.Lock()
	sock := h.eng.servers["mini"].sess.SocketPath()
	h.eng.mu.Unlock()
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to give up")
	st := h.eng.Status("myapp")
	if st.State != StateDegraded || !strings.Contains(st.Message, "Permission denied") {
		t.Fatalf("status = %+v", st)
	}

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if got := h.eng.Status("myapp"); got.WebLocalPort != running.WebLocalPort || len(h.forwards("mini")) != 2 {
		t.Fatalf("status = %+v (was %+v), forwards %v", got, running, h.forwards("mini"))
	}
}

func TestDegradedProjectAnnouncesEachNewReason(t *testing.T) {
	wrapper, flag, _ := authWrapper(t)
	h := buildHarness(t, remoteConfig(testutil.FreePort(t)), func(o *Options) { o.SSHPath = wrapper })
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	// The connection drops and the key is no longer accepted: the server goes
	// through reconnecting and connecting, and gives up in the end.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st, _ := h.eng.ServerStatus("mini")
		return st == transport.StatusAuthFailed
	}, "the server to give up")
	st := h.eng.Status("myapp")
	if st.State != StateDegraded || !strings.Contains(st.Message, "auth failed") {
		t.Fatalf("status = %+v", st)
	}
	// The supervisor logs after it changed the status; its last line is what
	// announces the final reason.
	var got []Event
	deadline := time.After(5 * time.Second)
	for len(got) == 0 || got[len(got)-1].Message != "state: degraded: "+st.Message {
		select {
		case ev := <-events:
			if ev.Project == "myapp" && ev.State != "" {
				got = append(got, ev)
			}
		case <-deadline:
			t.Fatalf("the last state event does not carry the reason Status reports (%q):\n%+v", st.Message, got)
		}
	}
	// One event per distinct reason, and no more than the server said itself.
	serverLines := strings.Count(h.log(), "-N -M")*2 + 2
	if len(got) > serverLines {
		t.Fatalf("%d state events for a server that changed status at most %d times", len(got), serverLines)
	}
	for i, ev := range got {
		if ev.State != StateDegraded {
			t.Fatalf("event %d = %+v, want degraded throughout", i, ev)
		}
		if i > 0 && ev.Message == got[i-1].Message {
			t.Fatalf("the same state and reason were announced twice in a row: %q", ev.Message)
		}
	}
}

func TestRunningProjectBecomesDegradedWhenServerDrops(t *testing.T) {
	upPort := upstream(t, "ok")
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upPort))
	h := newHarness(t, cfg, "FAKE_SSH_FLAKY_ONCE=2")
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("StartProject web: %v", err)
	}
	testutil.WaitUntil(t, 5*time.Second, func() bool { return h.eng.Status("myapp").State == StateDegraded }, "degraded state")
	st := h.eng.Status("myapp")
	if !strings.Contains(st.Message, "Broken pipe") || st.WebLocalPort == 0 {
		t.Fatalf("degraded status = %+v", st)
	}
	if web := h.eng.Status("web"); web.State != StateRunning {
		t.Fatalf("a local project is not affected by a server dropping: %+v", web)
	}
	testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "recovery")
	if st := h.eng.Status("myapp"); st.Message != "" {
		t.Fatalf("recovered status = %+v", st)
	}

	// Both transitions are announced, and the supervisor's messages arrive as
	// log lines of the server.
	var states []State
	var sawSupervisor bool
	deadline := time.After(5 * time.Second)
	for len(states) < 4 || !sawSupervisor {
		select {
		case ev := <-events:
			// Degraded is announced once per reason; count the state once.
			if ev.Project == "myapp" && ev.State != "" && (len(states) == 0 || states[len(states)-1] != ev.State) {
				states = append(states, ev.State)
			}
			if ev.Server == "mini" && ev.Project == "" && strings.Contains(ev.Message, "reconnecting") {
				sawSupervisor = true
			}
		case <-deadline:
			t.Fatalf("events missing: states=%v supervisor=%v", states, sawSupervisor)
		}
	}
	want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("state events = %v, want %v", states, want)
		}
	}
}

func TestForwardLostOnReconnectPutsOnlyThatProjectInError(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-fails")
	lb1, lb2 := testutil.FreePort(t), testutil.FreePort(t)
	cfg := &config.Config{
		Servers:  []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{remoteProject("one", "mini", lb1), remoteProject("two", "mini", lb2)},
	}
	h := newHarness(t, cfg, "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	h.eng.holderOf = func(_ context.Context, port int) string {
		if port == lb1 {
			return "vite (pid 4242)"
		}
		return ""
	}
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	for _, name := range []string{"one", "two"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}

	// The master drops; while it is away something takes one's loopback port,
	// so the forward cannot be re-applied on the new master.
	if err := os.WriteFile(flag, []byte(strconv.Itoa(lb1)), 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	// The error names the port at once and the process once it is found.
	testutil.WaitUntil(t, 15*time.Second, func() bool {
		st := h.eng.Status("one")
		return st.State == StateError && strings.Contains(st.Message, "in use by")
	}, "project one to show the lost forward and who holds its port")
	one := h.eng.Status("one")
	for _, want := range []string{strconv.Itoa(lb1), "not forwarded", "vite (pid 4242)"} {
		if !strings.Contains(one.Message, want) {
			t.Fatalf("message %q does not contain %q", one.Message, want)
		}
	}
	if one.WebLocalPort == 0 || len(h.eng.Sites()) != 2 {
		t.Fatalf("one's web route must stay served: %+v, sites %v", one, h.eng.Sites())
	}
	if two := h.eng.Status("two"); two.State != StateRunning || two.Message != "" {
		t.Fatalf("two shares the server but lost nothing: %+v", two)
	}
	if got := h.unapplied("mini"); len(got) != 1 || got[0] != lb1 {
		t.Fatalf("unapplied forwards = %v, want only %d", got, lb1)
	}
	states, last := stateEvents(events, "one")
	if want := []State{StateStarting, StateRunning, StateDegraded, StateError}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events of one = %v, want %v", states, want)
	}
	if last[StateError] != "state: error: "+one.Message {
		t.Fatalf("event said %q, Status says %q", last[StateError], one.Message)
	}

	// The port is free again; the next reconnect restores the forward.
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("one").State == StateRunning }, "project one to recover")
	if st := h.eng.Status("one"); st.Message != "" || len(h.unapplied("mini")) != 0 {
		t.Fatalf("status = %+v, unapplied %v", st, h.unapplied("mini"))
	}
	if st := h.eng.Status("two"); st.State != StateRunning {
		t.Fatalf("two = %+v", st)
	}
	states, _ = stateEvents(events, "one")
	if want := []State{StateDegraded, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events of one during recovery = %v, want %v", states, want)
	}
}

func TestMasterDyingDuringReapplyIsNotALostForward(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-kills-master")
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_FORWARD_KILLS_MASTER="+flag)
	var lookups atomic.Int32
	h.eng.holderOf = func(context.Context, int) string { lookups.Add(1); return "" }
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	// The connection drops, and the next master dies while the forwards are
	// being re-applied on it. The one after that is fine.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 20*time.Second, func() bool {
		_, err := os.Stat(flag)
		return os.IsNotExist(err) && h.eng.Status("myapp").State == StateRunning
	}, "the project to be running on the third master")
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStarting, StateRunning, StateDegraded, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v: a master that died is not a port that could not be bound", states, want)
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("%d process lookups for forwards that only failed because the master died", n)
	}
	if got := h.unapplied("mini"); len(got) != 0 {
		t.Fatalf("unapplied forwards = %v", got)
	}
}

// loseLoopbackForward starts myapp and makes its loopback forward fail to
// re-apply after a dropped connection, leaving the project in the error
// state. The harness must have FAKE_SSH_FORWARD_FAIL_FILE set to flag.
func loseLoopbackForward(t *testing.T, h *harness, flag string, lb int) ProjectStatus {
	t.Helper()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, []byte(strconv.Itoa(lb)), 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateError }, "the lost forward to show")
	return h.eng.Status("myapp")
}

func TestStopAfterALostForwardGoesFromErrorToStopped(t *testing.T) {
	for _, how := range []string{"StopProject", "Shutdown"} {
		t.Run(how, func(t *testing.T) {
			flag := filepath.Join(t.TempDir(), "forward-fails")
			lb := testutil.FreePort(t)
			h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			loseLoopbackForward(t, h, flag, lb)
			if states, _ := stateEvents(events, "myapp"); fmt.Sprint(states) != fmt.Sprint([]State{StateStarting, StateRunning, StateDegraded, StateError}) {
				t.Fatalf("state events before the stop = %v", states)
			}
			var err error
			if how == "Shutdown" {
				err = h.eng.Shutdown(bg)
			} else {
				err = h.eng.StopProject(bg, "myapp")
			}
			if err != nil {
				t.Fatal(err)
			}
			states, _ := stateEvents(events, "myapp")
			if want := []State{StateStopped}; fmt.Sprint(states) != fmt.Sprint(want) {
				t.Fatalf("state events of the stop = %v, want %v", states, want)
			}
			if st := h.eng.Status("myapp"); st.State != StateStopped || st.Message != "" {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestStatusDuringTeardownKeepsTheAnnouncedState(t *testing.T) {
	for _, variant := range []string{"healthy", "after a lost forward"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			flag, wait := filepath.Join(dir, "forward-fails"), filepath.Join(dir, "cancel-waits")
			lb := testutil.FreePort(t)
			h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag, "FAKE_SSH_CANCEL_WAIT="+wait)
			t.Cleanup(func() { os.Remove(wait) }) // never leave a cancel waiting
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			var before ProjectStatus
			if variant == "healthy" {
				if err := h.eng.StartProject(bg, "myapp"); err != nil {
					t.Fatal(err)
				}
				before = h.eng.Status("myapp")
			} else {
				before = loseLoopbackForward(t, h, flag, lb)
			}
			stateEvents(events, "myapp") // drop what led here

			// The teardown stops inside its first forward cancel.
			if err := os.WriteFile(wait, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan error, 1)
			go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the teardown to reach a cancel")
			for i := 0; i < 20; i++ {
				during := h.eng.Status("myapp")
				if during.State != before.State || during.Message != before.Message {
					t.Fatalf("status during the teardown = %+v, want what was last announced: %+v", during, before)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if states, _ := stateEvents(events, "myapp"); len(states) != 0 {
				t.Fatalf("state events during the teardown: %v", states)
			}

			if err := os.Remove(wait); err != nil {
				t.Fatal(err)
			}
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if st := h.eng.Status("myapp"); st.State != StateStopped {
				t.Fatalf("status = %+v", st)
			}
			if states, _ := stateEvents(events, "myapp"); fmt.Sprint(states) != fmt.Sprint([]State{StateStopped}) {
				t.Fatalf("state events of the stop = %v, want [stopped]", states)
			}
		})
	}
}

func TestStartDuringAStopWaitsForItAndThenStarts(t *testing.T) {
	wait := filepath.Join(t.TempDir(), "cancel-waits")
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_CANCEL_WAIT="+wait)
	t.Cleanup(func() { os.Remove(wait) }) // never leave a cancel waiting
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	stateEvents(events, "myapp") // drop what led here

	// The stop is held inside its first forward cancel.
	if err := os.WriteFile(wait, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.eng.StopProject(bg, "myapp") }()
	testutil.WaitUntil(t, 10*time.Second, func() bool { return strings.Contains(h.log(), "-O cancel") }, "the teardown to reach a cancel")

	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	// It has nothing to report while the stop is under way. (A start that
	// only gets going after this pause is caught by what follows.)
	select {
	case err := <-started:
		t.Fatalf("StartProject returned %v while the project was being stopped", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.Remove(wait); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("StartProject after the stop: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never finished")
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.WebLocalPort == 0 {
		t.Fatalf("status = %+v: the start requested during the stop was lost", st)
	}
	if f := h.forwards("mini"); len(f) != 2 || len(h.unapplied("mini")) != 0 || len(h.eng.Sites()) != 1 {
		t.Fatalf("forwards = %v, unapplied %v, sites %v", f, h.unapplied("mini"), h.eng.Sites())
	}
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStopped, StateStarting, StateRunning}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v", states, want)
	}
}

func TestFailedCancelAfterALostForwardEndsInErrorNamingTheCancel(t *testing.T) {
	dir := t.TempDir()
	flag, block := filepath.Join(dir, "forward-fails"), filepath.Join(dir, "cancel-fails")
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag, "FAKE_SSH_CANCEL_FAIL="+block)
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	loseLoopbackForward(t, h, flag, lb)
	stateEvents(events, "myapp")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err == nil {
		t.Fatal("expected the stop to report the failed cancel")
	}
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, "stop myapp") || !strings.Contains(st.Message, "cancel") {
		t.Fatalf("status = %+v, want the failed stop", st)
	}
	states, last := stateEvents(events, "myapp")
	if fmt.Sprint(states) != fmt.Sprint([]State{StateError}) || last[StateError] != "state: error: "+st.Message {
		t.Fatalf("state events = %v (%q), want one error carrying %q", states, last[StateError], st.Message)
	}
	if f := h.forwards("mini"); len(f) != 1 {
		t.Fatalf("forwards = %v, want the web forward kept for a retry", f)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.forwards("mini")) != 0 {
		t.Fatalf("status = %+v, forwards %v", st, h.forwards("mini"))
	}
}

func TestStoppingDoesNotLookLikeLosingForwards(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	var lookups atomic.Int32
	h.eng.holderOf = func(context.Context, int) string { lookups.Add(1); return "" }
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	states, _ := stateEvents(events, "myapp")
	if want := []State{StateStarting, StateRunning, StateStopped}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("state events = %v, want %v", states, want)
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("%d process lookups for forwards the engine cancelled itself", n)
	}
}

func TestStatusDoesNotRunAheadOfTheLostForwardEvent(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	before := h.eng.Status("myapp")
	stateEvents(events, "myapp") // drop what led here

	// The session changes first and tells the engine afterwards. Stand
	// between the two: the project has a forward its connected session does
	// not have, and the session has not said anything yet.
	lost := testutil.FreePort(t)
	h.eng.mu.Lock()
	run := h.eng.runs["myapp"]
	run.forwards = append(run.forwards, forward{lost, lost})
	h.eng.mu.Unlock()
	if st := h.eng.Status("myapp"); st != before {
		t.Fatalf("status before the session reported = %+v, want what was last announced: %+v", st, before)
	}
	if states, _ := stateEvents(events, "myapp"); len(states) != 0 {
		t.Fatalf("state events before the session reported: %v", states)
	}

	// Its message arrives: the event and Status change together.
	h.eng.serverLog("mini", "mini: connected")
	st := h.eng.Status("myapp")
	if st.State != StateError || !strings.Contains(st.Message, strconv.Itoa(lost)) {
		t.Fatalf("status = %+v, want the error naming port %d", st, lost)
	}
	states, last := stateEvents(events, "myapp")
	if fmt.Sprint(states) != fmt.Sprint([]State{StateError}) || last[StateError] != "state: error: "+st.Message {
		t.Fatalf("events %v (%q), status %+v", states, last[StateError], st)
	}
}

// slowLookup replaces the engine's process lookup with one that reports each
// call on asked and answers "vite (pid 4242)" once answer has been called.
func slowLookup(t *testing.T, h *harness) (asked chan struct{}, answer func()) {
	t.Helper()
	asked = make(chan struct{}, 8)
	release := make(chan struct{})
	h.eng.holderOf = func(context.Context, int) string {
		asked <- struct{}{}
		<-release // the lookup is slow
		return "vite (pid 4242)"
	}
	var once sync.Once
	answer = func() { once.Do(func() { close(release) }) }
	t.Cleanup(answer) // before the harness shuts down: never leave a lookup waiting
	return asked, answer
}

func TestLostForwardIsAnnouncedWhileItsHolderIsStillLookedUp(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-fails")
	lb1, lb2 := testutil.FreePort(t), testutil.FreePort(t)
	cfg := &config.Config{
		Servers:  []config.Server{{Name: "mini", SSHHost: "mini"}},
		Projects: []config.Project{remoteProject("one", "mini", lb1), remoteProject("two", "mini", lb2)},
	}
	h := newHarness(t, cfg, "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	asked, answer := slowLookup(t, h)
	events, unsub := h.eng.Events().Subscribe(1024)
	defer unsub()
	for _, name := range []string{"one", "two"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}
	if err := os.WriteFile(flag, []byte(strconv.Itoa(lb1)), 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	select {
	case <-asked:
	case <-time.After(15 * time.Second):
		t.Fatal("the holder of the lost port was never looked up")
	}

	// The lookup has not answered. The error is out all the same, naming the
	// port, and Status says what the event said.
	portOnly := fmt.Sprintf("loopback port %d is not forwarded: ssh could not bind it on this machine", lb1)
	waitStateEvent(t, events, "one", "state: error: "+portOnly)
	if st := h.eng.Status("one"); st.State != StateError || st.Message != portOnly {
		t.Fatalf("status while the lookup runs = %+v, want the announced error %q", st, portOnly)
	}

	// Nothing on the server waits for the lookup either: another project
	// cancels its forwards on the same session.
	stopped := make(chan error, 1)
	go func() { stopped <- h.eng.StopProject(bg, "two") }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stopping another project on the server waited for the process lookup")
	}

	// The holder is added when it is found, in an event of its own; Status
	// changes with that event, not before it.
	answer()
	waitStateEvent(t, events, "one", "state: error: "+portOnly+" (in use by vite (pid 4242))")
	if st := h.eng.Status("one"); st.State != StateError || st.Message != portOnly+" (in use by vite (pid 4242))" {
		t.Fatalf("status after the lookup = %+v", st)
	}
	if states, _ := stateEvents(events, "one"); len(states) != 0 {
		t.Fatalf("state events after the holder was announced: %v", states)
	}
}

func TestHolderFoundTooLateIsDropped(t *testing.T) {
	for _, variant := range []string{"project stopped", "forward restored"} {
		t.Run(variant, func(t *testing.T) {
			flag := filepath.Join(t.TempDir(), "forward-fails")
			lb := testutil.FreePort(t)
			h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
			asked, answer := slowLookup(t, h)
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			loseLoopbackForward(t, h, flag, lb)
			select {
			case <-asked:
			case <-time.After(15 * time.Second):
				t.Fatal("the holder of the lost port was never looked up")
			}
			// While the lookup runs, what it is about goes away.
			if err := os.Remove(flag); err != nil {
				t.Fatal(err)
			}
			want := ProjectStatus{State: StateStopped, Alias: "myapp.test"}
			if variant == "project stopped" {
				if err := h.eng.StopProject(bg, "myapp"); err != nil {
					t.Fatal(err)
				}
			} else {
				h.dropMaster(t, "mini")
				testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "the forward to be restored")
				want = h.eng.Status("myapp")
			}
			stateEvents(events, "myapp") // drop what led here

			answer()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return !strings.Contains(engineGoroutines(), "lookupHolder") }, "the lookup to finish")
			if st := h.eng.Status("myapp"); st != want {
				t.Fatalf("status = %+v after a late answer, want it unchanged: %+v", st, want)
			}
			if states, last := stateEvents(events, "myapp"); len(states) != 0 {
				t.Fatalf("state events after a late answer: %v %q", states, last)
			}
		})
	}
}

func TestShutdownEndsARunningHolderLookup(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-fails")
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	asked := make(chan struct{}, 8)
	h.eng.holderOf = func(ctx context.Context, _ int) string {
		asked <- struct{}{}
		<-ctx.Done() // as slow as it is allowed to be
		return ""
	}
	loseLoopbackForward(t, h, flag, lb)
	select {
	case <-asked:
	case <-time.After(15 * time.Second):
		t.Fatal("the holder of the lost port was never looked up")
	}
	done := make(chan error, 1)
	go func() { done <- h.eng.Shutdown(bg) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown waited for a process lookup")
	}
	h.assertNothingLeft(t)
}

func TestStartRetriesAProjectWhoseForwardWasLost(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "forward-fails")
	lb := testutil.FreePort(t)
	h := newHarness(t, remoteConfig(lb), "FAKE_SSH_FORWARD_FAIL_FILE="+flag)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, []byte(strconv.Itoa(lb)), 0o644); err != nil {
		t.Fatal(err)
	}
	h.dropMaster(t, "mini")
	testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateError }, "the lost forward to show")
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	// Starting is not a no-op here: it replaces the broken set of forwards.
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status = %+v", st)
	}
	if f := h.forwards("mini"); len(f) != 2 || len(h.unapplied("mini")) != 0 {
		t.Fatalf("forwards = %v, unapplied %v", f, h.unapplied("mini"))
	}
}

func TestMasterDroppingBeforeForwardsAreAdded(t *testing.T) {
	for _, variant := range []string{"forwards apply on reconnect", "a forward fails on reconnect"} {
		t.Run(variant, func(t *testing.T) {
			fails := variant == "a forward fails on reconnect"
			lb := testutil.FreePort(t)
			var env []string
			if fails {
				env = append(env, "FAKE_SSH_FORWARD_FAIL="+strconv.Itoa(lb))
			}
			var h *harness
			// The hosts step sits between connecting and adding forwards:
			// lose the master exactly there.
			h = buildHarness(t, remoteConfig(lb), func(o *Options) {
				o.EnsureHosts = func(context.Context, []string) error {
					h.dropMaster(t, "mini")
					testutil.WaitUntil(t, 10*time.Second, func() bool {
						st, _ := h.eng.ServerStatus("mini")
						return st != transport.StatusConnected
					}, "the drop to be noticed")
					return nil
				}
			}, env...)
			if err := h.eng.Start(bg); err != nil {
				t.Fatal(err)
			}
			events, unsub := h.eng.Events().Subscribe(1024)
			defer unsub()
			err := h.eng.StartProject(bg, "myapp")
			// The forwards were only queued. Whatever the project shows now,
			// it must not be running on forwards that do not exist.
			if st := h.eng.Status("myapp"); st.State == StateRunning && len(h.unapplied("mini")) != 0 {
				t.Fatalf("running with unapplied forwards %v (StartProject returned %v)", h.unapplied("mini"), err)
			}
			if !fails {
				testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateRunning }, "running once the master is back")
				if got := h.unapplied("mini"); len(got) != 0 {
					t.Fatalf("running with unapplied forwards %v", got)
				}
				return
			}
			testutil.WaitUntil(t, 15*time.Second, func() bool { return h.eng.Status("myapp").State == StateError }, "error once the master is back")
			st := h.eng.Status("myapp")
			if !strings.Contains(st.Message, strconv.Itoa(lb)) || st.WebLocalPort == 0 || len(h.eng.Sites()) != 1 {
				t.Fatalf("status = %+v, sites %v", st, h.eng.Sites())
			}
			if got := h.unapplied("mini"); len(got) != 1 || got[0] != lb {
				t.Fatalf("unapplied = %v, want only the loopback port %d", got, lb)
			}
			states, _ := stateEvents(events, "myapp")
			for _, s := range states {
				if s == StateRunning {
					t.Fatalf("the project was announced running without its forward: %v", states)
				}
			}
		})
	}
}

func TestUnknownProjectAndMissingWebRoute(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects[0].Routes[0].Role = config.RoleIgnore
	cfg.Projects = append(cfg.Projects, config.Project{
		Name: "lost", Server: "gone", Alias: "lost.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb}},
	})
	h := newHarness(t, cfg)
	if err := h.eng.StartProject(bg, "nope"); err == nil {
		t.Fatal("expected unknown project error")
	}
	if err := h.eng.StopProject(bg, "nope"); err == nil {
		t.Fatal("expected unknown project error on stop")
	}
	if err := h.eng.StartProject(bg, "myapp"); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("expected missing web route error, got %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "web") {
		t.Fatalf("status = %+v", st)
	}
	if err := h.eng.StartProject(bg, "lost"); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("expected unknown server error, got %v", err)
	}
	if h.log() != "" || len(h.ensureCalls()) != 0 || len(h.eng.Sites()) != 0 {
		t.Fatal("a project that cannot start must not start anything")
	}
}

func TestEventsCarryStateChangesAndLogs(t *testing.T) {
	h := newHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}})
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	h.getEventually(t, "web.test", "/hello")
	var sawRunning, sawAccess bool
	deadline := time.After(5 * time.Second)
	for !(sawRunning && sawAccess) {
		select {
		case ev := <-events:
			if ev.Time.IsZero() {
				t.Fatalf("event without a time: %+v", ev)
			}
			if ev.Project == "web" && ev.State == StateRunning && strings.Contains(ev.Message, "running") {
				sawRunning = true
			}
			if ev.Project == "web" && ev.State == "" && strings.Contains(ev.Message, "/hello") {
				sawAccess = true
			}
		case <-deadline:
			t.Fatalf("events missing: running=%v access=%v", sawRunning, sawAccess)
		}
	}
}

func TestHostsFailureLeavesProjectStopped(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)))
	h.setHostsErr(errors.New("sudo: a password is required"))
	err := h.eng.StartProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "a password is required") {
		t.Fatalf("expected the hosts error, got %v", err)
	}
	st := h.eng.Status("myapp")
	if st.State != StateStopped || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v, want stopped", st)
	}
	for _, want := range []string{"myapp.test", "not resolvable", "start the project again"} {
		if !strings.Contains(st.Message, want) {
			t.Fatalf("message %q does not contain %q", st.Message, want)
		}
	}
	if len(h.eng.Sites()) != 0 || len(h.forwards("mini")) != 0 || strings.Contains(h.log(), "-O forward") {
		t.Fatalf("nothing may be started when the alias cannot resolve; sites %v, log:\n%s", h.eng.Sites(), h.log())
	}

	// Retrying after the cause is fixed works.
	h.setHostsErr(nil)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateRunning || st.Message != "" {
		t.Fatalf("status after retry = %+v", st)
	}
}

func TestStoppingInterruptsAHostsUpdateThatWaits(t *testing.T) {
	for _, how := range []string{"StopProject", "Shutdown"} {
		t.Run(how, func(t *testing.T) {
			cfg := &config.Config{Projects: []config.Project{
				localProject("alpha", upstream(t, "alpha")),
				localProject("beta", upstream(t, "beta")),
			}}
			entered := make(chan struct{}, 8)
			h := buildHarness(t, cfg, func(o *Options) {
				// Like sudo waiting for a password: it only ends when told to.
				o.EnsureHosts = func(ctx context.Context, _ []string) error {
					entered <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}
			})
			if err := h.eng.Start(bg); err != nil {
				t.Fatal(err)
			}
			alpha, beta := make(chan error, 1), make(chan error, 1)
			go func() { alpha <- h.eng.StartProject(bg, "alpha") }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the hosts update never began")
			}
			// beta waits for its turn behind alpha's update.
			go func() { beta <- h.eng.StartProject(bg, "beta") }()
			testutil.WaitUntil(t, 10*time.Second, func() bool { return h.eng.Status("beta").State == StateStarting }, "beta to be starting")

			done := make(chan error, 1)
			go func() {
				if how == "Shutdown" {
					done <- h.eng.Shutdown(bg)
					return
				}
				// The one that waits for its turn first: it must not have to
				// wait for the other's update to end.
				if err := h.eng.StopProject(bg, "beta"); err != nil {
					done <- err
					return
				}
				done <- h.eng.StopProject(bg, "alpha")
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("%s hung behind a hosts update that was waiting", how)
			}
			for name, started := range map[string]chan error{"alpha": alpha, "beta": beta} {
				select {
				case err := <-started:
					if err == nil {
						t.Fatalf("the interrupted start of %s must report an error", name)
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("the start of %s never returned", name)
				}
				if st := h.eng.Status(name); st.State != StateStopped || st.Message != "" {
					t.Fatalf("%s = %+v", name, st)
				}
			}
			if len(entered) != 0 {
				t.Fatal("a hosts update began for a start that had already been interrupted")
			}
			if len(h.eng.Sites()) != 0 {
				t.Fatalf("Sites = %+v", h.eng.Sites())
			}
		})
	}
}

func TestInvalidAliasFailsOnlyItsOwnProject(t *testing.T) {
	bad := localProject("bad", upstream(t, "bad"))
	bad.Alias = "Bad_Alias.dev"
	cfg := &config.Config{Projects: []config.Project{bad, localProject("web", upstream(t, "ok"))}}
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	h := buildHarness(t, cfg, func(o *Options) {
		o.EnsureHosts = func(_ context.Context, aliases []string) error {
			_, err := hosts.Ensure(hostsFile, aliases, func(a []string) error { return hosts.Apply(hostsFile, a) })
			return err
		}
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatalf("a project with a valid alias must start beside one with an invalid alias: %v", err)
	}
	if got, _ := os.ReadFile(hostsFile); !strings.Contains(string(got), "127.0.0.1 web.test") {
		t.Fatalf("hosts file = %q", got)
	}
	err := h.eng.StartProject(bg, "bad")
	if err == nil || !strings.Contains(err.Error(), "Bad_Alias.dev") {
		t.Fatalf("expected an invalid alias error, got %v", err)
	}
	if st := h.eng.Status("bad"); st.State != StateError {
		t.Fatalf("bad = %+v", st)
	}
	if st := h.eng.Status("web"); st.State != StateRunning {
		t.Fatalf("web = %+v", st)
	}
}

func TestStoppingOneProjectKeepsOthersServing(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{
		localProject("alpha", upstream(t, "alpha")),
		localProject("beta", upstream(t, "beta")),
	}}
	h := newHarness(t, cfg)
	for _, name := range []string{"alpha", "beta"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		if code, body := h.getEventually(t, name+".test", "/"); code != 200 || !strings.HasPrefix(body, name+":") {
			t.Fatalf("%s through proxy: %d %q", name, code, body)
		}
	}
	if err := h.eng.StopProject(bg, "alpha"); err != nil {
		t.Fatal(err)
	}
	if code, body := h.get(t, "beta.test", "/"); code != 200 || !strings.HasPrefix(body, "beta:") {
		t.Fatalf("beta after alpha stopped: %d %q", code, body)
	}
	if code, body := h.get(t, "alpha.test", "/"); code == 200 && strings.HasPrefix(body, "alpha:") {
		t.Fatal("alpha still served after stop")
	}
	if sites := h.eng.Sites(); len(sites) != 1 || sites[0].Alias != "beta.test" {
		t.Fatalf("Sites = %+v", sites)
	}
}

func TestStartFailsWhenAProxyPortIsTaken(t *testing.T) {
	// The proxy listens on TCP for HTTPS and HTTP, and on UDP at the HTTPS
	// port for HTTP/3.
	for _, which := range []string{"https", "http", "https-udp"} {
		t.Run(which, func(t *testing.T) {
			var holder interface{ Close() error }
			var held int
			if which == "https-udp" {
				pc, err := net.ListenPacket("udp", ":0")
				if err != nil {
					t.Fatal(err)
				}
				holder, held = pc, pc.LocalAddr().(*net.UDPAddr).Port
			} else {
				l, err := net.Listen("tcp", ":0")
				if err != nil {
					t.Fatal(err)
				}
				holder, held = l, l.Addr().(*net.TCPAddr).Port
			}
			defer holder.Close()
			h := buildHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}, func(o *Options) {
				if which == "http" {
					o.HTTPPort = held
				} else {
					o.HTTPSPort = held
				}
			})
			h.httpsPort, h.httpPort = h.opts.HTTPSPort, h.opts.HTTPPort
			h.eng.holderOf = func(_ context.Context, port int) string { return fmt.Sprintf("holder-of-%d", port) }
			err := h.eng.Start(bg)
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(held)) {
				t.Fatalf("expected an error naming port %d, got %v", held, err)
			}
			// The lookup covers TCP listeners only.
			if named := strings.Contains(err.Error(), fmt.Sprintf("holder-of-%d", held)); named != (which != "https-udp") {
				t.Fatalf("error %q: holder named = %v", err, named)
			}
			if err := h.eng.StartProject(bg, "web"); err == nil {
				t.Fatal("no project may start when the engine failed to start")
			}
			if st := h.eng.Status("web"); st.State != StateStopped || len(h.eng.Sites()) != 0 || len(h.ensureCalls()) != 0 {
				t.Fatalf("status = %+v", st)
			}
			holder.Close()
			// The conflict gone, the same engine starts.
			if err := h.eng.Start(bg); err != nil {
				t.Fatalf("Start after the port was freed: %v", err)
			}
			if err := h.eng.StartProject(bg, "web"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStopProjectReportsFailedCancelAndCanBeRetried(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_CANCEL_FAIL="+block)
	if err := h.eng.StartProject(bg, "myapp"); err != nil {
		t.Fatal(err)
	}
	err := h.eng.StopProject(bg, "myapp")
	if err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected a cancel error, got %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateError || !strings.Contains(st.Message, "cancel") || st.WebLocalPort != 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.eng.Sites()) != 0 {
		t.Fatal("the site must be gone even when a forward could not be cancelled")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.StopProject(bg, "myapp"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.forwards("mini")) != 0 {
		t.Fatalf("status = %+v, forwards %v", st, h.forwards("mini"))
	}
}

func TestShutdownStopsEverythingAndKeepsHosts(t *testing.T) {
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, cfg, func(o *Options) {
		o.EnsureHosts = func(_ context.Context, aliases []string) error {
			_, err := hosts.Ensure(hostsFile, aliases, func(a []string) error { return hosts.Apply(hostsFile, a) })
			return err
		}
	})
	if err := h.eng.Start(bg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"myapp", "web"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatalf("StartProject %s: %v", name, err)
		}
	}
	h.getEventually(t, "web.test", "/")
	before, _ := os.ReadFile(hostsFile)
	if !strings.Contains(string(before), "127.0.0.1 myapp.test") || !strings.Contains(string(before), "127.0.0.1 web.test") {
		t.Fatalf("hosts file = %q", before)
	}

	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, name := range []string{"myapp", "web"} {
		if st := h.eng.Status(name); st.State != StateStopped {
			t.Fatalf("%s = %+v", name, st)
		}
	}
	if !strings.Contains(h.log(), "-O exit mini") {
		t.Fatalf("the master was not asked to exit:\n%s", h.log())
	}
	if st, _ := h.eng.ServerStatus("mini"); st != transport.StatusDisconnected {
		t.Fatalf("server = %s", st)
	}
	if after, _ := os.ReadFile(hostsFile); string(after) != string(before) {
		t.Fatalf("Shutdown changed the hosts file:\n%s", after)
	}
	h.assertNothingLeft(t)

	if err := h.eng.StartProject(bg, "web"); err == nil {
		t.Fatal("StartProject after Shutdown must fail")
	}
	if err := h.eng.Start(bg); err == nil {
		t.Fatal("Start after Shutdown must fail")
	}
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestShutdownReturnsTeardownErrorsAndStillStopsTheRest(t *testing.T) {
	block := filepath.Join(t.TempDir(), "cancel-fails")
	if err := os.WriteFile(block, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := remoteConfig(testutil.FreePort(t))
	cfg.Projects = append(cfg.Projects, localProject("web", upstream(t, "ok")))
	h := newHarness(t, cfg, "FAKE_SSH_CANCEL_FAIL="+block)
	for _, name := range []string{"myapp", "web"} {
		if err := h.eng.StartProject(bg, name); err != nil {
			t.Fatal(err)
		}
	}
	err := h.eng.Shutdown(bg)
	if err == nil || !strings.Contains(err.Error(), "myapp") || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("expected Shutdown to report the failed cancel of myapp, got %v", err)
	}
	if !strings.Contains(h.log(), "-O exit mini") {
		t.Fatal("the master must still be stopped")
	}
	if st := h.eng.Status("web"); st.State != StateStopped {
		t.Fatalf("web = %+v", st)
	}
	h.assertNothingLeft(t)
}

func TestShutdownInterruptsAStartInProgress(t *testing.T) {
	h := newHarness(t, remoteConfig(testutil.FreePort(t)), "FAKE_SSH_HANG=1")
	started := make(chan error, 1)
	go func() { started <- h.eng.StartProject(bg, "myapp") }()
	testutil.WaitUntil(t, 5*time.Second, func() bool {
		return h.eng.Status("myapp").State == StateStarting && strings.Contains(h.log(), "-N -M")
	}, "the start to be waiting for ssh")
	begin := time.Now()
	if err := h.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("Shutdown waited %s for a start that could not finish", d)
	}
	if err := <-started; err == nil {
		t.Fatal("the interrupted start must report an error")
	}
	if st := h.eng.Status("myapp"); st.State != StateStopped || len(h.eng.Sites()) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestShutdownLeavesAProxyTakenOverByAnotherEngine(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}
	old := newHarness(t, cfg)
	h := newHarness(t, cfg) // takes over the process-wide proxy and its log sink
	events, unsub := h.eng.Events().Subscribe(256)
	defer unsub()
	if err := old.eng.Shutdown(bg); err != nil {
		t.Fatalf("Shutdown of the replaced engine: %v", err)
	}
	if err := h.eng.StartProject(bg, "web"); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.getEventually(t, "web.test", "/hello"); code != 200 {
		t.Fatalf("status %d through the newer engine's proxy", code)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Project == "web" && strings.Contains(ev.Message, "/hello") {
				return
			}
		case <-deadline:
			t.Fatal("the newer engine lost its proxy log lines when the older one shut down")
		}
	}
}

func TestProjectsBeforeStartAreRefused(t *testing.T) {
	h := buildHarness(t, &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}, nil)
	if err := h.eng.StartProject(bg, "web"); err == nil {
		t.Fatal("StartProject before Start must fail")
	}
	if err := h.eng.StopProject(bg, "web"); err != nil {
		t.Fatalf("stopping a project that never ran is a no-op, got %v", err)
	}
	if st := h.eng.Status("web"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

func TestConfigIsSnapshotted(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{localProject("web", upstream(t, "ok"))}}
	h := newHarness(t, cfg)
	cfg.Projects[0].Alias = "changed.test"
	got := h.eng.Config()
	if got.Projects[0].Alias != "web.test" {
		t.Fatalf("engine config followed a later change by the caller: %+v", got.Projects[0])
	}
	got.Projects[0].Routes[0].Port = 1
	if h.eng.Config().Projects[0].Routes[0].Port == 1 {
		t.Fatal("Config must return a copy")
	}
}

// TestConcurrentUse hammers the whole API from many goroutines. It asserts
// little by itself; the race detector and the leak checks in the harness
// cleanup do the work.
func TestConcurrentUse(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini"}, {Name: "maxi", SSHHost: "maxi"}},
		Projects: []config.Project{
			remoteProject("one", "mini", testutil.FreePort(t)),
			remoteProject("two", "mini", testutil.FreePort(t)),
			remoteProject("three", "maxi", testutil.FreePort(t)),
			localProject("alpha", upstream(t, "alpha")),
			localProject("beta", upstream(t, "beta")),
		},
	}
	h := newHarness(t, cfg)
	names := []string{"one", "two", "three", "alpha", "beta"}
	stop := make(chan struct{})
	var readers, workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			events, unsub := h.eng.Events().Subscribe(8)
			defer unsub()
			for {
				select {
				case <-stop:
					return
				case <-events:
				default:
				}
				for _, n := range names {
					h.eng.Status(n)
				}
				h.eng.Sites()
				h.eng.ServerStatus("mini")
				h.eng.Config()
				time.Sleep(time.Millisecond)
			}
		}()
	}
	for _, n := range names {
		for w := 0; w < 2; w++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := 0; i < 3; i++ {
					h.eng.StartProject(bg, n) // may race a stop; either outcome is fine
					h.eng.StopProject(bg, n)
				}
			}()
		}
	}
	workers.Wait()
	close(stop)
	readers.Wait()

	// After the storm every project starts cleanly and holds exactly its own
	// forwards.
	for _, n := range names {
		if err := h.eng.StopProject(bg, n); err != nil {
			t.Fatalf("StopProject %s: %v", n, err)
		}
	}
	if f := append(h.forwards("mini"), h.forwards("maxi")...); len(f) != 0 {
		t.Fatalf("forwards left after stopping everything: %v", f)
	}
	for _, n := range names {
		if err := h.eng.StartProject(bg, n); err != nil {
			t.Fatalf("StartProject %s: %v", n, err)
		}
	}
	if len(h.eng.Sites()) != 5 || len(h.forwards("mini")) != 4 || len(h.forwards("maxi")) != 2 {
		t.Fatalf("sites %v, mini %v, maxi %v", h.eng.Sites(), h.forwards("mini"), h.forwards("maxi"))
	}
	for _, n := range []string{"alpha", "beta"} {
		if code, body := h.getEventually(t, n+".test", "/"); code != 200 || !strings.HasPrefix(body, n+":") {
			t.Fatalf("%s through proxy: %d %q", n, code, body)
		}
	}
}
